package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	funnelvideo "github.com/bnursik/business_surgery_backend/web/funnel_video"
	"github.com/gin-gonic/gin"
)

type fvFakeFiles struct {
	mu sync.Mutex
	m  map[string]pg.PlatformFile
}

func (f *fvFakeFiles) PutFile(ctx context.Context, x pg.PlatformFile, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]pg.PlatformFile{}
	}
	x.Size = int64(len(x.Data))
	f.m[x.ID] = x
	return nil
}
func (f *fvFakeFiles) GetFile(ctx context.Context, id string) (*pg.PlatformFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x, ok := f.m[id]; ok {
		return &x, nil
	}
	return nil, nil
}
func (f *fvFakeFiles) DeleteFile(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

type fvCall struct {
	Chat    int64
	Data    int
	FileID  string
	Caption string
	KB      string
	Meta    bot.VideoMeta
}

type fvFakeTG struct {
	mu    sync.Mutex
	calls []fvCall
}

func (g *fvFakeTG) send(ctx context.Context, chat int64, key, name string, data []byte, fileID, caption string, kb map[string]any, m bot.VideoMeta) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, _ := json.Marshal(kb)
	g.calls = append(g.calls, fvCall{chat, len(data), fileID, caption, string(b), m})
	if fileID != "" {
		return fileID, nil
	}
	return "TGFILE1", nil
}

func fakeMP4(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte("\x00\x00\x00\x18ftypmp42"))
	return b
}

func fvSeedFS() fs.FS {
	return fstest.MapFS{
		"videos.json": {Data: []byte(`{"videos":[{"file":"promo.mp4","title":"Промо","topic":"что такое BS","step":"start","caption":"{имя}, это BS — за 30 секунд","w":720,"h":1280,"dur":33}]}`)},
		"promo.mp4":   {Data: fakeMP4(4096)},
		"other.mp4":   {Data: fakeMP4(2048)},
	}
}

type fvEnv struct {
	*salesEnv
	files *fvFakeFiles
	vtg   *fvFakeTG
	v     *FunnelVideos
}

func newFVEnv(t *testing.T) *fvEnv {
	se := newSalesEnv(t)
	e := &fvEnv{salesEnv: se, files: &fvFakeFiles{}, vtg: &fvFakeTG{}}
	e.v = NewFunnelVideos(se.docs, e.files, []byte("secret"))
	e.v.now = se.clock
	se.s.F.Videos, se.s.F.Video = e.v, e.vtg.send
	return e
}

// R55: the shipped videos come in once, with the step of videos.json; a
// video without a line stays switched off; a deleted seed is not brought
// back; the long dash of the caption is replaced.
func TestR55FunnelVideoSeed(t *testing.T) {
	e := newFVEnv(t)
	ctx := context.Background()
	n, err := e.v.Seed(ctx, fvSeedFS())
	if err != nil || n != 2 {
		t.Fatalf("seed %d %v", n, err)
	}
	if n, _ := e.v.Seed(ctx, fvSeedFS()); n != 0 {
		t.Fatalf("seeded again: %d", n)
	}
	it := e.v.ForStep(ctx, "start")
	if it == nil || it.Title != "Промо" || it.W != 720 || it.Dur != 33 || strings.Contains(it.Caption, "—") {
		t.Fatalf("start video %+v", it)
	}
	if st := e.v.Steps(ctx); len(st) != 1 || !st["start"] {
		t.Fatalf("steps %v", st)
	}
	// delete the seed through the platform: it stays deleted
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: it.ID}}
	c.Request = httptest.NewRequest("DELETE", "/", nil)
	e.v.Delete(c)
	if w.Code != 200 {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
	if n, _ := e.v.Seed(ctx, fvSeedFS()); n != 0 || e.v.ForStep(ctx, "start") != nil {
		t.Fatal("a deleted seed came back")
	}
	if f, _ := e.files.GetFile(ctx, it.File); f != nil {
		t.Fatal("the file of a deleted video stays")
	}
}

// The video shipped in the repo: Telegram-friendly and on the start step.
func TestR55ShippedPromo(t *testing.T) {
	names, _ := fs.Glob(funnelvideo.FS, "*.mp4")
	if len(names) == 0 {
		t.Fatal("no video in web/funnel_video")
	}
	for _, n := range names {
		b, _ := fs.ReadFile(funnelvideo.FS, n)
		if len(b) > 8<<20 || string(b[4:8]) != "ftyp" {
			t.Fatalf("%s: %d bytes, not a small mp4", n, len(b))
		}
	}
	e := newFVEnv(t)
	if _, err := e.v.Seed(context.Background(), funnelvideo.FS); err != nil {
		t.Fatal(err)
	}
	it := e.v.ForStep(context.Background(), "start")
	if it == nil || !strings.Contains(it.Caption, "50 000 ₸") || it.H != 1280 {
		t.Fatalf("promo %+v", it)
	}
}

// A lead of the bot gets the start video 20+ minutes after /start, once,
// with the express-разбор button; the first time the file goes up, then the
// kept file_id; the warm-up day's video comes before that day's touch.
func TestR55FunnelVideoSent(t *testing.T) {
	e := newFVEnv(t)
	ctx := context.Background()
	if _, err := e.v.Seed(ctx, fvSeedFS()); err != nil {
		t.Fatal(err)
	}
	now := e.now // 06.10.2026 12:00 Almaty
	lead := func(tg int64, name string, age time.Duration, warm int) map[string]any {
		return map[string]any{"id": "tg" + name, "tgId": float64(tg), "name": name + " Тестов", "col": "new", "funnel": "bot",
			"source": "Threads: пост 05.10 14:48", "startAt": now.Add(-age).UTC().Format(time.RFC3339), "warm": float64(warm), "warmV": float64(2)}
	}
	setLeadDoc(t, e.salesEnv,
		lead(501, "Айдар", 30*time.Minute, 0), // due
		lead(502, "Бота", 5*time.Minute, 0),   // too early
		lead(503, "Старый", 3*24*time.Hour, 5), // started long ago: no start video
		lead(504, "Дана", 25*time.Hour, 0),     // day 1 touch due
	)
	// day 1 has a video too
	other := ""
	lib, _ := e.v.load(ctx)
	for _, x := range lib.Videos {
		if x.Step == "" {
			other = x.ID
		}
	}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: other}}
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"step":"d1","on":true,"caption":"День 1"}`))
	e.v.Put(c)
	if w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}

	e.s.F.WarmOnce(ctx)
	e.vtg.mu.Lock()
	calls := append([]fvCall{}, e.vtg.calls...)
	e.vtg.calls = nil
	e.vtg.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("videos: %+v", calls)
	}
	var start, d1 *fvCall
	for i := range calls {
		switch calls[i].Chat {
		case 501:
			start = &calls[i]
		case 504:
			d1 = &calls[i]
		}
	}
	if start == nil || start.Data == 0 || !strings.HasPrefix(start.Caption, "Айдар, это BS") || !strings.Contains(start.KB, "?p=razbor") || start.Meta.H != 1280 {
		t.Fatalf("start video %+v", start)
	}
	if d1 == nil || d1.Caption != "День 1" {
		t.Fatalf("day 1 video %+v", d1)
	}
	// the day-1 touch went too, after its video
	msgs := e.tg.to(504)
	if len(msgs) != 1 {
		t.Fatalf("day 1 touch %+v", msgs)
	}
	crm := e.docs.get("club", "bs_crm")
	for _, x := range crm["leads"].([]any) {
		l := x.(map[string]any)
		fv, _ := l["fv"].(map[string]any)
		switch l["tgId"].(float64) {
		case 501:
			if fv["start"] == nil {
				t.Fatal("no mark")
			}
		case 502, 503:
			if fv != nil {
				t.Fatalf("%v got a video", l["name"])
			}
		}
	}
	// once only; the next lead gets the kept Telegram file
	e.at(now.Add(time.Hour))
	e.s.F.WarmOnce(ctx)
	e.vtg.mu.Lock()
	calls = append([]fvCall{}, e.vtg.calls...)
	e.vtg.calls = nil
	e.vtg.mu.Unlock()
	if len(calls) != 1 || calls[0].Chat != 502 || calls[0].FileID != "TGFILE1" || calls[0].Data != 0 {
		t.Fatalf("second pass %+v", calls)
	}
	if it := e.v.ForStep(ctx, "start"); it.Sent != 2 || it.TgFileID != "TGFILE1" {
		t.Fatalf("library %+v", it)
	}
}

func TestR55FVCaption(t *testing.T) {
	if c := fvCaption("{имя}, 60 секунд о деньгах", ""); c != "60 секунд о деньгах" {
		t.Fatal(c)
	}
	if c := fvCaption("{имя}, что вы получите", ""); c != "Что вы получите" {
		t.Fatal(c)
	}
	if c := fvCaption("{имя}, привет", "Айдар"); c != "Айдар, привет" {
		t.Fatal(c)
	}
}

// Upload: an MP4 only, the new one takes its step, the preview link is signed.
func TestR55FunnelVideoUpload(t *testing.T) {
	e := newFVEnv(t)
	ctx := context.Background()
	if _, err := e.v.Seed(ctx, fvSeedFS()); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	up := func(data []byte, step string) (int, map[string]any) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "Кейс Исфандияр.mp4")
		fw.Write(data)
		mw.WriteField("title", "Кейс — Исфандияр")
		mw.WriteField("step", step)
		mw.WriteField("dur", "41")
		mw.Close()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", &body)
		c.Request.Header.Set("Content-Type", mw.FormDataContentType())
		e.v.Upload(c)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := up([]byte("not a video at all"), "start"); code != 400 {
		t.Fatalf("not mp4: %d", code)
	}
	code, out := up(fakeMP4(3000), "start")
	if code != 200 || out["title"] != "Кейс - Исфандияр" || out["dur"] != float64(41) || out["on"] != true {
		t.Fatalf("upload %d %v", code, out)
	}
	if it := e.v.ForStep(ctx, "start"); it == nil || it.Source != "upload" {
		t.Fatalf("the new video should take the step: %+v", it)
	}
	// the preview: a signed link, Range works
	url := out["url"].(string)
	name := url[strings.LastIndex(url, "/")+1:]
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "name", Value: name}}
	c.Request = httptest.NewRequest("GET", url, nil)
	c.Request.Header.Set("Range", "bytes=0-99")
	e.v.PublicFile(c)
	if w.Code != 206 || w.Body.Len() != 100 {
		t.Fatalf("preview %d %d", w.Code, w.Body.Len())
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "name", Value: strings.Replace(name, "-", "-x", 1)}}
	c.Request = httptest.NewRequest("GET", "/", nil)
	e.v.PublicFile(c)
	if w.Code != 404 {
		t.Fatalf("bad signature served: %d", w.Code)
	}
}

// The numbers of the funnel from the CRM and the slots.
func TestR55FunnelStats(t *testing.T) {
	e := newFVEnv(t)
	now := e.now
	at := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	setLeadDoc(t, e.salesEnv,
		map[string]any{"tgId": float64(1), "funnel": "bot", "col": "new", "source": "Threads: пост 05.10 14:48", "startAt": at(2 * 24 * time.Hour), "warm": float64(1), "pain": "sales",
			"qz": map[string]any{"sales": map[string]any{"st": "x", "fin": "y"}}, "fv": map[string]any{"start": "x"}},
		map[string]any{"tgId": float64(2), "funnel": "bot", "col": "meet", "source": "Telegram: бот", "startAt": at(3 * 24 * time.Hour), "warm": float64(3), "razborSlot": "s1",
			"log": []any{map[string]any{"text": "Заблокировал бота, прогрев остановлен"}}},
		map[string]any{"tgId": float64(3), "funnel": "bot", "col": "won", "source": "Instagram шапка", "startAt": at(5 * 24 * time.Hour), "pz": map[string]any{"st": "wait"}},
		map[string]any{"tgId": float64(4), "funnel": "bot", "col": "new", "source": "Threads", "startAt": at(40 * 24 * time.Hour)}, // too old
		map[string]any{"tgId": float64(5), "funnel": "platform", "col": "new", "source": "platform_login", "startAt": at(time.Hour)},
	)
	e.docs.put(t, "club", slotsDoc, map[string]any{"slots": []any{map[string]any{"id": "s1", "start": now.Add(48 * time.Hour).UTC().Format(time.RFC3339), "dur": 60, "status": "booked",
		"booking": map[string]any{"tgId": float64(2), "paid": true}}}})
	s, err := e.s.F.FunnelStats(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if s.Leads != 3 || s.Threads != 1 || s.ThreadPost != 1 || s.NoSource != 1 || s.Pain != 1 || s.QuizFin != 1 || s.Warm1 != 2 || s.Warm3 != 1 ||
		s.Blocked != 1 || s.Videos["start"] != 1 || s.Booked != 2 || s.Paid != 1 || s.Razbor != 1 || s.Won != 1 {
		t.Fatalf("stats %+v", s)
	}
	if l := s.Line(); !strings.Contains(l, "leads 3 (Threads 1") || !strings.Contains(l, "videos [start 1]") {
		t.Fatal(l)
	}
}
