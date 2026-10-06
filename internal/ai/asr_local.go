package ai

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R34a: speech to text on the server, without any AI key.
//
// Claude does not take audio, and the owner wants no Gemini, so a разбор
// recording is transcribed here: Whisper (OpenAI's open model, MIT) run by
// sherpa-onnx (Apache-2.0) with the Silero VAD (MIT) cutting the speech into
// phrases. Nothing of it is linked into the Go binary and nothing is needed
// at build time, so the Railway build cannot break: on the first recording
// (or at start with ASR_PRELOAD=1) the server downloads into ASR_DIR
//
//   - sherpa-onnx's prebuilt Linux x64 command line tools (GitHub release);
//   - the Whisper model, int8 (asr-models/sherpa-onnx-whisper-<ASR_MODEL>);
//   - silero_vad.onnx;
//   - ffmpeg, a static build, unless the image already has one.
//
// Sizes and speed (CPU only): Whisper small int8 is about 360 MB on disk and
// about 1 GB of memory; on 2 vCPU an hour of a call takes roughly 20-40
// minutes, on 8 vCPU 8-15. The job runs in the background with a heartbeat
// (platform_calls.go), one recording at a time, with a lower priority than
// the web server. ASR_DIR on a Railway volume (e.g. /data) keeps the
// download across deploys; without one it is downloaded again after a deploy.
//
//	ASR_LOCAL=0        off (then OPENAI_API_KEY → Whisper API, else Gemini
//	                   with GEMINI_ENABLED=1, else «Расшифровка недоступна»)
//	ASR_MODEL          tiny | base | small (default) | medium | turbo
//	ASR_DIR            where the tools and the model live
//	ASR_THREADS        CPU threads (default: all but one, at most 8)
//	ASR_PRELOAD=1      download at start, not on the first recording
//	ASR_SHERPA_URL, ASR_MODEL_URL, ASR_VAD_URL, ASR_FFMPEG_URL: other sources
//	ASR_EXTRA_ARGS     extra flags for sherpa-onnx-vad-with-offline-asr

// LocalASR runs Whisper on the server.
type LocalASR struct {
	Dir     string
	Model   string
	Lang    string
	Threads int
	Chunk   time.Duration // the recording is cut into pieces this long

	SherpaURL, ModelURL, VADURL, FFmpegURL string
	ExtraArgs                              []string
	HTTP                                   *http.Client

	run   sync.Mutex // one recording at a time: the CPU is shared with the web server
	prep  sync.Mutex
	state struct {
		sync.Mutex
		s string
	}
}

// sherpaReleasesURL: the latest sherpa-onnx release (tests point it elsewhere).
var sherpaReleasesURL = "https://api.github.com/repos/k2-fsa/sherpa-onnx/releases/latest"

// LocalASRFromEnv: nil when ASR_LOCAL=0.
func LocalASRFromEnv() *LocalASR {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("ASR_LOCAL")))
	if v == "0" || v == "false" || v == "off" || v == "no" {
		return nil
	}
	a := &LocalASR{
		Dir:       strings.TrimSpace(os.Getenv("ASR_DIR")),
		Model:     strings.ToLower(strings.TrimSpace(os.Getenv("ASR_MODEL"))),
		Lang:      strings.TrimSpace(os.Getenv("ASR_LANG")),
		SherpaURL: strings.TrimSpace(os.Getenv("ASR_SHERPA_URL")),
		ModelURL:  strings.TrimSpace(os.Getenv("ASR_MODEL_URL")),
		VADURL:    strings.TrimSpace(os.Getenv("ASR_VAD_URL")),
		FFmpegURL: strings.TrimSpace(os.Getenv("ASR_FFMPEG_URL")),
		ExtraArgs: strings.Fields(os.Getenv("ASR_EXTRA_ARGS")),
	}
	if n, err := strconv.Atoi(os.Getenv("ASR_THREADS")); err == nil && n > 0 {
		a.Threads = n
	}
	return a.defaults()
}

func (a *LocalASR) defaults() *LocalASR {
	if a.Model == "" {
		a.Model = "small"
	}
	if a.Lang == "" {
		a.Lang = "ru"
	}
	if a.Threads <= 0 {
		a.Threads = runtime.NumCPU() - 1
		if a.Threads < 1 {
			a.Threads = 1
		}
		if a.Threads > 8 {
			a.Threads = 8
		}
	}
	if a.Chunk <= 0 {
		a.Chunk = 10 * time.Minute
	}
	if a.Dir == "" {
		a.Dir = filepath.Join(os.TempDir(), "bs-asr")
		if st, err := os.Stat("/data"); err == nil && st.IsDir() && writable("/data") {
			a.Dir = "/data/bs-asr"
		}
	}
	if a.ModelURL == "" {
		a.ModelURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-" + a.Model + ".tar.bz2"
	}
	if a.VADURL == "" {
		a.VADURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"
	}
	if a.FFmpegURL == "" {
		a.FFmpegURL = "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-linux-x64.gz"
	}
	if a.HTTP == nil {
		a.HTTP = &http.Client{} // downloads are bounded by ctx, not by a client timeout
	}
	return a
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".w")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Describe: «Whisper small на сервере», for «Состояние ИИ».
func (a *LocalASR) Describe() string {
	s := "Whisper " + a.Model + " на сервере"
	if st := a.State(); st != "" {
		s += " (" + st + ")"
	}
	return s
}

// State: "" (not used yet), "загружается", "готов" or the last error.
func (a *LocalASR) State() string {
	a.state.Lock()
	defer a.state.Unlock()
	return a.state.s
}

func (a *LocalASR) setState(s string) {
	a.state.Lock()
	a.state.s = s
	a.state.Unlock()
}

type asrPaths struct {
	ffmpeg, bin, lib, encoder, decoder, tokens, vad string
}

// Prepare downloads whatever is missing (once; safe to call at any time).
func (a *LocalASR) Prepare(ctx context.Context) (asrPaths, error) {
	a.prep.Lock()
	defer a.prep.Unlock()
	p, err := a.prepare(ctx)
	if err != nil {
		a.setState("ошибка: " + err.Error())
		return p, err
	}
	a.setState("готов")
	return p, nil
}

func (a *LocalASR) prepare(ctx context.Context) (asrPaths, error) {
	var p asrPaths
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return p, fmt.Errorf("папка распознавания %s: %v", a.Dir, err)
	}
	// ffmpeg: the image's own, else a static build
	f, err := a.ffmpegPath(ctx)
	if err != nil {
		return p, err
	}
	p.ffmpeg = f
	// sherpa-onnx tools
	sh := filepath.Join(a.Dir, "sherpa")
	if p.bin = findVADBin(sh); p.bin == "" {
		a.setState("загружается")
		u := a.SherpaURL
		if u == "" {
			var err error
			if u, err = a.latestSherpa(ctx); err != nil {
				return p, fmt.Errorf("sherpa-onnx: %v (адрес можно задать в ASR_SHERPA_URL)", err)
			}
		}
		tmp := sh + ".part"
		os.RemoveAll(tmp)
		if err := a.fetchTarBz2(ctx, u, tmp, nil); err != nil {
			return p, fmt.Errorf("загрузка sherpa-onnx: %v", err)
		}
		os.RemoveAll(sh)
		if err := os.Rename(tmp, sh); err != nil {
			return p, err
		}
		if p.bin = findVADBin(sh); p.bin == "" {
			return p, errors.New("в архиве sherpa-onnx нет sherpa-onnx-vad-with-offline-asr")
		}
	}
	p.lib = filepath.Join(filepath.Dir(filepath.Dir(p.bin)), "lib")
	// the Whisper model: the int8 encoder, decoder and tokens only
	md := filepath.Join(a.Dir, "whisper-"+a.Model)
	find := func() bool {
		p.encoder, p.decoder, p.tokens = globOne(md, "*encoder.int8.onnx"), globOne(md, "*decoder.int8.onnx"), globOne(md, "*tokens.txt")
		return p.encoder != "" && p.decoder != "" && p.tokens != ""
	}
	if !find() {
		a.setState("загружается")
		tmp := md + ".part"
		os.RemoveAll(tmp)
		keep := func(name string) bool {
			b := filepath.Base(name)
			return strings.HasSuffix(b, "encoder.int8.onnx") || strings.HasSuffix(b, "decoder.int8.onnx") || strings.HasSuffix(b, "tokens.txt")
		}
		if err := a.fetchTarBz2(ctx, a.ModelURL, tmp, keep); err != nil {
			return p, fmt.Errorf("загрузка модели Whisper %s: %v", a.Model, err)
		}
		os.RemoveAll(md)
		if err := os.Rename(tmp, md); err != nil {
			return p, err
		}
		if !find() {
			return p, fmt.Errorf("в архиве модели Whisper %s нет файлов int8", a.Model)
		}
	}
	p.vad = filepath.Join(a.Dir, "silero_vad.onnx")
	if !fileOK(p.vad) {
		a.setState("загружается")
		if err := a.fetchFile(ctx, a.VADURL, p.vad); err != nil {
			return p, fmt.Errorf("загрузка silero_vad: %v", err)
		}
	}
	return p, nil
}

// FFmpeg: the image's ffmpeg, else the static build in ASR_DIR (downloaded
// once). The Reels editor (R54, internal/video) uses the same binary, so the
// download happens once for both.
func (a *LocalASR) FFmpeg(ctx context.Context) (string, error) {
	a.prep.Lock()
	defer a.prep.Unlock()
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return "", fmt.Errorf("папка распознавания %s: %v", a.Dir, err)
	}
	return a.ffmpegPath(ctx)
}

func (a *LocalASR) ffmpegPath(ctx context.Context) (string, error) {
	if f, err := exec.LookPath("ffmpeg"); err == nil {
		return f, nil
	}
	f := filepath.Join(a.Dir, "ffmpeg")
	if !fileOK(f) {
		if err := a.fetchGz(ctx, a.FFmpegURL, f); err != nil {
			return "", fmt.Errorf("загрузка ffmpeg: %v", err)
		}
	}
	return f, nil
}

func fileOK(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func globOne(dir, pat string) string {
	var hit string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && hit == "" {
			if ok, _ := filepath.Match(pat, info.Name()); ok {
				hit = path
			}
		}
		return nil
	})
	return hit
}

func findVADBin(dir string) string {
	var hit string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "sherpa-onnx-vad-with-offline-asr" && info.Mode()&0o111 != 0 {
			hit = path
		}
		return nil
	})
	return hit
}

var sherpaAssetRe = regexp.MustCompile(`^sherpa-onnx-v[\d.]+-linux-x64-(static|shared)(-cpu)?\.tar\.bz2$`)

// latestSherpa: the Linux x64 CPU build of the latest release (static first).
func (a *LocalASR) latestSherpa(ctx context.Context) (string, error) {
	r, _ := http.NewRequestWithContext(ctx, "GET", sherpaReleasesURL, nil)
	r.Header.Set("Accept", "application/vnd.github+json")
	res, err := a.HTTP.Do(r)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("GitHub ответил %d", res.StatusCode)
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rel); err != nil {
		return "", err
	}
	var cs []string
	urls := map[string]string{}
	for _, x := range rel.Assets {
		if sherpaAssetRe.MatchString(x.Name) {
			cs = append(cs, x.Name)
			urls[x.Name] = x.URL
		}
	}
	if len(cs) == 0 {
		return "", errors.New("в последнем выпуске нет сборки linux-x64")
	}
	sort.Slice(cs, func(i, j int) bool {
		si, sj := strings.Contains(cs[i], "-static"), strings.Contains(cs[j], "-static")
		if si != sj {
			return si
		}
		return cs[i] < cs[j]
	})
	return urls[cs[0]], nil
}

func (a *LocalASR) get(ctx context.Context, url string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := a.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("%s: %d", url, res.StatusCode)
	}
	return res, nil
}

func (a *LocalASR) fetchFile(ctx context.Context, url, dst string) error {
	res, err := a.get(ctx, url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return writeAtomic(dst, res.Body, 0o644)
}

func (a *LocalASR) fetchGz(ctx context.Context, url, dst string) error {
	res, err := a.get(ctx, url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		return err
	}
	return writeAtomic(dst, zr, 0o755)
}

func writeAtomic(dst string, r io.Reader, mode os.FileMode) error {
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// fetchTarBz2 unpacks a .tar.bz2 into dst, the archive's top folder cut;
// keep (when set) picks the files to keep.
func (a *LocalASR) fetchTarBz2(ctx context.Context, url, dst string, keep func(string) bool) error {
	res, err := a.get(ctx, url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(bzip2.NewReader(bufio.NewReaderSize(res.Body, 1<<20)))
	type link struct{ name, target string }
	var links []link
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Clean(h.Name))
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		} else if h.Typeflag == tar.TypeDir {
			continue
		}
		if name == "" || name == "." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || filepath.IsAbs(name) {
			continue
		}
		if keep != nil && h.Typeflag != tar.TypeDir && !keep(name) {
			continue
		}
		out := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			if err := writeAtomic(out, tr, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if !strings.Contains(h.Linkname, "..") && !filepath.IsAbs(h.Linkname) {
				links = append(links, link{out, h.Linkname})
			}
		}
	}
	for _, l := range links { // shared builds: libfoo.so -> libfoo.so.1
		_ = os.MkdirAll(filepath.Dir(l.name), 0o755)
		_ = os.Symlink(l.target, l.name)
	}
	return nil
}

var asrLineRe = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*--\s*(\d+(?:\.\d+)?)\s*:\s*(.*\S)\s*$`)

// ParseASR: the phrases sherpa-onnx-vad-with-offline-asr prints
// («12.345 -- 15.678: текст»), in order, one per line.
func ParseASR(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if m := asrLineRe.FindStringSubmatch(l); m != nil {
			if t := strings.TrimSpace(m[3]); t != "" {
				lines = append(lines, t)
			}
		}
	}
	return lines
}

// Transcribe: the recording → 16 kHz mono pieces → Whisper. The text has one
// phrase per line, without speakers (Whisper does not tell them apart; the
// summary prompt works with that).
func (a *LocalASR) Transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	a.run.Lock()
	defer a.run.Unlock()
	p, err := a.Prepare(ctx)
	if err != nil {
		return "", fmt.Errorf("распознавание на сервере не готово: %v", err)
	}
	work, err := os.MkdirTemp("", "bs-asr-job-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	in := filepath.Join(work, "in"+audioExt(mime))
	if err := os.WriteFile(in, audio, 0o600); err != nil {
		return "", err
	}
	seg := filepath.Join(work, "part-%04d.wav")
	cmd := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-i", in,
		"-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "segment", "-segment_time", strconv.Itoa(int(a.Chunk.Seconds())), seg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("запись не читается (ffmpeg): %s", tail(string(out), err))
	}
	parts, _ := filepath.Glob(filepath.Join(work, "part-*.wav"))
	sort.Strings(parts)
	if len(parts) == 0 {
		return "", errors.New("в записи нет звука")
	}
	t0 := time.Now()
	var all []string
	for i, part := range parts {
		args := []string{
			"--silero-vad-model=" + p.vad,
			"--whisper-encoder=" + p.encoder,
			"--whisper-decoder=" + p.decoder,
			"--whisper-language=" + a.Lang,
			"--whisper-task=transcribe",
			"--tokens=" + p.tokens,
			"--num-threads=" + strconv.Itoa(a.Threads),
		}
		args = append(append(args, a.ExtraArgs...), part)
		name := p.bin
		if nice, err := exec.LookPath("nice"); err == nil {
			args = append([]string{"-n", "10", p.bin}, args...)
			name = nice
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+p.lib+":"+os.Getenv("LD_LIBRARY_PATH"))
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("распознавание на сервере не удалось: %s", tail(buf.String(), err))
		}
		all = append(all, ParseASR(buf.String())...)
		log.Printf("asr: piece %d/%d done in %s", i+1, len(parts), time.Since(t0).Round(time.Second))
	}
	return strings.Join(all, "\n"), nil
}

// ASRSegment: one phrase with its time in the recording, in seconds.
type ASRSegment struct {
	Start, End float64
	Text       string
}

// ParseASRTimed: the phrases with their times («12.345 -- 15.678: текст»).
func ParseASRTimed(out string) []ASRSegment {
	var segs []ASRSegment
	for _, l := range strings.Split(out, "\n") {
		if m := asrLineRe.FindStringSubmatch(l); m != nil {
			t := strings.TrimSpace(m[3])
			if t == "" {
				continue
			}
			s, _ := strconv.ParseFloat(m[1], 64)
			e, _ := strconv.ParseFloat(m[2], 64)
			if e < s {
				e = s
			}
			segs = append(segs, ASRSegment{Start: s, End: e, Text: t})
		}
	}
	return segs
}

// TranscribeTimed: a short 16 kHz mono WAV (the Reels editor, R54) → phrases
// with times. Whisper in sherpa-onnx gives no word times, so the caller spreads
// a phrase's words over its time itself.
func (a *LocalASR) TranscribeTimed(ctx context.Context, wav string) ([]ASRSegment, error) {
	a.run.Lock()
	defer a.run.Unlock()
	p, err := a.Prepare(ctx)
	if err != nil {
		return nil, fmt.Errorf("распознавание на сервере не готово: %v", err)
	}
	args := []string{
		"--silero-vad-model=" + p.vad,
		"--whisper-encoder=" + p.encoder,
		"--whisper-decoder=" + p.decoder,
		"--whisper-language=" + a.Lang,
		"--whisper-task=transcribe",
		"--tokens=" + p.tokens,
		"--num-threads=" + strconv.Itoa(a.Threads),
	}
	args = append(append(args, a.ExtraArgs...), wav)
	name := p.bin
	if nice, err := exec.LookPath("nice"); err == nil {
		args = append([]string{"-n", "10", p.bin}, args...)
		name = nice
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+p.lib+":"+os.Getenv("LD_LIBRARY_PATH"))
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("распознавание на сервере не удалось: %s", tail(buf.String(), err))
	}
	return ParseASRTimed(buf.String()), nil
}

func audioExt(mime string) string {
	switch {
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"), strings.Contains(mime, "aac"):
		return ".m4a"
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	}
	return ".webm"
}

func tail(out string, err error) string {
	out = strings.TrimSpace(out)
	if r := []rune(out); len(r) > 300 {
		out = "…" + string(r[len(r)-300:])
	}
	if out == "" {
		return err.Error()
	}
	return err.Error() + ": " + out
}
