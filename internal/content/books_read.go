package content

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// R82: «Сделай, чтобы каждую книгу можно было скачать и читать, и ссылка,
// если хочет купить бумажную версию». Клуб не раздаёт файлы книг под
// авторским правом. У каждой книги полки:
//   - файл, который загрузила команда («Файл книги»): «Скачать» и чтение на
//     платформе (страница);
//   - бесплатная легальная версия, если правообладатель её раздаёт сам
//     (BookFree);
//   - «Читать» в легальных магазинах электронных книг и «Купить бумажную»
//     в магазинах Казахстана (поиск по названию, страница);
//   - «Конспект BS: 5 идей и как применить» (books/konspekt.json): пересказ
//     своими словами, подготовлен с помощью ИИ по знаниям библиотеки и
//     помечен как конспект.

//go:embed books/konspekt.json
var konspektJSON []byte

// KonspektIdea: одна идея книги и как её применить.
type KonspektIdea struct {
	Idea  string `json:"idea"`
	Apply string `json:"apply"`
}

var (
	konspektOnce sync.Once
	konspekt     map[string][][2]string
	konspektErr  error
)

// BookKonspekt: конспект книги (nil, если его нет).
func BookKonspekt(id string) ([]KonspektIdea, error) {
	konspektOnce.Do(func() { konspektErr = json.Unmarshal(konspektJSON, &konspekt) })
	if konspektErr != nil {
		return nil, konspektErr
	}
	list := konspekt[id]
	if len(list) == 0 {
		return nil, nil
	}
	out := make([]KonspektIdea, 0, len(list))
	for _, x := range list {
		out = append(out, KonspektIdea{Idea: x[0], Apply: x[1]})
	}
	return out, nil
}

// BookLink: легальная бесплатная версия книги от правообладателя.
type BookLink struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// BookFree: книги, которые правообладатель раздаёт бесплатно сам (проверено
// 10.10.2026 на сайте правообладателя).
var BookFree = map[string]BookLink{
	// Эрик Йоргенсон: «free to read on this site», PDF и ePub бесплатно (английский оригинал)
	"bk_naval": {URL: "https://navalmanack.com/", Label: "Бесплатно от автора: navalmanack.com, на английском"},
}
