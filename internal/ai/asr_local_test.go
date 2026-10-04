package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A fake sherpa-onnx tool: checks its flags and the 16 kHz mono wav it gets,
// prints phrases the way sherpa-onnx-vad-with-offline-asr does (stderr).
const fakeSherpa = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    --whisper-language=ru|--whisper-task=transcribe|--num-threads=*|--silero-vad-model=*|--whisper-encoder=*|--whisper-decoder=*|--tokens=*) ;;
    --*) echo "unknown flag $a" >&2; exit 2 ;;
    *) wav="$a" ;;
  esac
done
[ -f "$wav" ] || { echo "no wav" >&2; exit 3; }
head -c 44 "$wav" | od -An -tx1 | tr -d ' \n' | grep -q '803e0000' || { echo "not 16k" >&2; exit 4; }
echo "Started" >&2
echo "0.320 -- 2.540: Добрый день, начнём разбор." >&2
echo "2.900 -- 6.100: Выручка за месяц выросла." >&2
`

func bz2Tar(t *testing.T, root, out string) {
	t.Helper()
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Skip("bzip2 not installed")
	}
	cmd := exec.Command("tar", "-cjf", out, "-C", filepath.Dir(root), filepath.Base(root))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, b)
	}
}

func TestR34aLocalASRDownloadsAndTranscribes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	src := t.TempDir()
	// sherpa-onnx release archive: bin/ + lib/
	sh := filepath.Join(src, "sherpa-onnx-v1.99.0-linux-x64-static")
	_ = os.MkdirAll(filepath.Join(sh, "bin"), 0o755)
	_ = os.MkdirAll(filepath.Join(sh, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(sh, "bin", "sherpa-onnx-vad-with-offline-asr"), []byte(fakeSherpa), 0o755)
	_ = os.WriteFile(filepath.Join(sh, "bin", "sherpa-onnx-offline"), []byte("#!/bin/sh\n"), 0o755)
	bz2Tar(t, sh, filepath.Join(src, "sherpa.tar.bz2"))
	// the model archive: int8 files are kept, the fp32 ones are not
	md := filepath.Join(src, "sherpa-onnx-whisper-small")
	_ = os.MkdirAll(md, 0o755)
	for _, n := range []string{"small-encoder.int8.onnx", "small-decoder.int8.onnx", "small-tokens.txt", "small-encoder.onnx", "small-decoder.onnx"} {
		_ = os.WriteFile(filepath.Join(md, n), []byte("x"+n), 0o644)
	}
	bz2Tar(t, md, filepath.Join(src, "model.tar.bz2"))

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/repos/k2-fsa/sherpa-onnx/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"assets": []any{
				map[string]any{"name": "sherpa-onnx-v1.99.0-linux-x64-shared.tar.bz2", "browser_download_url": "http://" + r.Host + "/shared"},
				map[string]any{"name": "sherpa-onnx-v1.99.0-linux-x64-static.tar.bz2", "browser_download_url": "http://" + r.Host + "/sherpa.tar.bz2"},
				map[string]any{"name": "sherpa-onnx-v1.99.0-linux-aarch64-static.tar.bz2", "browser_download_url": "http://" + r.Host + "/arm"},
			}})
		case "/sherpa.tar.bz2", "/model.tar.bz2":
			http.ServeFile(w, r, filepath.Join(src, strings.TrimPrefix(r.URL.Path, "/")))
		case "/silero_vad.onnx":
			_, _ = w.Write([]byte("vad"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := (&LocalASR{Dir: dir, ModelURL: srv.URL + "/model.tar.bz2", VADURL: srv.URL + "/silero_vad.onnx", Threads: 2}).defaults()
	// the release lookup goes to the fake GitHub
	a.SherpaURL = ""
	old := sherpaReleasesURL
	sherpaReleasesURL = srv.URL + "/repos/k2-fsa/sherpa-onnx/releases/latest"
	defer func() { sherpaReleasesURL = old }()

	// 3 seconds of a 44.1 kHz stereo tone as an mp3-free wav: ffmpeg makes it 16 kHz mono
	in := filepath.Join(t.TempDir(), "call.wav")
	if b, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=300:duration=3", "-ac", "2", "-ar", "44100", in).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	audio, _ := os.ReadFile(in)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	txt, err := a.Transcribe(ctx, audio, "audio/wav")
	if err != nil {
		t.Fatal(err)
	}
	if txt != "Добрый день, начнём разбор.\nВыручка за месяц выросла." {
		t.Fatalf("text: %q", txt)
	}
	if a.State() != "готов" || !strings.Contains(a.Describe(), "Whisper small") {
		t.Fatal(a.Describe())
	}
	// only the int8 model files were unpacked
	if fileOK(filepath.Join(dir, "whisper-small", "small-encoder.onnx")) || !fileOK(filepath.Join(dir, "whisper-small", "small-encoder.int8.onnx")) {
		t.Fatal("model files")
	}
	// the second recording downloads nothing
	n := hits.Load()
	if _, err := a.Transcribe(ctx, audio, "audio/wav"); err != nil || hits.Load() != n {
		t.Fatalf("second run: %v hits %d → %d", err, n, hits.Load())
	}

	// through the client: local first; when it fails, Whisper API answers
	c := &Client{ASR: a, OpenAI: "o", OpenAISTTModel: "whisper-1", HTTP: srv.Client()}
	if got, err := c.Transcribe(ctx, audio, "audio/wav"); err != nil || !strings.Contains(got, "Выручка") {
		t.Fatalf("client: %q %v", got, err)
	}
	if st := c.Status(); st["speech"] != "local" || !strings.Contains(st["speech_model"].(string), "Whisper") {
		t.Fatalf("status: %+v", st)
	}
	oai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/transcriptions" {
			_, _ = w.Write([]byte("через Whisper API"))
			return
		}
		w.WriteHeader(404)
	}))
	defer oai.Close()
	c.OpenAIBase = oai.URL
	if got, err := c.Transcribe(ctx, []byte("not audio"), "audio/webm"); err != nil || got != "через Whisper API" {
		t.Fatalf("fallback: %q %v", got, err)
	}
}

func TestR34aParseASR(t *testing.T) {
	out := "Started\nsome log line\n0.000 -- 1.200: Привет\n  12.5 -- 14.25 :  Как дела? \n3.000 -- 4.000: \nDone"
	if got := strings.Join(ParseASR(out), "|"); got != "Привет|Как дела?" {
		t.Fatal(got)
	}
	if audioExt("audio/mp4") != ".m4a" || audioExt("audio/webm;codecs=opus") != ".webm" || audioExt("audio/mpeg") != ".mp3" {
		t.Fatal("ext")
	}
}
