package content

import (
	"embed"
	"encoding/json"
	"io/fs"
	"sync"
)

// Расширение библиотеки клуба: новые диагнозы (bs_diag), инструменты
// (bs_tools) и вопросы (bs_questions). Сервер один раз на версию добавляет в
// документы клуба то, чего там ещё нет (handlers/http/library_ext.go).

// LibExtVersion: поднимать, когда в library_ext добавлены новые пункты.
const LibExtVersion = "2026-10-ext8" // Полка: книжная полка (books/books.json) в bs_tools

//go:embed library_ext/*.json
var libExtFS embed.FS

type LibExtSet struct {
	Version   string              `json:"version"`
	Diag      []map[string]any    `json:"diag"`
	Tools     []map[string]any    `json:"tools"`
	Questions map[string][]string `json:"questions"`
	// Полка: книги полки как карточки bs_tools (isBook); идут в bs_tools вместе с Tools
	Books []map[string]any `json:"books"`
}

var (
	libExtOnce sync.Once
	libExt     LibExtSet
	libExtErr  error
)

// LibExt returns the shipped extension (parsed once).
func LibExt() (LibExtSet, error) {
	libExtOnce.Do(func() {
		libExt.Version = LibExtVersion
		read := func(name string, v any) {
			if libExtErr != nil {
				return
			}
			b, err := libExtFS.ReadFile("library_ext/" + name)
			if err == nil {
				err = json.Unmarshal(b, v)
			}
			libExtErr = err
		}
		read("diag.json", &libExt.Diag)
		read("tools.json", &libExt.Tools)
		read("questions.json", &libExt.Questions)
		if libExtErr == nil {
			if _, libExtErr = Books(); libExtErr == nil {
				libExt.Books = BookToolItems()
			}
		}
	})
	return libExt, libExtErr
}

// LibraryFS: the content library files (library/*.json), for checks.
func LibraryFS() fs.FS { return libraryFS }
