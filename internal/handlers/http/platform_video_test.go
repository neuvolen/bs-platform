package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/video"
	"github.com/gin-gonic/gin"
)

func videoStand(t *testing.T, role string) (*PlatformVideo, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	t.Setenv("VIDEO_DIR", dir)
	v := NewPlatformVideo(nil, nil, []byte("secret"))
	if v == nil {
		t.Fatal("no video")
	}
	r := gin.New()
	g := r.Group("/api/v1/platform")
	g.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
	v.Routes(r, g)
	return v, r
}

func doJSON(r http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var rd *bytes.Reader
	switch b := body.(type) {
	case []byte:
		rd = bytes.NewReader(b)
	case nil:
		rd = bytes.NewReader(nil)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, path, rd)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestR54VideoAPI(t *testing.T) {
	v, r := videoStand(t, "admin")
	// status: templates, limits, the CTA
	w, st := doJSON(r, "GET", "/api/v1/platform/video", nil)
	if w.Code != 200 || len(st["templates"].([]any)) != 3 || st["cta"] != video.DefaultCTA {
		t.Fatalf("%d %v", w.Code, st)
	}
	// chunked upload
	data := bytes.Repeat([]byte("x"), 1000)
	_, u := doJSON(r, "POST", "/api/v1/platform/video/uploads", map[string]any{"name": "a.mp4", "kind": "clip", "size": len(data)})
	id, _ := u["id"].(string)
	if !video.ValidID(id) {
		t.Fatalf("%v", u)
	}
	w, out := doJSON(r, "PUT", "/api/v1/platform/video/uploads/"+id+"?off=0", data[:600])
	if w.Code != 200 || out["got"].(float64) != 600 {
		t.Fatalf("%d %v", w.Code, out)
	}
	w, out = doJSON(r, "PUT", "/api/v1/platform/video/uploads/"+id+"?off=900", data[900:])
	if w.Code != 409 || out["got"].(float64) != 600 {
		t.Fatalf("a gap: %d %v", w.Code, out)
	}
	w, out = doJSON(r, "PUT", "/api/v1/platform/video/uploads/"+id+"?off=600", data[600:])
	if w.Code != 200 || out["got"].(float64) != 1000 {
		t.Fatalf("%d %v", w.Code, out)
	}
	w, out = doJSON(r, "POST", "/api/v1/platform/video/uploads", map[string]any{"name": "big.mp4", "kind": "clip", "size": video.MaxUpload + 1})
	if w.Code != 400 || !strings.Contains(out["error"].(string), "500 МБ") {
		t.Fatalf("%d %v", w.Code, out)
	}
	w, out = doJSON(r, "POST", "/api/v1/platform/video/jobs", map[string]any{"clips": []string{id}, "opts": map[string]any{"template": "talk", "hook": strings.Repeat("я", 71)}})
	if w.Code != 400 || !strings.Contains(out["error"].(string), "70 знаков") {
		t.Fatalf("%d %v", w.Code, out)
	}

	// a finished job: its files by the signed link only
	jid := "0123456789abcdef01234567"
	os.MkdirAll(filepath.Dir(v.M.OutPath(jid)), 0o755)
	os.WriteFile(v.M.OutPath(jid), bytes.Repeat([]byte("v"), 5000), 0o644)
	link := v.link("j"+jid, "mp4")
	req := httptest.NewRequest("GET", link, nil)
	req.Header.Set("Range", "bytes=0-99")
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)
	if rw.Code != 206 || rw.Body.Len() != 100 || rw.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("range: %d %d %q", rw.Code, rw.Body.Len(), rw.Header().Get("Content-Type"))
	}
	bad := strings.Replace(link, "-", "-0", 1)
	if w, _ := doJSON(r, "GET", bad, nil); w.Code != 404 {
		t.Fatalf("a wrong signature opens the file: %d", w.Code)
	}
	if w, _ := doJSON(r, "GET", "/api/v1/platform/video/f/j"+jid+"-"+strings.Repeat("0", 24)+".mp4", nil); w.Code != 404 {
		t.Fatal("a made-up signature opens the file")
	}
	rw = httptest.NewRecorder()
	r.ServeHTTP(rw, httptest.NewRequest("GET", link+"?dl=1&name=reels-1", nil))
	if !strings.Contains(rw.Header().Get("Content-Disposition"), `attachment; filename=reels-1.mp4`) {
		t.Fatalf("download name: %q", rw.Header().Get("Content-Disposition"))
	}
}

func TestR54VideoTeamOnly(t *testing.T) {
	_, r := videoStand(t, "resident")
	for _, p := range [][2]string{{"GET", "/api/v1/platform/video"}, {"POST", "/api/v1/platform/video/uploads"}, {"POST", "/api/v1/platform/video/jobs"}} {
		if w, _ := doJSON(r, p[0], p[1], map[string]any{}); w.Code != 403 {
			t.Fatalf("%s %s for a resident: %d", p[0], p[1], w.Code)
		}
	}
}

// TestR54Stand: the real «Видео» server for the browser test (r54/pw_r54.js):
// R54_STAND=127.0.0.1:8797 R54_STAND_SECS=900 go test -run TestR54Stand.
func TestR54Stand(t *testing.T) {
	addr := os.Getenv("R54_STAND")
	if addr == "" {
		t.Skip("R54_STAND=host:port to run")
	}
	gin.SetMode(gin.ReleaseMode)
	v := NewPlatformVideo(nil, ai.LocalASRFromEnv(), []byte("stand-secret"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.Start(ctx)
	r := gin.New()
	g := r.Group("/api/v1/platform")
	g.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Set("userID", "tg:453800951") })
	v.Routes(r, g)
	srv := &http.Server{Addr: addr, Handler: r}
	go srv.ListenAndServe()
	secs, _ := strconv.Atoi(os.Getenv("R54_STAND_SECS"))
	if secs <= 0 {
		secs = 600
	}
	t.Logf("stand on %s, files in %s", addr, v.M.Dir)
	time.Sleep(time.Duration(secs) * time.Second)
	srv.Close()
}
