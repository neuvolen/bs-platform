package http

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

func TestNewRazborPrice(t *testing.T) {
	for in, want := range map[string]string{
		"Разбор за 30 000 ₸: t.me/x": "Разбор за 50 000 ₸: t.me/x",
		"Экспресс-разбор с Рустамом и Береке. 60 минут, 30 000 ₸.": "Экспресс-разбор с Рустамом и Береке. 60 минут, 50 000 ₸.",
		"Мне говорят: «30 000 ₸ за час дорого».":                   "Мне говорят: «50 000 ₸ за час дорого».",
		"Вход за 30 000 ₸: экспресс-разбор за 1 час":               "Вход за 50 000 ₸: экспресс-разбор за 1 час",
		"Премия 30 000 ₸ при всех.":                                "Премия 30 000 ₸ при всех.",
		"Нурлан купил 20 отзывов за 30 000 ₸.":                     "Нурлан купил 20 отзывов за 30 000 ₸.",
		"разбор: грант 1 730 000 ₸ и 230 000 ₸":                    "разбор: грант 1 730 000 ₸ и 230 000 ₸",
		"Разбор стоит 30 000 тенге":                                "Разбор стоит 50 000 тенге",
	} {
		if got := newRazborPrice(in); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
	// Nothing shipped still carries the old разбор price.
	_ = fs.WalkDir(content.LibraryFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := fs.ReadFile(content.LibraryFS(), p)
		if s := string(b); newRazborPrice(s) != s {
			t.Errorf("%s: old разбор price left", p)
		}
		return nil
	})
	if s := string(content.Marketing); newRazborPrice(s) != s {
		t.Error("marketing.json: old разбор price left")
	}
}

func TestMigrateRazborPrice(t *testing.T) {
	repo, ctx := testPlatformDB(t, priceMigrateKey, slotsDoc, contentKey, "bs_mkt_analysis")
	for k, v := range map[string]string{
		slotsDoc:          `{"price":30000,"kaspiLink":"k","slots":[]}`,
		contentKey:        `{"queue":[{"text":"Премия 30 000 ₸ повару.\n\nРазбор за 30 000 ₸: t.me/bsurgery_bot?start=th_x","edited":true}],"history":[{"text":"Разбор за 30 000 ₸"}]}`,
		"bs_mkt_analysis": `{"value":{"price_logic":"Экспресс-разбор 30 000 ₸ за 1 час"},"competitors":[{"price":"30 000 ₸ в месяц"}]}`,
	} {
		if _, err := repo.PutDoc(ctx, "club", k, 0, v, false, "team"); err != nil {
			t.Fatal(err)
		}
	}
	h := NewPlatformAI(repo, nil)
	h.MigrateRazborPrice(ctx)
	d, _ := repo.GetDoc(ctx, "club", slotsDoc)
	var slots map[string]any
	_ = json.Unmarshal([]byte(d.Value), &slots)
	if anyInt(slots["price"]) != 50000 {
		t.Fatalf("slots: %s", d.Value)
	}
	d, _ = repo.GetDoc(ctx, "club", contentKey)
	if !strings.Contains(d.Value, "Премия 30 000 ₸") || !strings.Contains(d.Value, "Разбор за 50 000 ₸: t.me") || !strings.Contains(d.Value, `"history":[{"text":"Разбор за 30 000 ₸"}]`) {
		t.Fatalf("content: %s", d.Value)
	}
	d, _ = repo.GetDoc(ctx, "club", "bs_mkt_analysis")
	if !strings.Contains(d.Value, "Экспресс-разбор 50 000 ₸") || !strings.Contains(d.Value, `"30 000 ₸ в месяц"`) {
		t.Fatalf("mkt: %s", d.Value)
	}
	// Once: a price the team sets back later stays.
	_, _ = repo.PutDoc(ctx, "club", slotsDoc, mustClubVersion(t, h, slotsDoc), `{"price":30000}`, false, "team")
	h.MigrateRazborPrice(ctx)
	if d, _ = repo.GetDoc(ctx, "club", slotsDoc); d.Value != `{"price":30000}` {
		t.Fatalf("second run changed slots: %s", d.Value)
	}
}

func mustClubVersion(t *testing.T, h *PlatformAI, key string) int {
	d, err := h.repo.GetDoc(t.Context(), "club", key)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	return d.Version
}
