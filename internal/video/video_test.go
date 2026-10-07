package video

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFF answers like ffmpeg: probes, silencedetect, and writes the output
// file of every other call. It records the calls and can block or fail.
type fakeFF struct {
	mu      sync.Mutex
	calls   [][]string
	dur     float64 // every clip's duration
	noAudio bool
	block   chan struct{} // the final render waits on it when set
	failOn  string        // an arg that makes the call fail
	running int
	maxRun  int
}

func (f *fakeFF) Run(ctx context.Context, args []string, onTime func(float64)) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.running++
	if f.running > f.maxRun {
		f.maxRun = f.running
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.running--; f.mu.Unlock() }()
	j := strings.Join(args, " ")
	if f.failOn != "" && strings.Contains(j, f.failOn) {
		return "Error: boom", errors.New("exit status 1")
	}
	if len(args) == 2 && args[0] == "-i" { // probe
		a := "  Stream #0:1(und): Audio: aac (LC), 48000 Hz, stereo\n"
		if f.noAudio {
			a = ""
		}
		return "Input #0, mov\n  Duration: 00:00:" + fmt.Sprintf("%05.2f", f.dur) + ", start: 0\n" +
			"  Stream #0:0(und): Video: h264 (High), yuv420p, 1920x1080, 30 fps\n" + a, errors.New("exit status 1")
	}
	if strings.Contains(j, "silencedetect") {
		return "[silencedetect] silence_start: 2\n[silencedetect] silence_end: 3.5 | silence_duration: 1.5\n", nil
	}
	if strings.Contains(j, "-filters") || strings.Contains(j, "-encoders") {
		return " ... ass V->V\n ... loudnorm A->A\n ..C sidechaincompress AA->A\n V..... libx264 H.264\n", nil
	}
	if strings.Contains(j, "out.mp4 ") || strings.HasSuffix(j, "out.mp4") {
		if f.block != nil {
			select {
			case <-f.block:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	if onTime != nil {
		onTime(0.5)
		onTime(1)
	}
	out := args[len(args)-1]
	if out != "-" {
		if err := os.WriteFile(out, []byte("data"), 0o644); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (f *fakeFF) find(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			return c
		}
	}
	return nil
}

func fakeASR(ctx context.Context, wav string) ([]Seg, error) {
	return []Seg{{0.2, 1.8, "Почему прибыль не растёт?"}, {2.1, 4.0, "Потому что бизнес держится на одном человеке."}}, nil
}

func TestR54ParseProbe(t *testing.T) {
	m, err := ParseProbe("  Duration: 00:01:02.50, start: 0.000000\n  Stream #0:0[0x1](und): Video: h264 (High) (avc1), yuv420p(tv), 1920x1080 [SAR 1:1 DAR 16:9], 30 fps\n    Side data:\n      displaymatrix: rotation of -90.00 degrees\n  Stream #0:1: Audio: aac\n")
	if err != nil || m.Dur != 62.5 || m.W != 1080 || m.H != 1920 || !m.HasAudio || !m.HasVideo || m.FPS != 30 {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ParseProbe("garbage"); err == nil {
		t.Fatal("garbage parsed")
	}
}

func TestR54KeepSpans(t *testing.T) {
	sil := ParseSilences("silence_start: 0\nsilence_end: 1.0\nsilence_start: 3\nsilence_end: 3.1\nsilence_start: 5\nsilence_end: 6.5\nsilence_start: 9.5\n", 10)
	if len(sil) != 4 || sil[3] != (Span{9.5, 10}) {
		t.Fatalf("%v", sil)
	}
	k := KeepSpans(sil, 10, 0.08)
	// the 0.1 s silence stays; the end piece 9.92..10 is too short
	want := []Span{{0, 0.08}, {0.92, 5.08}, {6.42, 9.58}}
	if len(k) != 2 || k[0].A != want[1].A || k[1].B != want[2].B {
		t.Fatalf("%v", k)
	}
}

func TestR54CaptionPages(t *testing.T) {
	ws := Words([]Seg{{0, 3, "Финансы, продажи, команда и стратегия."}, {4, 5, "Итог 10 дней!"}})
	if len(ws) != 8 || ws[0].Start != 0 || ws[4].End < 2.99 || ws[4].End > 3.01 {
		t.Fatalf("%+v", ws)
	}
	ps := Pages(ws)
	for _, p := range ps {
		n := 0
		for _, w := range p.Words {
			n += len([]rune(cleanWord(w.Text))) + 1
		}
		if len(p.Words) > pageWords || n-1 > pageRunes {
			t.Fatalf("page too long: %+v", p)
		}
	}
	if ps[len(ps)-1].Words[0].Text != "Итог" {
		t.Fatalf("the pause must start a page: %+v", ps)
	}
	ass := ASS(DefaultStyle, ps, "Почему прибыль не растёт?")
	for _, s := range []string{"PlayResX: 1080", "Style: Cap,Manrope ExtraBold,112", ",Hook,", "\\bord12}Финансы{\\r}", "Dialogue: 2,0:00:00.00,0:00:01.50"} {
		if !strings.Contains(ass, s) {
			t.Fatalf("no %q in\n%s", s, ass)
		}
	}
	if strings.Contains(ass, "Финансы,") || strings.Contains(ass, "\u2014") {
		t.Fatal("commas stay off the captions, no long dash")
	}
	if em := Emphasis(ps); len(em) == 0 {
		t.Fatal("the page with 10 is an emphasis")
	}
	if got := Wrap("Ссылка в шапке профиля: 99 чек-листов", 18); len(got) != 3 {
		t.Fatalf("%q", got)
	}
}

func newTestManager(t *testing.T, ff *fakeFF) *Manager {
	t.Helper()
	dir, _ := os.MkdirTemp("", "r54-")
	t.Cleanup(func() { time.Sleep(50 * time.Millisecond); os.RemoveAll(dir) }) // the worker may still write the index
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Tools = func(context.Context) (Tools, error) {
		return Tools{FF: ff, ASR: fakeASR, Font: []byte("ttf"), Logo: []byte("png")}, nil
	}
	return m
}

func upload(t *testing.T, m *Manager, kind string, data []byte) string {
	t.Helper()
	u, err := m.NewUpload("clip.mp4", kind, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	half := len(data) / 2
	if _, err := m.Chunk(u.ID, 0, bytes.NewReader(data[:half])); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Chunk(u.ID, int64(len(data)), bytes.NewReader(data[half:])); err != ErrGap {
		t.Fatalf("a gap must be refused, got %v", err)
	}
	if _, err := m.Chunk(u.ID, 0, bytes.NewReader(data[:half])); err != nil { // a retried chunk
		t.Fatal(err)
	}
	got, err := m.Chunk(u.ID, int64(half), bytes.NewReader(data[half:]))
	if err != nil || got.Got != int64(len(data)) {
		t.Fatalf("%+v %v", got, err)
	}
	b, _ := os.ReadFile(m.upPath(u.ID))
	if !bytes.Equal(b, data) {
		t.Fatal("file differs")
	}
	return u.ID
}

func waitJob(t *testing.T, m *Manager, id string, st string) *Job {
	t.Helper()
	for i := 0; i < 400; i++ {
		if j := m.Job(id); j != nil && j.Status == st {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never %s: %+v", id, st, m.Job(id))
	return nil
}

func TestR54JobFlow(t *testing.T) {
	ff := &fakeFF{dur: 12.5}
	m := newTestManager(t, ff)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	c1 := upload(t, m, "clip", []byte("first clip bytes"))
	c2 := upload(t, m, "clip", []byte("second"))
	mu := upload(t, m, "music", []byte("track"))
	if _, err := m.Submit([]string{c1, c2}, mu, Options{Template: "nope"}, "Рустам"); err == nil {
		t.Fatal("unknown template accepted")
	}
	j, err := m.Submit([]string{c1, c2}, mu, Options{Template: "talk", Hook: "Почему прибыль не растёт?"}, "Рустам")
	if err != nil {
		t.Fatal(err)
	}
	d := waitJob(t, m, j.ID, "done")
	if d.Pct != 100 || d.Words != 11 || d.Cut < 1.3 || !fileOK(m.OutPath(j.ID)) || !fileOK(m.CoverPath(j.ID)) {
		t.Fatalf("%+v", d)
	}
	// the stages ffmpeg went through
	if c := ff.find("silencedetect=noise=-35dB:d=0.5"); c == nil {
		t.Fatal("no silence detection")
	}
	if c := ff.find("aselect='between(t,0.000,2.080)"); c == nil {
		t.Fatal("pauses not cut")
	}
	fin := ff.find("+faststart")
	js := strings.Join(fin, " ")
	for _, s := range []string{"loudnorm=I=-14:TP=-1", "sidechaincompress", "-stream_loop -1", "ass=", "concat=n=2:v=1:a=1", "libx264", "aac", "overlay=enable="} {
		if !strings.Contains(js, s) {
			t.Fatalf("final render has no %q: %s", s, js)
		}
	}
	// the temp folder is gone, the index survives a restart
	if _, err := os.Stat(filepath.Join(m.Dir, "work", j.ID)); !os.IsNotExist(err) {
		t.Fatal("work dir left")
	}
	m2, _ := NewManager(m.Dir)
	if g := m2.Job(j.ID); g == nil || g.Status != "done" {
		t.Fatalf("index lost: %+v", g)
	}
}

func TestR54OneAtATimeAndTimeout(t *testing.T) {
	ff := &fakeFF{dur: 5, block: make(chan struct{})}
	m := newTestManager(t, ff)
	m.Timeout = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	c := upload(t, m, "clip", []byte("clip"))
	a, _ := m.Submit([]string{c}, "", Options{Template: "clean"}, "")
	b, _ := m.Submit([]string{c}, "", Options{Template: "clean"}, "")
	waitJob(t, m, a.ID, "running")
	time.Sleep(50 * time.Millisecond)
	if j := m.Job(b.ID); j.Status != "queued" {
		t.Fatalf("second job must wait: %+v", j)
	}
	fa := waitJob(t, m, a.ID, "failed")
	if !strings.Contains(fa.Error, "дольше") {
		t.Fatalf("timeout message: %q", fa.Error)
	}
	waitJob(t, m, b.ID, "failed")
	if m.maxBusy != 1 || ff.maxRun != 1 {
		t.Fatalf("ran in parallel: %d %d", m.maxBusy, ff.maxRun)
	}
	// a retry works and replaces the failed job
	close(ff.block)
	m.Timeout = time.Minute
	r, err := m.Retry(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, r.ID, "done")
	if m.Job(a.ID) != nil {
		t.Fatal("the failed job stays after the retry")
	}
}

func TestR54Limits(t *testing.T) {
	ff := &fakeFF{dur: 59.9}
	m := newTestManager(t, ff)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	if _, err := m.NewUpload("x.mp4", "clip", MaxUpload+1); err == nil {
		t.Fatal("over 500 MB accepted")
	}
	c := upload(t, m, "clip", []byte("clip"))
	j, _ := m.Submit([]string{c, c, c, c}, "", Options{Template: "talk"}, "")
	f := waitJob(t, m, j.ID, "failed")
	if !strings.Contains(f.Error, "можно до 180 с)") {
		t.Fatalf("%q", f.Error)
	}
	// a failing ffmpeg: a clear stage in the error
	ff2 := &fakeFF{dur: 5, failOn: "outro.ass"}
	m2 := newTestManager(t, ff2)
	m2.Start(ctx)
	c2 := upload(t, m2, "clip", []byte("clip"))
	j2, _ := m2.Submit([]string{c2}, "", Options{Template: "talk"}, "")
	f2 := waitJob(t, m2, j2.ID, "failed")
	if !strings.Contains(f2.Error, "концовка не собралась") {
		t.Fatalf("%q", f2.Error)
	}
	// no audio in a clip: a silent track is added, no captions crash
	ff3 := &fakeFF{dur: 5, noAudio: true}
	m3 := newTestManager(t, ff3)
	m3.Start(ctx)
	c3 := upload(t, m3, "clip", []byte("clip"))
	j3, _ := m3.Submit([]string{c3}, "", Options{Template: "broll"}, "")
	waitJob(t, m3, j3.ID, "done")
	if ff3.find("anullsrc=r=48000:cl=stereo") == nil {
		t.Fatal("no silent track for a clip without sound")
	}
	if ff3.find("boxblur") == nil {
		t.Fatal("broll must fit the frame on a blurred copy")
	}
	// old uploads go
	m3.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	m3.Cleanup()
	if m3.Upload(c3) != nil {
		t.Fatal("a two-day-old upload stays")
	}
}

func TestR54Check(t *testing.T) {
	f, err := Check(context.Background(), &fakeFF{})
	if err != nil || len(f.Missing()) != 0 {
		t.Fatalf("%+v %v", f, err)
	}
}

// R55: the folder lives on the 5 GB volume: past MaxBytes the oldest
// finished Reels go first, a job still waiting keeps its clips.
func TestR55CapSizeOldestFirst(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	m.Now = func() time.Time { return now }
	mk := func(id string, age time.Duration, size int) {
		os.MkdirAll(m.jobDir(id), 0o755)
		os.WriteFile(m.OutPath(id), make([]byte, size), 0o644)
		m.jobs[id] = &Job{ID: id, Status: "done", Created: now.Add(-age)}
	}
	mk("aaaaaaaaaaaaaaaa", 3*time.Hour, 400)
	mk("bbbbbbbbbbbbbbbb", 2*time.Hour, 400)
	mk("cccccccccccccccc", time.Hour, 400)
	// an upload a queued job waits for: never removed
	os.WriteFile(m.upPath("dddddddddddddddd"), make([]byte, 300), 0o644)
	m.uploads["dddddddddddddddd"] = &Upload{ID: "dddddddddddddddd", Kind: "clip", Size: 300, Got: 300, Created: now.Add(-5 * time.Hour)}
	m.jobs["eeeeeeeeeeeeeeee"] = &Job{ID: "eeeeeeeeeeeeeeee", Status: "queued", Clips: []string{"dddddddddddddddd"}, Created: now}
	m.MaxBytes = 1000
	m.Cleanup()
	if m.jobs["aaaaaaaaaaaaaaaa"] != nil || m.jobs["bbbbbbbbbbbbbbbb"] != nil {
		t.Fatal("the oldest finished jobs should go")
	}
	if m.jobs["cccccccccccccccc"] == nil || m.uploads["dddddddddddddddd"] == nil {
		t.Fatal("the newest job and the waited-for upload stay")
	}
	if u := m.Used(); u > 1000 {
		t.Fatalf("used %d", u)
	}
}
