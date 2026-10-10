package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// R79: a short spoken command of the board's voice assistant (a few seconds
// after «Джарвис») is transcribed by the same Whisper as the разбор
// recordings, but on its own lane: a long call being transcribed does not make
// the command wait (the call's lock is not taken), it runs at normal priority
// with all CPU threads, and Russian is forced. The audio is cleaned for a
// room microphone (rumble and hiss cut, loudness evened) and padded with
// silence so the VAD does not clip the first word.
//
//	ASR_CMD_MODEL   Whisper for commands (default: the same as ASR_MODEL,
//	                already on disk; «medium» is more accurate and about
//	                three times slower)

var (
	cmdOnce sync.Once
	cmdASR  *LocalASR
)

// Command: the LocalASR used for voice commands (shares the tools, the VAD
// and, by default, the model with a).
func (a *LocalASR) Command() *LocalASR {
	cmdOnce.Do(func() {
		m := strings.ToLower(strings.TrimSpace(os.Getenv("ASR_CMD_MODEL")))
		if m == "" {
			m = a.Model
		}
		c := &LocalASR{Dir: a.Dir, Model: m, Lang: "ru", SherpaURL: a.SherpaURL, VADURL: a.VADURL, FFmpegURL: a.FFmpegURL,
			ExtraArgs: a.ExtraArgs, HTTP: a.HTTP, Chunk: a.Chunk}
		if m == a.Model {
			c.ModelURL = a.ModelURL
		}
		c.Threads = runtime.NumCPU()
		if c.Threads > 8 {
			c.Threads = 8
		}
		cmdASR = c.defaults()
	})
	return cmdASR
}

// TranscribeCommand: a short recording (up to ~30 s) → text.
func (a *LocalASR) TranscribeCommand(ctx context.Context, audio []byte, mime string) (string, error) {
	if len(audio) == 0 {
		return "", errors.New("пустая запись")
	}
	// the shared tools first, under the main lock of preparation only
	if _, err := a.Prepare(ctx); err != nil {
		return "", fmt.Errorf("распознавание на сервере не готово: %v", err)
	}
	c := a.Command()
	c.run.Lock()
	defer c.run.Unlock()
	p, err := c.Prepare(ctx)
	if err != nil {
		return "", fmt.Errorf("распознавание на сервере не готово: %v", err)
	}
	work, err := os.MkdirTemp("", "bs-asr-cmd-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	in := filepath.Join(work, "in"+audioExt(mime))
	if err := os.WriteFile(in, audio, 0o600); err != nil {
		return "", err
	}
	wav := filepath.Join(work, "cmd.wav")
	cmd := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-i", in,
		"-af", "highpass=f=90,lowpass=f=7600,dynaudnorm=f=150:g=15,adelay=400|400,apad=pad_dur=0.6",
		"-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-t", "40", wav)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("запись не читается (ffmpeg): %s", tail(string(out), err))
	}
	args := []string{
		"--silero-vad-model=" + p.vad,
		"--whisper-encoder=" + p.encoder,
		"--whisper-decoder=" + p.decoder,
		"--whisper-language=ru",
		"--whisper-task=transcribe",
		"--tokens=" + p.tokens,
		"--num-threads=" + strconv.Itoa(c.Threads),
	}
	args = append(append(args, c.ExtraArgs...), wav)
	run := exec.CommandContext(ctx, p.bin, args...)
	run.Env = append(os.Environ(), "LD_LIBRARY_PATH="+p.lib+":"+os.Getenv("LD_LIBRARY_PATH"))
	var buf bytes.Buffer
	run.Stdout, run.Stderr = &buf, &buf
	if err := run.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("распознавание на сервере не удалось: %s", tail(buf.String(), err))
	}
	return strings.Join(ParseASR(buf.String()), " "), nil
}
