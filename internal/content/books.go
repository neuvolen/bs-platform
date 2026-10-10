package content

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

// Полка: книжная полка клуба. books/books.json: самые полезные собственнику
// книги (русские издания) по категориям, с пользой для собственника и
// диагнозами и инструментами BS, к которым они относятся. Книги попадают в
// bs_tools через расширение библиотеки (LibExt().Books, isBook), обложки
// сервер скачивает сам (internal/bookcovers).

//go:embed books/books.json
var booksJSON []byte

// Book: одна книга полки.
type Book struct {
	ID       string   `json:"id"`       // bk_<slug>
	RU       string   `json:"ru"`       // название русского издания
	Author   string   `json:"author"`   // автор по-русски
	Orig     string   `json:"orig"`     // оригинальное название ("" у книг на русском)
	AuthorEn string   `json:"authorEn"` // автор латиницей (поиск обложки)
	Year     int      `json:"year"`     // год первого издания
	Cat      string   `json:"cat"`      // категория полки
	Use      string   `json:"use"`      // чем полезна собственнику, 2 предложения
	Diag     []string `json:"diag"`     // id диагнозов BS
	Tools    []string `json:"tools"`    // id инструментов BS
}

// BookCats: категории полки по порядку и орган BS, к которому книга встаёт в библиотеке.
var BookCats = []struct{ Name, Organ string }{
	{"Стратегия", "Стратегия"}, {"Управление", "Процессы"}, {"Лидерство", "Команда"}, {"Команда и найм", "Команда"},
	{"Продажи", "Продажи"}, {"Маркетинг", "Маркетинг"}, {"Финансы", "Финансы"}, {"Переговоры", "Продажи"},
	{"Продуктивность", "Энергия"}, {"Мышление", "Мышление"}, {"Продукт и запуск", "Продукт"}, {"Истории компаний", "Стратегия"},
}

// BookOrgan: орган BS книги по её категории.
func BookOrgan(cat string) string {
	for _, c := range BookCats {
		if c.Name == cat {
			return c.Organ
		}
	}
	return "Стратегия"
}

// ToolTitle: название книги в библиотеке инструментов, как у книг, что уже были на платформе.
func (b Book) ToolTitle() string { return "Книга: «" + b.RU + "» · " + b.Author }

var (
	booksOnce sync.Once
	books     []Book
	booksErr  error
)

// Books: полка (разбирается один раз).
func Books() ([]Book, error) {
	booksOnce.Do(func() { booksErr = json.Unmarshal(booksJSON, &books) })
	return books, booksErr
}

// bookMatch: правило, по которому файл книги встаёт к ней по имени файла (как у прежних книг).
func bookMatch(b Book) string {
	var parts []string
	first := func(s string) string {
		s = strings.TrimSpace(strings.Split(s, ",")[0])
		f := strings.Fields(s)
		if len(f) == 0 {
			return ""
		}
		return strings.ToLower(f[len(f)-1])
	}
	if s := first(b.Author); len([]rune(s)) >= 4 {
		parts = append(parts, s)
	}
	if s := first(b.AuthorEn); len(s) >= 5 {
		parts = append(parts, s)
	}
	return strings.Join(parts, "|")
}

// BookToolItems: книги как карточки bs_tools (isBook), для расширения библиотеки.
func BookToolItems() []map[string]any {
	list, err := Books()
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, b := range list {
		out = append(out, map[string]any{
			"id": b.ID, "bookId": b.ID, "isBook": true, "organ": BookOrgan(b.Cat), "color": "#E8E8E8", "icon": "▤",
			"title": b.ToolTitle(), "short": b.Use, "why": b.Use, "author": b.Author, "origTitle": b.Orig, "year": b.Year,
			"bookCat": b.Cat, "diagIds": b.Diag, "toolIds": b.Tools, "match": bookMatch(b),
			"how": []string{}, "check": []string{}, "example": "", "time": "", "link": "", "video": "", "sample": "",
			"file": "", "fileName": "", "origin": "lib_ext",
		})
	}
	return out
}

var (
	cardTitlesOnce sync.Once
	cardTitles     map[string]string
)

// CardTitle: название диагноза или инструмента библиотеки по id ("" если нет).
func CardTitle(id string) string {
	cardTitlesOnce.Do(func() {
		cardTitles = map[string]string{}
		for _, f := range []struct {
			fs   interface{ ReadFile(string) ([]byte, error) }
			name string
		}{{libRichFS, "library_rich/diag.json"}, {libRichFS, "library_rich/tools.json"}, {libExtFS, "library_ext/diag.json"}, {libExtFS, "library_ext/tools.json"}} {
			b, err := f.fs.ReadFile(f.name)
			if err != nil {
				continue
			}
			var l []struct{ ID, Title string }
			if json.Unmarshal(b, &l) != nil {
				continue
			}
			for _, x := range l {
				if x.ID != "" && cardTitles[x.ID] == "" {
					cardTitles[x.ID] = x.Title
				}
			}
		}
	})
	return cardTitles[id]
}
