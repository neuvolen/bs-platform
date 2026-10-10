package http

import (
	"encoding/json"
	"strings"
	"testing"
)

// R78: «Ресурсы клуба» in either shape, the resident's card cleaned, the defaults intact.
func TestR78ResLibShapes(t *testing.T) {
	d, raw := parseResLib(`{"items":[{"cat":"Партнёры","t":"Таксопарк","d":"","who":"","url":"","extra":1}]}`)
	if len(d.Items) != 1 || d.Items[0].T != "Таксопарк" || len(raw) != 1 || raw[0]["extra"] == nil {
		t.Fatalf("object shape: %+v %v", d, raw)
	}
	d, raw = parseResLib(`[{"cat":"Сервисы","t":"Kaspi"}]`)
	if len(d.Items) != 1 || d.Items[0].T != "Kaspi" || len(raw) != 1 {
		t.Fatalf("list shape: %+v %v", d, raw)
	}
	var def []resLibEntry
	if err := json.Unmarshal([]byte(defaultResLib), &def); err != nil || len(def) != 35 {
		t.Fatalf("defaults: %d %v", len(def), err)
	}
}

func TestR78ResourceClean(t *testing.T) {
	in := resourceIn{T: "  Поставщики  ", URL: "@altair"}
	if msg := in.clean(); msg != "" || in.T != "Поставщики" || in.URL != "https://t.me/altair" {
		t.Fatalf("clean: %q %+v", msg, in)
	}
	in = resourceIn{URL: "kaspi.kz"}
	if msg := in.clean(); msg == "" || in.URL != "https://kaspi.kz" {
		t.Fatalf("no title must be refused: %q %+v", msg, in)
	}
	in = resourceIn{T: strings.Repeat("я", 300)}
	in.clean()
	if len([]rune(in.T)) != 120 {
		t.Fatalf("title not clipped: %d", len([]rune(in.T)))
	}
}

func TestR78EventKeyAndICS(t *testing.T) {
	a, b := eventKey("feed", "Завтрак", "2026-10-13", "09:00"), eventKey("bs", "Завтрак", "2026-10-13", "09:00")
	if a == b || len(a) != 12 {
		t.Fatalf("keys: %s %s", a, b)
	}
	if got := icsEsc("a,b;c\nd"); got != `a\,b\;c\nd` {
		t.Fatalf("ics escape: %s", got)
	}
	if !looksOnline("Онлайн, Zoom") || looksOnline("Алматы, Rixos") {
		t.Fatal("online by place")
	}
}
