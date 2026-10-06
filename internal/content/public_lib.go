package content

import (
	"encoding/json"
	"sync"
)

// R49: открытая библиотека для поиска и ИИ-ассистентов (web/public_site.go).
// Берутся только встроенные карточки library_rich: пункты, которые сервер
// добавил во время работы (рекомендации), и правки команды из базы клуба на
// открытые страницы не попадают. Истории героев и кейсы (hero, case, example)
// сюда не читаются вовсе: открытые страницы показывают метод, признаки, шаги
// и метрики, но не истории с именами.

// PublicStep: шаг инструмента без примера из истории героя.
type PublicStep struct {
	Title   string `json:"title"`
	Do      string `json:"do"`
	Mistake string `json:"mistake"`
}

// PublicTest: самопроверка диагноза.
type PublicTest struct {
	Questions []struct {
		Q string `json:"q"`
	} `json:"questions"`
	Scale string `json:"scale"`
}

// PublicItem: диагноз (kind "diag") или инструмент (kind "tool").
type PublicItem struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Organ    string `json:"organ"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	// диагноз
	Desc       string      `json:"desc"`
	Signs      []string    `json:"signs"`
	Causes     []string    `json:"causes"`
	Cost       string      `json:"cost"`
	Test       *PublicTest `json:"test"`
	Cure       []string    `json:"cure"`
	FirstSteps []string    `json:"first_steps"`
	Risk       string      `json:"risk"`
	// инструмент
	Promise  string        `json:"promise"`
	When     []string      `json:"when"`
	Steps    []PublicStep  `json:"steps"`
	Metrics  []string      `json:"metrics"`
	Time     string        `json:"time"`
	Level    string        `json:"level"`
	Source   string        `json:"source"`
	Template *RichTemplate `json:"template"`
}

var (
	pubOnce  sync.Once
	pubItems []PublicItem
	pubErr   error
)

// PublicLibrary returns the embedded diagnoses and tools (diagnoses first),
// as written in library_rich, without heroes and cases.
func PublicLibrary() ([]PublicItem, error) {
	pubOnce.Do(func() {
		var d, t []PublicItem
		for _, f := range []struct {
			name string
			into *[]PublicItem
		}{{"library_rich/diag.json", &d}, {"library_rich/tools.json", &t}} {
			b, err := libRichFS.ReadFile(f.name)
			if err != nil {
				pubErr = err
				return
			}
			if err := json.Unmarshal(b, f.into); err != nil {
				pubErr = err
				return
			}
		}
		pubItems = append(d, t...)
	})
	return pubItems, pubErr
}

// LibRichRaw: one embedded library file as it is (tests of the open pages).
func LibRichRaw(name string) ([]byte, error) { return libRichFS.ReadFile(name) }
