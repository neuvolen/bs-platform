package http

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

func voiceRouter(role string) (*gin.Engine, *PlatformAI) {
	gin.SetMode(gin.TestMode)
	h := &PlatformAI{AI: &ai.Client{}} // no keys, no ASR: rules, then a note
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("userID", "tg:1") })
	r.POST("/ai/voice", h.Voice)
	r.POST("/ai/command", h.Command)
	r.POST("/ai/notekind", h.NoteKind)
	return r, h
}

func voicePost(r *gin.Engine, fields map[string]string) map[string]any {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("audio", "cmd.wav")
	_, _ = fw.Write([]byte("RIFF fake"))
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/ai/voice", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"_code": w.Code}
	}
	return out
}

func TestVoiceR79(t *testing.T) {
	r, _ := voiceRouter("admin")
	ctx := `{"nodes":[{"id":7,"type":"diag","title":"Кассовые разрывы"},{"id":12,"type":"task","title":"Позвонить поставщику"}],"selected":7,
		"residents":["Даулет Сайты","Асет"],"diag_library":["Кассовые разрывы"],"tool_library":["Платёжный календарь"],"team":["Рустам","Береке"]}`

	// no server ASR here: the browser's own transcript is used
	out := voicePost(r, map[string]string{"local": "джарвис поставь задачу позвонить поставщику до пятницы ответственный береке", "context": ctx})
	acts, _ := out["actions"].([]any)
	if out["via"] != "rules" || out["asr"] != "local" || len(acts) != 1 {
		t.Fatalf("voice: %v", out)
	}
	a := acts[0].(map[string]any)
	if a["op"] != "add_node" || a["type"] != "task" || a["who"] != "Береке" || a["date"] == nil {
		t.Fatalf("task: %v", a)
	}

	// mark done and open a resident
	out = voicePost(r, map[string]string{"local": "Ассистент, отметь задачу позвонить поставщику выполненной", "context": ctx})
	if acts, _ = out["actions"].([]any); len(acts) != 1 || acts[0].(map[string]any)["op"] != "task_done" {
		t.Fatalf("done: %v", out)
	}
	out = voicePost(r, map[string]string{"local": "джарвис открой доску даулета", "context": ctx})
	if acts, _ = out["actions"].([]any); len(acts) != 1 || acts[0].(map[string]any)["name"] != "Даулет Сайты" {
		t.Fatalf("open: %v", out)
	}

	// not a command and no AI: a note, sorted (numbers are numbers)
	out = voicePost(r, map[string]string{"local": "выручка четыре миллиона в месяц", "context": ctx})
	k, _ := out["kind"].(map[string]any)
	if out["via"] != "note" || k == nil || k["kind"] != "metric" {
		t.Fatalf("note: %v", out)
	}

	// nothing heard
	out = voicePost(r, map[string]string{"context": ctx})
	if acts, _ = out["actions"].([]any); len(acts) != 0 || !strings.Contains(out["say"].(string), "Не расслышал") {
		t.Fatalf("silence: %v", out)
	}

	// sorting notes
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ai/notekind", strings.NewReader(`{"items":["конверсия 12%","Нет системы продаж","Внедрить платёжный календарь","Партнёр в Астане"],"diag":["Нет системы продаж"],"tools":["Платёжный календарь"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var nk struct{ Kinds []struct{ Kind, Match string } }
	_ = json.Unmarshal(w.Body.Bytes(), &nk)
	got := []string{}
	for _, x := range nk.Kinds {
		got = append(got, x.Kind)
	}
	if strings.Join(got, ",") != "metric,diag,tool,note" || nk.Kinds[1].Match != "Нет системы продаж" {
		t.Fatalf("notekind: %s", w.Body.String())
	}

	// residents and leads do not command the board
	r2, _ := voiceRouter("resident")
	if out = voicePost(r2, map[string]string{"local": "стоп"}); out["error"] == nil {
		t.Fatalf("resident: %v", out)
	}
}
