package content

import (
	"encoding/json"
	"strings"
	"testing"
)

// The shelf: enough books in every category, each with its use for the owner
// and links only to diagnoses and tools that exist; brand rules.
func TestBooksShelf(t *testing.T) {
	books, err := Books()
	if err != nil {
		t.Fatal(err)
	}
	if len(books) < 120 {
		t.Fatalf("books %d", len(books))
	}
	ids := map[string]bool{}
	read := func(fs interface{ ReadFile(string) ([]byte, error) }, name string) {
		b, err := fs.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var l []map[string]any
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatal(err)
		}
		for _, it := range l {
			if s, _ := it["id"].(string); s != "" {
				ids[s] = true
			}
		}
	}
	read(libRichFS, "library_rich/diag.json")
	read(libRichFS, "library_rich/tools.json")
	read(libExtFS, "library_ext/diag.json")
	read(libExtFS, "library_ext/tools.json")
	cats := map[string]int{}
	seen, titles := map[string]bool{}, map[string]bool{}
	for _, b := range books {
		if !strings.HasPrefix(b.ID, "bk_") || seen[b.ID] || titles[b.ToolTitle()] {
			t.Errorf("%s: id or title repeated", b.ID)
		}
		seen[b.ID], titles[b.ToolTitle()] = true, true
		if b.RU == "" || b.Author == "" || b.Year < 1900 || b.Year > 2026 || (b.Orig != "" && b.AuthorEn == "") {
			t.Errorf("%s: incomplete %+v", b.ID, b)
		}
		if strings.Count(b.Use, ".") < 2 || len([]rune(b.Use)) > 260 {
			t.Errorf("%s: use must be two sentences: %q", b.ID, b.Use)
		}
		if BookOrgan(b.Cat) == "Стратегия" && b.Cat != "Стратегия" && b.Cat != "Истории компаний" {
			t.Errorf("%s: category %q", b.ID, b.Cat)
		}
		cats[b.Cat]++
		if len(b.Diag) == 0 {
			t.Errorf("%s: no diagnosis", b.ID)
		}
		for _, id := range append(append([]string{}, b.Diag...), b.Tools...) {
			if !ids[id] || CardTitle(id) == "" {
				t.Errorf("%s: no card %s", b.ID, id)
			}
		}
		raw, _ := json.Marshal(b)
		if strings.Contains(string(raw), "—") {
			t.Errorf("%s: em dash", b.ID)
		}
	}
	for _, c := range BookCats {
		if cats[c.Name] < 6 {
			t.Errorf("category %s: %d books", c.Name, cats[c.Name])
		}
	}
	// The ten books that were on the platform keep their titles: the merge
	// matches by title, so they are not added a second time.
	for _, old := range []string{"Книга: «Принципы» · Рэй Далио", "Книга: «iПрезентация» · Кармин Галло", "Книга: «От нуля к единице» · Питер Тиль",
		"Книга: «Номер 1» · Игорь Манн", "Книга: «Стартап без бюджета» · Майк Микаловиц", "Книга: «Стартап за $100» · Крис Гильбо",
		"Книга: «Rework» · Джейсон Фрайд, Дэвид Хайнемайер Ханссон", "Книга: «7 навыков высокоэффективных людей» · Стивен Кови",
		"Книга: «Scrum» · Джефф Сазерленд", "Книга: «Стратегия голубого океана» · Чан Ким, Рене Моборн"} {
		if !titles[old] {
			t.Errorf("platform book %q not on the shelf under the same title", old)
		}
	}
	items := BookToolItems()
	if len(items) != len(books) || items[0]["isBook"] != true || items[0]["title"] != books[0].ToolTitle() {
		t.Fatalf("tool items %d %v", len(items), items[0])
	}
	ext, err := LibExt()
	if err != nil || len(ext.Books) != len(books) {
		t.Fatalf("ext books %d %v", len(ext.Books), err)
	}
}
