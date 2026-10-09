package tgevents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The fixture is a synthetic t.me/s page with the markup of the real preview
// (posts modelled on the channel «Возможности startup-course.com»).
func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/startup_course_com.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePage(t *testing.T) {
	pg := ParsePage(fixture(t))
	if pg.Title != "📢 Возможности 🚀 startup-course.com 💰" || pg.Before != 6303 {
		t.Fatalf("title %q before %d", pg.Title, pg.Before)
	}
	if len(pg.Posts) != 8 || pg.Posts[0].ID != 6303 || pg.Posts[7].ID != 6310 {
		t.Fatalf("posts %+v", pg.Posts)
	}
	p := pg.Posts[3]
	if strings.Contains(p.Text, "Старый пост") || !strings.HasPrefix(p.Text, "🎓 GSEA Kazakhstan") {
		t.Fatalf("the reply quote must not be the text: %q", p.Text)
	}
	if p.Time.Format(time.RFC3339) != "2026-10-07T21:17:00Z" || p.URL() != "https://t.me/startup_course_com/6306" {
		t.Fatal(p.Time, p.URL())
	}
	if len(pg.Posts[1].Links) != 2 || pg.Posts[1].Links[0] != "https://forms.example.kz/centras" {
		t.Fatal(pg.Posts[1].Links)
	}
	if !strings.Contains(pg.Posts[1].Text, "\nФинал: 4 ноября в Алматы\n") {
		t.Fatalf("lines lost: %q", pg.Posts[1].Text)
	}
}

func TestExtractRules(t *testing.T) {
	pg := ParsePage(fixture(t))
	today := "2026-10-09"
	got := map[int]Result{}
	for _, p := range pg.Posts {
		got[p.ID] = Extract(p, today)
	}
	if r := got[6303]; r.OK || r.NeedAI {
		t.Fatalf("a passed event: %+v", r)
	}
	if r := got[6305]; r.OK || r.NeedAI {
		t.Fatalf("an ad: %+v", r)
	}
	if r := got[6308]; r.OK || !r.NeedAI {
		t.Fatalf("a digest goes to the AI: %+v", r)
	}
	c := got[6304].Event
	if !got[6304].OK || c.Kind != KindOpportunity || c.Date != "2026-10-14" || c.Deadline != "2026-10-14" ||
		c.Place != "Алматы" || c.URL != "https://forms.example.kz/centras" || c.Post != "https://t.me/startup_course_com/6304" ||
		!strings.HasPrefix(c.Title, "Кейс-челлендж Centras Leadership Challenge") || c.Origin != "tg" || c.Source != "t.me/startup_course_com" {
		t.Fatalf("centras %+v", c)
	}
	g := got[6306].Event
	if g.Kind != KindOpportunity || g.Date != "2027-01-14" || g.URL != "https://gsea.example.org/kz" || g.Title != "GSEA Kazakhstan: конкурс для студентов-предпринимателей" {
		t.Fatalf("gsea %+v", g)
	}
	hk := got[6307].Event
	if hk.Kind != KindOpportunity || hk.Date != "2026-10-15" || !hk.Online || hk.Place != "Онлайн" || hk.URL != "https://www.hkstp.example/ideation" {
		t.Fatalf("hkstp %+v", hk)
	}
	n := got[6309].Event
	if n.Kind != "" || n.Date != "2026-10-17" || n.Time != "19:00" || n.Place != "Astana Hub, Алматы" || n.Org != "NU STeP" ||
		n.Price != "бесплатно" || n.URL != "https://lu.ma/founders-almaty" || n.Title != "Нетворкинг для фаундеров" {
		t.Fatalf("networking %+v", n)
	}
	s := got[6310].Event
	if s.Kind != "" || s.Date != "2026-10-22" || s.Deadline != "2026-10-20" || !s.Online || s.Place != "Онлайн" || s.Title != "Global Founders Summit" {
		t.Fatalf("summit %+v", s)
	}
	if strings.Contains(n.Desc, "Нетворкинг для фаундеров") || !strings.Contains(n.Desc, "17 октября") {
		t.Fatalf("desc %q", n.Desc)
	}
}

func TestYearInference(t *testing.T) {
	post := time.Date(2026, 12, 20, 10, 0, 0, 0, Almaty)
	ds := dates("Старт 10 января, заявки до 25.12", post)
	if len(ds) != 2 || ds[0].date != "2027-01-10" || ds[1].date != "2026-12-25" || !ds[1].deadline || ds[0].deadline {
		t.Fatalf("%+v", ds)
	}
	if d := dates("3 marketing tips, 1.5 млн, 18:30", post); len(d) != 0 {
		t.Fatalf("not dates: %+v", d)
	}
	if d := dates("Конференция 5-6 марта 2027", post); len(d) != 1 || d[0].date != "2027-03-05" {
		t.Fatalf("%+v", d)
	}
}

func TestFetchAndAI(t *testing.T) {
	page := fixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s/startup_course_com" {
			_, _ = w.Write([]byte(page))
			return
		}
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Telegram: Contact @nobody"></head><body class="tgme_page"></body></html>`))
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL + "/s/"
	defer func() { BaseURL = old }()
	pg, err := Fetch(context.Background(), srv.Client(), "startup_course_com", 0)
	if err != nil || len(pg.Posts) != 8 {
		t.Fatal(err, len(pg.Posts))
	}
	if _, err := Fetch(context.Background(), srv.Client(), "nobody_here", 0); err != ErrNoChannel {
		t.Fatal(err)
	}
	digest := pg.Posts[5]
	model := func(ctx context.Context, system, prompt string) (string, error) {
		if !strings.Contains(prompt, "=== Пост 6308 от 2026-10-09 ===") || !strings.Contains(prompt, "AI Hackathon") {
			t.Fatalf("prompt %q", prompt)
		}
		return "```json\n" + `{"items":[{"post":6308,"title":"Pitch Night","date":"2026-10-15","time":"19:00","kind":"мероприятие","place":"Алматы","tags":["стартапы"]},` +
			`{"post":6308,"title":"AI Hackathon","date":"2026-10-18","kind":"мероприятие","url":"https://calendar.example.com/oct"},` +
			`{"post":6308,"title":"Грант на пилот","deadline":"2026-10-30","kind":"возможность"},` +
			`{"post":6308,"title":"Старое","date":"2026-10-01"}]}` + "\n```", nil
	}
	evs, err := AIExtract(context.Background(), model, []Post{digest}, time.Date(2026, 10, 9, 12, 0, 0, 0, Almaty))
	if err != nil || len(evs) != 3 {
		t.Fatal(err, evs)
	}
	if evs[0].Title != "Pitch Night" || evs[0].URL != "https://t.me/startup_course_com/6308" || evs[0].Origin != "tg" || evs[0].Kind != "" {
		t.Fatalf("%+v", evs[0])
	}
	if evs[1].URL != "https://calendar.example.com/oct" || evs[1].Post != "https://t.me/startup_course_com/6308" {
		t.Fatalf("%+v", evs[1])
	}
	if evs[2].Kind != KindOpportunity || evs[2].Date != "2026-10-30" || evs[2].Tags[0] != KindOpportunity {
		t.Fatalf("%+v", evs[2])
	}
}
