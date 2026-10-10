package http

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// The shipped extension: enough items, every card complete, no duplicates,
// brand rules (no em dash, numbers like «10 000»).
func TestLibExtContent(t *testing.T) {
	ext, err := content.LibExt()
	if err != nil {
		t.Fatal(err)
	}
	if len(ext.Diag) < 40 || len(ext.Tools) < 40 {
		t.Fatalf("diag %d, tools %d", len(ext.Diag), len(ext.Tools))
	}
	organs := map[string]bool{"Финансы": true, "Продажи": true, "Маркетинг": true, "Команда": true, "Процессы": true,
		"Стратегия": true, "Аналитика": true, "Продукт": true, "Мышление": true, "Энергия": true, "Цели": true, "Окружение": true}
	ids, titles := map[string]bool{}, map[string]bool{}
	// Инструменты из исходной библиотеки страницы, на которые ссылаются новые диагнозы.
	toolTitles := map[string]bool{}
	for _, s := range []string{
		"3 ветки масштабирования", "4 вида креативов", "5 почему?", "ABCDX сегментация", "CustDev", "HADI цикл",
		"Unit экономика", "Аудит закупок и себестоимости", "Аудит энергии", "Бизнес через 1 и 3 года", "Воронка и конверсии",
		"Восемь видов потерь", "Выход из операционки", "Декомпозиция цели", "Еженедельный обзор", "Карта потока ценности",
		"Карта процессов: от заявки до денег", "Коллаборации и сарафан", "Лестница целей", "Менеджмент: 4 шага",
		"Обратная связь от 10 человек", "Окно Джохари", "Онбординг и адаптация", "Описание роли", "Оргструктура",
		"Панель показателей", "Платёжный календарь", "Поднять средний чек", "Портрет клиента", "Постановка проблемы",
		"Продуктовая матрица ABC", "Регламент процесса", "Регламент работы с заявкой", "Скрипт продаж",
		"Трекер выполненных задач", "Управленческий отчёт", "Учёт источников", "Финансовая модель", "Шкала найма",
		// R62
		"Матрица Эйзенхауэра", "Аудит времени собственника", "Матрица приоритетов", "Ограничивающие убеждения",
		"Аудит воронки: где утечка", "Диагностика таргета по CTR", "Найм флагмана", "Формула перемен", "SPACE модель",
	} {
		toolTitles[s] = true
	}
	for _, t0 := range ext.Tools {
		toolTitles[t0["title"].(string)] = true
	}
	bigNum := regexp.MustCompile(`(^|[^\d #])\d{5,}`)
	check := func(kind string, it map[string]any, fields []string, lists map[string]int) {
		id, _ := it["id"].(string)
		title, _ := it["title"].(string)
		if id == "" || ids[id] || title == "" || titles[libNorm(title)] {
			t.Errorf("%s: id %q title %q missing or repeated", kind, id, title)
		}
		ids[id], titles[libNorm(title)] = true, true
		if !organs[it["organ"].(string)] {
			t.Errorf("%s: organ %v", id, it["organ"])
		}
		for _, f := range fields {
			if s, _ := it[f].(string); strings.TrimSpace(s) == "" {
				t.Errorf("%s: no %s", id, f)
			}
		}
		for f, n := range lists {
			l, _ := it[f].([]any)
			if len(l) < n {
				t.Errorf("%s: %s has %d, want %d", id, f, len(l), n)
			}
		}
		b, _ := json.Marshal(it)
		s := string(b)
		if strings.Contains(s, "—") {
			t.Errorf("%s: em dash", id)
		}
		if m := bigNum.FindString(s); m != "" && !strings.Contains(s, "http") {
			t.Errorf("%s: number format %q", id, m)
		}
		if cure, ok := it["cure"].([]any); ok {
			for _, c := range cure {
				if !toolTitles[c.(string)] {
					t.Errorf("%s: cure %q is not a tool", id, c)
				}
			}
		}
	}
	for _, it := range ext.Diag {
		check("diag", it, []string{"color", "icon", "desc", "risk"}, map[string]int{"signs": 4, "questions": 3, "cure": 1})
	}
	for _, it := range ext.Tools {
		if it["isExt"] == true { // R83: an external material: a description and the link to it
			check("tool", it, []string{"color", "icon", "short", "why", "time", "link", "source"}, map[string]int{"how": 3})
			continue
		}
		check("tool", it, []string{"color", "icon", "short", "why", "example", "time"}, map[string]int{"how": 5, "check": 3})
	}
	n := 0
	for _, l := range ext.Questions {
		n += len(l)
	}
	if n < 30 {
		t.Fatalf("questions %d", n)
	}
}

func TestMergeLibExt(t *testing.T) {
	repo, ctx := testPlatformDB(t, libExtStateKey, "bs_diag", "bs_tools", "bs_questions", "bs_libver")
	ext, err := content.LibExt()
	if err != nil || len(ext.Diag) == 0 || len(ext.Tools) == 0 {
		t.Fatal(err)
	}
	h := NewPlatformAI(repo, nil)
	put := func(k, v string) {
		d, _ := repo.GetDoc(ctx, "club", k)
		base := 0
		if d != nil {
			base = d.Version
		}
		if _, err := repo.PutDoc(ctx, "club", k, base, v, false, "team"); err != nil {
			t.Fatal(err)
		}
	}
	// The team already has one of the new diagnoses under its own spelling,
	// and its own card that must stay as it is.
	same := strings.ToUpper(ext.Diag[0]["title"].(string)) + "!"
	team := `{"organ":"Финансы","icon":"◆","title":"Своя карточка","desc":"правка команды"}`
	put("bs_diag", `[`+team+`,{"organ":"Финансы","icon":"◆","title":"`+same+`","desc":"своё"}]`)
	put("bs_tools", `[{"organ":"Финансы","icon":"◇","title":"Платёжный календарь","short":"x"}]`)
	put("bs_questions", `{"Финансы":["Свой вопрос?"]}`)

	// No bs_libver yet: the page would replace bs_diag/bs_tools, so only the
	// questions are merged.
	got, err := h.MergeLibExt(ctx)
	if err != nil || got["bs_diag"] != 0 || got["bs_tools"] != 0 || got["bs_questions"] == 0 {
		t.Fatalf("before libver: %v %v", got, err)
	}
	put("bs_libver", "3")
	got, err = h.MergeLibExt(ctx)
	if err != nil || got["bs_diag"] != len(ext.Diag)-1 || got["bs_tools"] != len(ext.Tools)+len(ext.Books) || got["bs_questions"] != 0 {
		t.Fatalf("merge: %v %v", got, err)
	}
	var diag []map[string]any
	d, _ := repo.GetDoc(ctx, "club", "bs_diag")
	_ = json.Unmarshal([]byte(d.Value), &diag)
	if len(diag) != len(ext.Diag)+1 || diag[0]["desc"] != "правка команды" || diag[1]["desc"] != "своё" {
		t.Fatalf("diag: %d %v", len(diag), diag[:2])
	}
	v := d.Version
	// Second run: nothing to do.
	if got, err = h.MergeLibExt(ctx); err != nil || len(got) != 0 {
		t.Fatalf("again: %v %v", got, err)
	}
	if d, _ = repo.GetDoc(ctx, "club", "bs_diag"); d.Version != v {
		t.Fatal("bs_diag rewritten")
	}
	// The team deletes an added card; a new version must not bring it back.
	diag = append(diag[:2], diag[3:]...)
	b, _ := json.Marshal(diag)
	put("bs_diag", string(b))
	_, _ = repo.PutDoc(ctx, "server", libExtStateKey, mustVersion(t, h, libExtStateKey), strings.Replace(mustValue(t, h, libExtStateKey), content.LibExtVersion, "old", 1), false, "test")
	if got, err = h.MergeLibExt(ctx); err != nil || got["bs_diag"] != 0 || got["bs_tools"] != 0 {
		t.Fatalf("new version: %v %v", got, err)
	}
	d, _ = repo.GetDoc(ctx, "club", "bs_diag")
	_ = json.Unmarshal([]byte(d.Value), &diag)
	if len(diag) != len(ext.Diag) {
		t.Fatalf("deleted card came back: %d", len(diag))
	}
	var q map[string][]string
	d, _ = repo.GetDoc(ctx, "club", "bs_questions")
	_ = json.Unmarshal([]byte(d.Value), &q)
	if q["Финансы"][0] != "Свой вопрос?" || len(q["Финансы"]) < 2 {
		t.Fatalf("questions: %v", q["Финансы"])
	}
}

func mustVersion(t *testing.T, h *PlatformAI, key string) int {
	d, err := h.repo.GetDoc(t.Context(), "server", key)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	return d.Version
}

func mustValue(t *testing.T, h *PlatformAI, key string) string {
	d, err := h.repo.GetDoc(t.Context(), "server", key)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	return d.Value
}
