package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// R62: «Шаг 7 в озвучке очень тихо почему-то».
//
// ElevenLabs gives every phrase at its own level (one take is −18 LUFS, the
// next −27), and the files were kept as they came: one quiet step among loud
// ones. Now every premium file (the tour and the login demo) is brought to
// one loudness with ffmpeg's two-pass loudnorm (EBU R128): −16 LUFS
// integrated, true peak −1.5 dBTP, linear gain where it fits. The normalized
// copy is kept next to the original under its own key (normKey), so its URL
// is new and the immutable caches of browsers fetch it; the original stays
// for a later re-normalization. Files made before this are normalized at the
// server's start (normalizeKept), each phrase's level before and after goes
// to the log.

const (
	normVer    = "ln1" // the loudness version: a new target is a new key
	normTarget = -16.0 // LUFS
	normPeak   = -1.5  // dBTP
	normLRA    = 11.0
)

// normKey: the key of the normalized copy of a kept file.
func normKey(raw string) string {
	s := sha256.Sum256([]byte(normVer + "\x00" + raw))
	return "el_" + hex.EncodeToString(s[:])[:48]
}

// Loudness: what loudnorm measured (input_*) or made (output_*).
type Loudness struct {
	I, TP, LRA, Thresh, Offset float64
}

func (l Loudness) String() string {
	return fmt.Sprintf("%.1f LUFS, peak %.1f dBTP", l.I, l.TP)
}

var loudJSONRe = regexp.MustCompile(`(?s)\{[^{}]*"input_i"[^{}]*\}`)

// parseLoudnorm reads the JSON loudnorm prints at the end of ffmpeg's stderr.
func parseLoudnorm(stderr []byte) (in, out Loudness, err error) {
	ms := loudJSONRe.FindAll(stderr, -1)
	if len(ms) == 0 {
		return in, out, errors.New("loudnorm: no measurement")
	}
	var m map[string]string
	if err := json.Unmarshal(ms[len(ms)-1], &m); err != nil {
		return in, out, fmt.Errorf("loudnorm: %v", err)
	}
	f := func(k string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(m[k]), 64)
		if err != nil {
			return math.Inf(-1)
		}
		return v
	}
	in = Loudness{I: f("input_i"), TP: f("input_tp"), LRA: f("input_lra"), Thresh: f("input_thresh"), Offset: f("target_offset")}
	out = Loudness{I: f("output_i"), TP: f("output_tp"), LRA: f("output_lra"), Thresh: f("output_thresh")}
	if math.IsInf(in.I, 0) || math.IsInf(in.TP, 0) || math.IsInf(in.Thresh, 0) {
		return in, out, errors.New("loudnorm: silence or no audio")
	}
	if math.IsInf(in.LRA, 0) {
		in.LRA = 0
	}
	if math.IsInf(in.Offset, 0) {
		in.Offset = 0
	}
	return in, out, nil
}

func runFF(ctx context.Context, ff string, in []byte, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, ff, args...)
	cmd.Stdin = bytes.NewReader(in)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.Bytes(), e.Bytes(), err
}

func lastLines(b []byte, n int) string {
	ls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " | ")
}

// MeasureLoudness: the integrated loudness and true peak of an audio file.
func MeasureLoudness(ctx context.Context, ff string, audio []byte) (Loudness, error) {
	_, e, err := runFF(ctx, ff, audio, "-hide_banner", "-nostats", "-i", "pipe:0",
		"-af", fmt.Sprintf("loudnorm=I=%g:TP=%g:LRA=%g:print_format=json", normTarget, normPeak, normLRA), "-f", "null", "-")
	if err != nil {
		return Loudness{}, fmt.Errorf("ffmpeg: %v: %s", err, lastLines(e, 2))
	}
	in, _, perr := parseLoudnorm(e)
	return in, perr
}

// NormalizeMP3: the file at −16 LUFS / −1.5 dBTP (two passes), MP3 44.1 kHz
// mono 128 kbit/s like ElevenLabs gives it. before is the measured level,
// after the level made.
func NormalizeMP3(ctx context.Context, ff string, audio []byte) (out []byte, before, after Loudness, err error) {
	before, err = MeasureLoudness(ctx, ff, audio)
	if err != nil {
		return nil, before, after, err
	}
	af := fmt.Sprintf("loudnorm=I=%g:TP=%g:LRA=%g:measured_I=%.2f:measured_TP=%.2f:measured_LRA=%.2f:measured_thresh=%.2f:offset=%.2f:linear=true:print_format=json",
		normTarget, normPeak, normLRA, before.I, before.TP, before.LRA, before.Thresh, before.Offset)
	o, e, err := runFF(ctx, ff, audio, "-hide_banner", "-nostats", "-i", "pipe:0", "-af", af,
		"-ar", "44100", "-ac", "1", "-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "pipe:1")
	if err != nil {
		return nil, before, after, fmt.Errorf("ffmpeg: %v: %s", err, lastLines(e, 2))
	}
	if len(o) < 512 {
		return nil, before, after, errors.New("ffmpeg: empty output")
	}
	if _, a, perr := parseLoudnorm(e); perr == nil {
		after = a
	}
	return o, before, after, nil
}

// SetFFmpeg: where the ffmpeg binary comes from (the video's: the system one
// or the static download). Without it the files stay as ElevenLabs gave them.
func (p *PremiumVoice) SetFFmpeg(f func(ctx context.Context) (string, error)) {
	p.mu.Lock()
	p.ffmpeg = f
	p.mu.Unlock()
	p.Kick()
}

func (p *PremiumVoice) ffBin(ctx context.Context) string {
	p.mu.Lock()
	f := p.ffmpeg
	p.mu.Unlock()
	if f == nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	bin, err := f(cctx)
	if err != nil {
		p.mu.Lock()
		said := p.ffErr == err.Error()
		p.ffErr = err.Error()
		p.mu.Unlock()
		if !said {
			log.Printf("tts premium: loudness: no ffmpeg (%v): the files play as ElevenLabs gave them", err)
		}
		return ""
	}
	return bin
}

// normalize keeps the normalized copy of a kept file (raw key, its sound).
// label names the phrase in the log («login step 7», «tour»).
func (p *PremiumVoice) normalize(ctx context.Context, ff, raw, voice, style, text, label string, audio []byte) bool {
	nctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, before, after, err := NormalizeMP3(nctx, ff, audio)
	if err != nil {
		log.Printf("tts premium: loudness: %s %q: %v (the original plays)", label, short(text), err)
		return false
	}
	if err := p.repo.PutTTSMime(context.WithoutCancel(ctx), normKey(raw), voice, style+"n", text, "audio/mpeg", out); err != nil {
		log.Printf("tts premium: loudness: %s: not saved: %v", label, err)
		return false
	}
	log.Printf("tts premium: loudness: %s %q: %s → %s", label, short(text), before, after)
	return true
}

func short(t string) string {
	r := []rune(t)
	if len(r) > 48 {
		return string(r[:48]) + "…"
	}
	return t
}

// normalizeKept: the kept files of the playing voice (the tour and the login
// demo) without a normalized copy get one; the pages then play the copies.
func (p *PremiumVoice) normalizeKept(ctx context.Context) {
	if p.repo == nil {
		return
	}
	v := p.config().Active
	if v.ID == "" {
		return
	}
	type item struct{ raw, style, text, label string }
	var items []item
	for _, t := range p.texts() {
		items = append(items, item{premiumKey(v, spokenKey(t)), premiumStyle, t, "tour"})
	}
	lv := p.loginVoice()
	for i, t := range p.loginLines() {
		items = append(items, item{premiumKey(lv, spokenKey(t)), loginStyle, t, "login step " + strconv.Itoa(i+1)})
	}
	keys := make([]string, 0, 2*len(items))
	for _, it := range items {
		keys = append(keys, it.raw, normKey(it.raw))
	}
	got, err := p.repo.TTSHave(ctx, keys)
	if err != nil {
		return
	}
	var todo []item
	for _, it := range items {
		if got[it.raw] && !got[normKey(it.raw)] {
			todo = append(todo, it)
		}
	}
	if len(todo) == 0 {
		return
	}
	ff := p.ffBin(ctx)
	if ff == "" {
		return
	}
	done := 0
	for _, it := range todo {
		if ctx.Err() != nil {
			return
		}
		audio, _, err := p.repo.GetTTSMime(ctx, it.raw)
		if err != nil || audio == nil {
			continue
		}
		if p.normalize(ctx, ff, it.raw, "elevenlabs:"+v.ID, it.style, it.text, it.label, audio) {
			done++
		}
	}
	log.Printf("tts premium: loudness: %d of %d kept files brought to %g LUFS / %g dBTP", done, len(todo), normTarget, normPeak)
	if done > 0 {
		p.refreshOverlay(ctx)
		p.refreshLoginOverlay(ctx)
	}
}
