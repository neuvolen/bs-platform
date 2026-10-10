package http

import (
	"context"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bookcovers"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/bnursik/business_surgery_backend/internal/datadir"
	"github.com/gin-gonic/gin"
)

// Полка: книжная полка клуба. Книги (content.Books) входят в bs_tools через
// расширение библиотеки, настоящие обложки сервер скачивает в том
// (internal/bookcovers) и отдаёт с нашего домена.
//
//	GET /covers/<id>.<ext>          обложка, кэш на год (адрес с ?v=<hash>)
//	GET /api/v1/platform/books      полка: книги, обложки, тон угла для значка BS

var (
	bookStoreOnce sync.Once
	bookStore     *bookcovers.Store
)

// BookCovers: хранилище обложек (в томе: bs-covers/).
func BookCovers() *bookcovers.Store {
	bookStoreOnce.Do(func() { bookStore = bookcovers.New(datadir.Path("bs-covers")) })
	return bookStore
}

// SetBookCovers: другое хранилище (тесты).
func SetBookCovers(s *bookcovers.Store) {
	bookStoreOnce.Do(func() {})
	bookStore = s
}

// BookCoversLoop: скачивает недостающие обложки после старта и дважды в сутки
// (не найденные ищутся снова раз в 3 дня).
func BookCoversLoop(ctx context.Context) {
	if os.Getenv("BOOK_COVERS") == "0" || testing.Testing() {
		return
	}
	t := time.NewTimer(40 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		runBookCovers(ctx)
		t.Reset(12 * time.Hour)
	}
}

func runBookCovers(ctx context.Context) {
	books, err := content.Books()
	if err != nil {
		log.Printf("book covers: %v", err)
		return
	}
	st := BookCovers()
	c, cancel := context.WithTimeout(ctx, 2*time.Hour)
	added, err := st.Run(c, books)
	cancel()
	ids := make([]string, len(books))
	src := map[string]int{}
	for i, b := range books {
		ids[i] = b.ID
		if e, ok := st.Get(b.ID); ok {
			src[e.Src]++
		}
	}
	found := st.Stats(ids)
	var parts []string
	for k, v := range src {
		parts = append(parts, k+"="+strconv.Itoa(v))
	}
	sort.Strings(parts)
	pct := 0
	if len(books) > 0 {
		pct = found * 100 / len(books)
	}
	log.Printf("book covers: %d/%d books with a real cover (%d%%), +%d this run, sources %s, err=%v",
		found, len(books), pct, added, strings.Join(parts, " "), err)
}

// BookCoverFile: GET /covers/:file, открыто (картинки обложек), кэш на год.
func BookCoverFile(c *gin.Context) {
	p := BookCovers().Path(c.Param("file"))
	if p == "" {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(p)
}

type shelfBook struct {
	content.Book
	Title string `json:"title"` // как в bs_tools: «Книга: «…» · Автор»
	Cover string `json:"cover,omitempty"`
	Tone  string `json:"tone,omitempty"` // light | dark: угол обложки под значком BS
	BG    string `json:"bg,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
	// названия диагнозов и инструментов, к которым книга относится
	DiagT []string `json:"diagT"`
	ToolT []string `json:"toolT"`
	// R82: бесплатная версия от правообладателя; есть ли «Конспект BS»
	Free     *content.BookLink `json:"free,omitempty"`
	Konspekt bool              `json:"konspekt,omitempty"`
}

// BookShelf: GET /api/v1/platform/books.
func BookShelf(c *gin.Context) {
	books, err := content.Books()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	st := BookCovers()
	out := make([]shelfBook, 0, len(books))
	found := 0
	for _, b := range books {
		sb := shelfBook{Book: b, Title: b.ToolTitle(), DiagT: []string{}, ToolT: []string{}}
		for _, id := range b.Diag {
			sb.DiagT = append(sb.DiagT, content.CardTitle(id))
		}
		for _, id := range b.Tools {
			sb.ToolT = append(sb.ToolT, content.CardTitle(id))
		}
		if f, ok := content.BookFree[b.ID]; ok {
			f := f
			sb.Free = &f
		}
		if k, _ := content.BookKonspekt(b.ID); len(k) > 0 {
			sb.Konspekt = true
		}
		if e, ok := st.Get(b.ID); ok {
			sb.Cover, sb.Tone, sb.BG, sb.W, sb.H = st.URL(b.ID), e.Tone, e.BG, e.W, e.H
			found++
		}
		out = append(out, sb)
	}
	cats := make([]string, 0, len(content.BookCats))
	for _, x := range content.BookCats {
		cats = append(cats, x.Name)
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.JSON(http.StatusOK, gin.H{"books": out, "cats": cats, "covers": found, "total": len(books)})
}

// BookKonspekt: GET /api/v1/platform/books/konspekt?id=bk_… «Конспект BS: 5
// идей и как применить» (R82), по запросу, чтобы полка грузилась быстро.
func BookKonspekt(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	list, err := content.BookKonspekt(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(list) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no_konspekt"})
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.JSON(http.StatusOK, gin.H{"id": id, "ideas": list, "label": "Конспект BS: пересказ идей книги своими словами, подготовлен с помощью ИИ"})
}
