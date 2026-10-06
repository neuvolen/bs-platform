// Package video is the Reels editor of Маркетинг → SMM → «Видео» (R54).
//
// The owner uploads one or more clips (talking head, b-roll), picks a
// template, and the server cuts a vertical Reels with ffmpeg alone: 9:16
// crop, silences cut out, Russian captions from the local Whisper burned in
// word by word, a hook card in the first 1.5 s, punch-ins, the brand outro with
// the CTA, optional music ducked under the voice, loudness -14 LUFS, H.264/AAC
// and a cover PNG. One job at a time, in the background, with progress.
//
// The short-form numbers (Reels safe zones, 2-4 words per caption page,
// silence threshold -35 dB / 0.5 s with 80 ms padding, -14 LUFS, music
// about 10 dB under the voice) follow the craft notes of OpenMontage
// (github.com/calesthio/OpenMontage, AGPL-3.0): only these facts are used,
// none of its code. OpenMontage itself is an agent toolkit for a local machine
// (Python, Remotion, paid generation APIs) and is not run on the server.
package video

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Runner runs ffmpeg. onTime (may be nil) gets the output time reached, in
// seconds, from -progress. Tests replace it with a fake.
type Runner interface {
	Run(ctx context.Context, args []string, onTime func(sec float64)) (stderr string, err error)
}

// Exec is the real ffmpeg.
type Exec struct{ Bin string }

var outTimeRe = regexp.MustCompile(`^out_time_(?:us|ms)=(\d+)`)

func (e Exec) Run(ctx context.Context, args []string, onTime func(sec float64)) (string, error) {
	full := append([]string{"-hide_banner", "-nostdin", "-y"}, args...)
	if onTime != nil {
		full = append([]string{"-progress", "pipe:1", "-nostats"}, full...)
	}
	cmd := exec.CommandContext(ctx, e.Bin, full...)
	var errBuf tailBuf
	cmd.Stderr = &errBuf
	if onTime == nil {
		cmd.Stdout = &errBuf // -filters, -encoders print to stdout
	}
	var wg sync.WaitGroup
	if onTime != nil {
		out, err := cmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				if m := outTimeRe.FindStringSubmatch(sc.Text()); m != nil {
					if us, err := strconv.ParseInt(m[1], 10, 64); err == nil {
						onTime(float64(us) / 1e6)
					}
				}
			}
			_, _ = io.Copy(io.Discard, out)
		}()
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	wg.Wait()
	err := cmd.Wait()
	if ctx.Err() != nil {
		return errBuf.String(), ctx.Err()
	}
	if err != nil {
		err = fmt.Errorf("%v: %s", err, lastLine(errBuf.String()))
	}
	return errBuf.String(), err
}

// tailBuf keeps the last 64 KB of stderr (probe output and errors).
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 64<<10 {
		t.b = t.b[len(t.b)-64<<10:]
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuf) String() string { t.mu.Lock(); defer t.mu.Unlock(); return string(t.b) }

// Media: what ffmpeg says about an input file.
type Media struct {
	Dur      float64
	W, H     int // as shown, rotation applied
	FPS      float64
	HasVideo bool
	HasAudio bool
}

var (
	durRe    = regexp.MustCompile(`Duration:\s*(\d+):(\d\d):(\d\d(?:\.\d+)?)`)
	vidRe    = regexp.MustCompile(`Stream #\S+.*?: Video: .*?(\d{2,5})x(\d{2,5})`)
	fpsRe    = regexp.MustCompile(`(\d+(?:\.\d+)?) fps`)
	rotRe    = regexp.MustCompile(`(?:rotate\s*:\s*|rotation of )(-?\d+(?:\.\d+)?)`)
	audRe    = regexp.MustCompile(`Stream #\S+.*?: Audio: `)
	silStRe  = regexp.MustCompile(`silence_start:\s*(-?\d+(?:\.\d+)?)`)
	silEndRe = regexp.MustCompile(`silence_end:\s*(-?\d+(?:\.\d+)?)`)
)

// ParseProbe reads `ffmpeg -i file` output.
func ParseProbe(out string) (Media, error) {
	var m Media
	d := durRe.FindStringSubmatch(out)
	if d == nil {
		return m, errors.New("файл не читается как видео")
	}
	h, _ := strconv.Atoi(d[1])
	mi, _ := strconv.Atoi(d[2])
	s, _ := strconv.ParseFloat(d[3], 64)
	m.Dur = float64(h*3600+mi*60) + s
	for _, l := range strings.Split(out, "\n") {
		if !m.HasVideo {
			if v := vidRe.FindStringSubmatch(l); v != nil && !strings.Contains(l, "attached pic") {
				m.HasVideo = true
				m.W, _ = strconv.Atoi(v[1])
				m.H, _ = strconv.Atoi(v[2])
				if f := fpsRe.FindStringSubmatch(l); f != nil {
					m.FPS, _ = strconv.ParseFloat(f[1], 64)
				}
			}
		}
		if audRe.MatchString(l) {
			m.HasAudio = true
		}
	}
	if r := rotRe.FindStringSubmatch(out); r != nil && m.HasVideo {
		if a, _ := strconv.ParseFloat(r[1], 64); int(a)%180 != 0 {
			m.W, m.H = m.H, m.W
		}
	}
	return m, nil
}

// Probe runs ffmpeg -i (it "fails" without an output; the text is what counts).
func Probe(ctx context.Context, r Runner, path string) (Media, error) {
	out, err := r.Run(ctx, []string{"-i", path}, nil)
	if ctx.Err() != nil {
		return Media{}, ctx.Err()
	}
	m, perr := ParseProbe(out)
	if perr != nil && err != nil {
		return m, fmt.Errorf("%v: %s", perr, lastLine(out))
	}
	return m, perr
}

// Span: a piece of time, in seconds.
type Span struct{ A, B float64 }

// ParseSilences reads silencedetect output; an open silence runs to total.
func ParseSilences(out string, total float64) []Span {
	var sp []Span
	start := -1.0
	for _, l := range strings.Split(out, "\n") {
		if m := silStRe.FindStringSubmatch(l); m != nil {
			start, _ = strconv.ParseFloat(m[1], 64)
			if start < 0 {
				start = 0
			}
		}
		if m := silEndRe.FindStringSubmatch(l); m != nil && start >= 0 {
			e, _ := strconv.ParseFloat(m[1], 64)
			sp = append(sp, Span{start, e})
			start = -1
		}
	}
	if start >= 0 && total > start {
		sp = append(sp, Span{start, total})
	}
	return sp
}

// KeepSpans: the speech left after cutting the silences, each silence kept
// for pad seconds at both ends so words are not clipped. Pieces shorter than
// 0.15 s are dropped.
func KeepSpans(sil []Span, total, pad float64) []Span {
	var keep []Span
	cur := 0.0
	for _, s := range sil {
		a, b := s.A+pad, s.B-pad
		if b-a < 0.2 { // too short to cut
			continue
		}
		if a > cur {
			keep = append(keep, Span{cur, a})
		}
		cur = b
	}
	if total > cur {
		keep = append(keep, Span{cur, total})
	}
	out := keep[:0]
	for _, k := range keep {
		if k.B-k.A >= 0.15 {
			out = append(out, k)
		}
	}
	return out
}

func spansDur(sp []Span) float64 {
	d := 0.0
	for _, s := range sp {
		d += s.B - s.A
	}
	return d
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if r := []rune(s); len(r) > 240 {
		s = string(r[:240])
	}
	return s
}

// Features: what this ffmpeg build can do (from -filters and -encoders).
type Features struct {
	ASS, X264, Loudnorm, Sidechain bool
}

func (f Features) Missing() []string {
	var m []string
	if !f.ASS {
		m = append(m, "ass (libass)")
	}
	if !f.X264 {
		m = append(m, "libx264")
	}
	if !f.Loudnorm {
		m = append(m, "loudnorm")
	}
	if !f.Sidechain {
		m = append(m, "sidechaincompress")
	}
	return m
}

// Check asks ffmpeg for its filters and encoders.
func Check(ctx context.Context, r Runner) (Features, error) {
	var f Features
	out, err := r.Run(ctx, []string{"-filters"}, nil)
	if err != nil && !strings.Contains(out, "loudnorm") {
		return f, fmt.Errorf("ffmpeg не запускается: %v %s", err, lastLine(out))
	}
	enc, _ := r.Run(ctx, []string{"-encoders"}, nil)
	has := func(s, name string) bool { return regexp.MustCompile(`(?m)^\s*\S+\s+` + name + `\s`).MatchString(s) }
	f.ASS = has(out, "ass")
	f.Loudnorm = has(out, "loudnorm")
	f.Sidechain = has(out, "sidechaincompress")
	f.X264 = has(enc, "libx264")
	return f, nil
}
