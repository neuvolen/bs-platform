package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/migrations"
	"github.com/gin-gonic/gin"
)

func TestWhatsAppCRM(t *testing.T) {
	dsn := os.Getenv("BS_TEST_DSN")
	if dsn == "" {
		t.Skip("BS_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pg.NewDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = pg.Migrate(ctx, db, migrations.FS)
	_, _ = db.Pool.Exec(ctx, `TRUNCATE crm_messages`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_docs WHERE key = 'bs_crm'`)
	var calls []string
	var sent map[string]any
	green := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, strings.Split(r.URL.Path, "/")[2])
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "sendMessage"):
			_ = json.Unmarshal(b, &sent)
			_, _ = w.Write([]byte(`{"idMessage":"OUT1"}`))
		case strings.Contains(r.URL.Path, "getStateInstance"):
			_, _ = w.Write([]byte(`{"stateInstance":"authorized"}`))
		default:
			_, _ = w.Write([]byte(`{"saveSettings":true}`))
		}
	}))
	defer green.Close()
	t.Setenv("GREEN_API_ID", "1101")
	t.Setenv("GREEN_API_TOKEN", "tok")
	t.Setenv("GREEN_API_URL", green.URL)
	t.Setenv("PUBLIC_URL", "https://app.bxclub.kz")
	repo := pg.NewPlatformRepo(db)
	_, _ = repo.PutDoc(ctx, "club", "bs_crm", 0, `{"leads":[{"id":"l1","col":"work","name":"Асем","phone":"8 701 111 22 33"}]}`, false, "t")
	h := NewPlatformAI(repo, &ai.Client{HTTP: green.Client()})
	h.SetupWhatsApp(ctx)
	if len(calls) != 1 || calls[0] != "setSettings" {
		t.Fatalf("setup: %v", calls)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin") })
	r.POST("/hook/:secret", h.WAWebhook)
	r.GET("/chats", h.WAChats)
	r.GET("/chats/:phone", h.WAMessages)
	r.POST("/chats/:phone/send", h.WASend)
	secret := (&greenAPI{token: "tok"}).webhookSecret()
	hook := func(s, body string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/hook/"+s, strings.NewReader(body)))
		return w.Code
	}
	in := func(id, chat, name, text string) string {
		return `{"typeWebhook":"incomingMessageReceived","idMessage":"` + id + `","timestamp":1790000000,"senderData":{"chatId":"` + chat + `","senderName":"` + name + `"},"messageData":{"typeMessage":"textMessage","textMessageData":{"textMessage":"` + text + `"}}}`
	}
	if hook("wrong", in("A", "77011112233@c.us", "Асем", "x")) != 404 {
		t.Fatal("a wrong secret must be refused")
	}
	hook(secret, in("A1", "77011112233@c.us", "Асем", "Здравствуйте, сколько стоит разбор?"))
	hook(secret, in("A1", "77011112233@c.us", "Асем", "Здравствуйте, сколько стоит разбор?")) // repeat
	hook(secret, in("B1", "77779998877@c.us", "Ержан", "Хочу в клуб"))
	hook(secret, in("G1", "120363@g.us", "Группа", "спам"))
	d, _ := repo.GetDoc(ctx, "club", "bs_crm")
	if strings.Count(d.Value, `"source":"WhatsApp"`) != 1 || !strings.Contains(d.Value, `"waLast":"Здравствуйте, сколько стоит разбор?"`) || !strings.Contains(d.Value, `"waUnread":1`) {
		t.Fatalf("crm: %s", d.Value)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/chats", nil))
	if !strings.Contains(w.Body.String(), `"phone":"77779998877"`) || strings.Count(w.Body.String(), `"phone"`) != 2 {
		t.Fatalf("chats: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/chats/87011112233/send", strings.NewReader(`{"text":"Разбор стоит …"}`)))
	if !strings.Contains(w.Body.String(), `"ok":true`) || sent["chatId"] != "77011112233@c.us" {
		t.Fatalf("send: %s %v", w.Body.String(), sent)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/chats/77011112233", nil))
	if strings.Count(w.Body.String(), `"dir"`) != 2 {
		t.Fatalf("messages: %s", w.Body.String())
	}
}
