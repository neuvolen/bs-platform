package video

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
)

// TestR54RealRender: a real render with the machine's ffmpeg and the local
// Whisper. Off by default; R54_REAL=1 R54_CLIPS=a.mp4,b.mp4 [R54_MUSIC=m.mp3]
// R54_OUT=dir runs it (ASR_DIR / ASR_MODEL as on the server).
func TestR54RealRender(t *testing.T) {
	if os.Getenv("R54_REAL") != "1" {
		t.Skip("R54_REAL=1 to run")
	}
	bin, err := exec.LookPath("ffmpeg")
	if b := os.Getenv("R54_FFMPEG"); b != "" {
		bin, err = b, nil
	}
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ff := Exec{Bin: bin}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	f, err := Check(ctx, ff)
	if err != nil || len(f.Missing()) > 0 {
		t.Fatalf("ffmpeg features: %+v %v", f, err)
	}
	tools := Tools{FF: ff, Font: tplpdf.FontHeavy(), Logo: tplpdf.LogoWhite()}
	cache := os.Getenv("R54_SEGS") // keeps the recognised phrases between runs
	if a := ai.LocalASRFromEnv(); a != nil {
		tools.ASR = func(ctx context.Context, wav string) ([]Seg, error) {
			var out []Seg
			if b, err := os.ReadFile(cache); err == nil && cache != "" && json.Unmarshal(b, &out) == nil {
				return out, nil
			}
			ss, err := a.TranscribeTimed(ctx, wav)
			for _, s := range ss {
				out = append(out, Seg{s.Start, s.End, s.Text})
			}
			if b, _ := json.Marshal(out); err == nil && cache != "" {
				os.WriteFile(cache, b, 0o644)
			}
			return out, err
		}
	}
	out := os.Getenv("R54_OUT")
	work := filepath.Join(out, "work")
	os.RemoveAll(work)
	os.MkdirAll(work, 0o755)
	tpl := os.Getenv("R54_TPL")
	if tpl == "" {
		tpl = "talk"
	}
	in := Input{Clips: strings.Split(os.Getenv("R54_CLIPS"), ","), Music: os.Getenv("R54_MUSIC"), MaxInput: 180,
		Opts: Options{Template: tpl, Hook: "Почему прибыль не растёт?"}}
	t0 := time.Now()
	last := ""
	res, err := Render(ctx, in, tools, work, func(s string, d float64) {
		if s != last {
			t.Logf("%5.1fs %3.0f%% %s", time.Since(t0).Seconds(), d*100, s)
			last = s
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("done in %s: %+v", time.Since(t0).Round(time.Second), *res)
	b, _ := os.ReadFile(filepath.Join(work, "captions.ass"))
	os.WriteFile(filepath.Join(out, "captions.ass"), b, 0o644)
	for _, p := range [][2]string{{res.Out, "out.mp4"}, {res.Cover, "cover.png"}} {
		if err := os.Rename(p[0], filepath.Join(out, p[1])); err != nil {
			t.Fatal(err)
		}
	}
}
