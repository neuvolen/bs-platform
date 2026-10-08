package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// R40c: the login page shows the resident's cabinet (no admin sections) and
// a guided demo whose every spoken line has a recorded file.
func TestR40cLoginDemo(t *testing.T) {
	page := string(loginPage.plain)
	lines := LoginTourLines(loginHTML)
	if len(lines) < 8 {
		t.Fatalf("demo has %d spoken lines, want at least 8", len(lines))
	}
	m := regexp.MustCompile(`<script type="application/json" id="bsLoginVoice">(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("bsLoginVoice not in the login page")
	}
	var mp map[string]string
	if err := json.Unmarshal([]byte(m[1]), &mp); err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if mp[l] == "" {
			t.Errorf("no recording for %q: run tools/voice/build.sh --login", l)
		}
	}
	if strings.Contains(page, "<!--LOGINVOICE-->") {
		t.Error("placeholder left in the page")
	}

	// Steps: caption, line and a picture that exists
	tm := loginTourRe.FindSubmatch(loginHTML)
	var d struct {
		Steps []struct {
			Img, T, D, V string
			Spot, Ms     []float64
			End          bool
		} `json:"steps"`
	}
	if err := json.Unmarshal(tm[1], &d); err != nil {
		t.Fatal(err)
	}
	if !d.Steps[len(d.Steps)-1].End {
		t.Error("last step is not the call to action")
	}
	for i, s := range d.Steps {
		if s.T == "" || s.D == "" || s.V == "" {
			t.Errorf("step %d lacks text", i)
		}
		if _, ok := PromoFile(s.Img + ".webp"); !ok {
			t.Errorf("step %d: no picture %s", i, s.Img)
		}
		for _, sp := range [][]float64{s.Spot, s.Ms} {
			if len(sp) != 0 && (len(sp) != 4 || sp[0]+sp[2] > 1 || sp[1]+sp[3] > 1) {
				t.Errorf("step %d: spot %v outside the picture", i, sp)
			}
		}
	}

	// Value for a resident, not for the admin
	for _, want := range []string{"Кабинет резидента", "Карта здоровья", "План лечения", "Инструменты и шаблоны", "Замеры и рост", "Саммари", "Gallup", "Сообщество", "Посмотреть, как это работает", "Войти через Telegram", "Записаться на экспресс-разбор"} {
		if !strings.Contains(page, want) {
			t.Errorf("login page lacks %q", want)
		}
	}
	for _, bad := range []string{"P&amp;L", "P&L", "CRM", "Лиды", "Трекинг резидентов", "Карта диагнозов", "ИИ-рекомендации", "—", "confirm(", "prompt("} {
		if strings.Contains(page, bad) {
			t.Errorf("login page carries %q", bad)
		}
	}

	// Files: served, cached, unknown names refused
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, "x", "bs_session")
	for _, u := range mp {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.Len() < 5000 {
			t.Fatalf("%s: %d %q %d bytes", u, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
		req := httptest.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("If-None-Match", w.Header().Get("ETag"))
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req)
		if w2.Code != http.StatusNotModified {
			t.Fatalf("%s repeat: %d", u, w2.Code)
		}
	}
	for _, bad := range []string{"/voice/login/x.mp3", "/voice/login/0000000000000000.mp3", "/voice/login/..%2F..%2Flogin.html"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, bad, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", bad, w.Code)
		}
	}
	// the platform tour's own files still answer at /voice/<key>.mp3
	for _, tx := range TourTexts() {
		if u := StaticVoice(tx); u != "" {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
			if w.Code != 200 {
				t.Errorf("platform voice %s: %d", u, w.Code)
			}
			break
		}
	}
}
