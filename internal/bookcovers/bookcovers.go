// Package bookcovers: настоящие обложки книг полки клуба (книжная полка).
//
// Сервер сам находит обложку каждой книги и кладёт её в том (/data/bs-covers):
//
//  1. Google Books, русское издание (название и автор по-русски), крупная
//     картинка (fife=w800);
//  2. Open Library по оригинальному названию и автору (covers.openlibrary.org,
//     размер L);
//  3. Google Books, оригинальное издание; 4) мелкая картинка русского издания.
//
// Берётся первая крупная (ширина от 300 px), иначе самая крупная из годных.
// Картинка хранится как есть, без правок: обложка издательства показывается
// неизменной. Сервер только считает яркость её правого верхнего угла (там на
// карточке стоит значок BS: тёмная подложка на светлой обложке и наоборот)
// и средний цвет (фон, пока картинка грузится).
//
// Отдаётся с нашего домена: /covers/<id>.<ext>?v=<hash>, кэш браузера на год.
// Ограничения: файл до 3 МБ, все обложки до 150 МБ. Не найденная обложка
// ищется снова через 3 дня; на карточке вместо неё аккуратная типографская.
//
//	BOOK_COVERS=0  не скачивать (только то, что уже лежит в томе)
package bookcovers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

// Entry: обложка одной книги в индексе.
type Entry struct {
	File  string    `json:"file,omitempty"` // bk_x.jpg
	Src   string    `json:"src,omitempty"`  // gb_ru, ol, gb_en, gb_ru_s
	W     int       `json:"w,omitempty"`
	H     int       `json:"h,omitempty"`
	Lum   int       `json:"lum"`            // яркость правого верхнего угла, 0..255
	Tone  string    `json:"tone,omitempty"` // light | dark (угол)
	BG    string    `json:"bg,omitempty"`   // средний цвет #rrggbb
	Hash  string    `json:"hash,omitempty"`
	Size  int64     `json:"size,omitempty"`
	Miss  bool      `json:"miss,omitempty"` // не нашлась
	Tries int       `json:"tries,omitempty"`
	At    time.Time `json:"at"`
}

// Store: обложки в папке Dir.
type Store struct {
	Dir      string
	Client   *http.Client
	OLSearch string // https://openlibrary.org/search.json
	OLCovers string // https://covers.openlibrary.org
	GBooks   string // https://www.googleapis.com/books/v1/volumes
	MaxFile  int64
	MaxTotal int64
	Retry    time.Duration // через сколько искать не найденную снова
	Pause    time.Duration // между книгами (вежливость к API)

	mu   sync.RWMutex
	idx  map[string]*Entry
	busy sync.Mutex
}

// New: хранилище в dir с настройками по умолчанию.
func New(dir string) *Store {
	return &Store{
		Dir:      dir,
		Client:   &http.Client{Timeout: 25 * time.Second},
		OLSearch: "https://openlibrary.org/search.json",
		OLCovers: "https://covers.openlibrary.org",
		GBooks:   "https://www.googleapis.com/books/v1/volumes",
		MaxFile:  3 << 20,
		MaxTotal: 150 << 20,
		Retry:    72 * time.Hour,
		Pause:    400 * time.Millisecond,
	}
}

func (s *Store) indexPath() string { return filepath.Join(s.Dir, "index.json") }

// Load: индекс с диска (записи без файла на диске забываются).
func (s *Store) Load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idx = map[string]*Entry{}
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		return
	}
	var m map[string]*Entry
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for id, e := range m {
		if e == nil {
			continue
		}
		if e.File != "" {
			if st, err := os.Stat(filepath.Join(s.Dir, e.File)); err != nil || st.Size() == 0 {
				continue
			}
		}
		s.idx[id] = e
	}
}

func (s *Store) ensure() {
	s.mu.RLock()
	ok := s.idx != nil
	s.mu.RUnlock()
	if !ok {
		s.Load()
	}
}

func (s *Store) saveIndex() error {
	s.mu.RLock()
	b, err := json.MarshalIndent(s.idx, "", " ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return writeAtomic(s.indexPath(), b)
}

func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Get: обложка книги (ok=false: нет файла).
func (s *Store) Get(id string) (Entry, bool) {
	s.ensure()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.idx[id]
	if e == nil || e.File == "" {
		return Entry{}, false
	}
	return *e, true
}

// Stats: сколько книг с обложкой из total.
func (s *Store) Stats(ids []string) (found int) {
	for _, id := range ids {
		if _, ok := s.Get(id); ok {
			found++
		}
	}
	return found
}

func (s *Store) total() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int64
	for _, e := range s.idx {
		n += e.Size
	}
	return n
}

// Run: скачивает недостающие обложки; возвращает, сколько добавлено.
func (s *Store) Run(ctx context.Context, books []content.Book) (added int, err error) {
	if !s.busy.TryLock() {
		return 0, errors.New("already running")
	}
	defer s.busy.Unlock()
	s.ensure()
	for _, b := range books {
		if ctx.Err() != nil {
			return added, ctx.Err()
		}
		s.mu.RLock()
		e := s.idx[b.ID]
		s.mu.RUnlock()
		if e != nil && (e.File != "" || time.Since(e.At) < s.Retry) {
			continue
		}
		if s.total() > s.MaxTotal {
			log.Printf("book covers: storage limit %d MB reached", s.MaxTotal>>20)
			break
		}
		ne, ferr := s.fetchOne(ctx, b)
		if ferr != nil {
			log.Printf("book covers: %s: %v", b.ID, ferr)
		}
		if e != nil {
			ne.Tries = e.Tries
		}
		ne.Tries++
		s.mu.Lock()
		s.idx[b.ID] = ne
		s.mu.Unlock()
		if ne.File != "" {
			added++
		}
		_ = s.saveIndex()
		if s.Pause > 0 {
			select {
			case <-ctx.Done():
				return added, ctx.Err()
			case <-time.After(s.Pause):
			}
		}
	}
	return added, nil
}

type cand struct {
	src, url string
}

type got struct {
	src  string
	data []byte
	w, h int
	ext  string
}

// fetchOne ищет и скачивает обложку одной книги.
func (s *Store) fetchOne(ctx context.Context, b content.Book) (*Entry, error) {
	var best *got
	var lastErr error
	try := func(c cand, good int) bool {
		g, err := s.download(ctx, c)
		if err != nil {
			lastErr = err
			return false
		}
		if best == nil || g.w*g.h > best.w*best.h {
			best = g
		}
		return g.w >= good
	}
	steps := []func() []cand{
		func() []cand { return s.gbooks(ctx, b.RU, b.Author, true, true) },
		func() []cand {
			if b.Orig == "" {
				return s.olSearch(ctx, b.RU, "")
			}
			return s.olSearch(ctx, b.Orig, b.AuthorEn)
		},
		func() []cand {
			if b.Orig == "" {
				return nil
			}
			return s.gbooks(ctx, b.Orig, b.AuthorEn, false, true)
		},
		func() []cand { return s.gbooks(ctx, b.RU, b.Author, true, false) },
	}
	done := false
	for _, step := range steps {
		for _, c := range step() {
			if try(c, 300) {
				done = true
				break
			}
		}
		if done {
			break
		}
	}
	now := time.Now()
	if best == nil {
		if lastErr == nil {
			lastErr = errors.New("not found")
		}
		return &Entry{Miss: true, At: now}, lastErr
	}
	sum := sha256.Sum256(best.data)
	e := &Entry{File: b.ID + "." + best.ext, Src: best.src, W: best.w, H: best.h, Size: int64(len(best.data)),
		Hash: hex.EncodeToString(sum[:])[:12], At: now}
	e.Lum, e.BG = measure(best.data)
	e.Tone = "dark"
	if e.Lum >= 150 {
		e.Tone = "light"
	}
	if err := writeAtomic(filepath.Join(s.Dir, e.File), best.data); err != nil {
		return &Entry{Miss: true, At: now}, err
	}
	return e, nil
}

func (s *Store) get(ctx context.Context, u string, max int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "BSPlatform/1.0 (book shelf covers; app.bxclub.kz)")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s: HTTP %d", hostOf(u), resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(b)) > max {
		return nil, "", fmt.Errorf("%s: too big", hostOf(u))
	}
	return b, resp.Header.Get("Content-Type"), nil
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return "?"
}

// download: картинка кандидата, проверенная как обложка книги.
func (s *Store) download(ctx context.Context, c cand) (*got, error) {
	b, _, err := s.get(ctx, c.url, s.MaxFile)
	if err != nil {
		return nil, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: not an image", c.src)
	}
	if cfg.Width < 90 || cfg.Height < 120 {
		return nil, fmt.Errorf("%s: too small %dx%d", c.src, cfg.Width, cfg.Height)
	}
	if r := float64(cfg.Height) / float64(cfg.Width); r < 1.12 || r > 1.95 {
		return nil, fmt.Errorf("%s: not a book shape %dx%d", c.src, cfg.Width, cfg.Height)
	}
	ext := map[string]string{"jpeg": "jpg", "png": "png", "gif": "gif"}[format]
	if ext == "" {
		return nil, fmt.Errorf("%s: format %s", c.src, format)
	}
	return &got{src: c.src, data: b, w: cfg.Width, h: cfg.Height, ext: ext}, nil
}

// measure: яркость правого верхнего угла (30% ширины, 16% высоты) и средний цвет.
func measure(b []byte) (int, string) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return 0, "#1a1a1a"
	}
	r := img.Bounds()
	w, h := r.Dx(), r.Dy()
	lum := func(x0, y0, x1, y1, step int) (float64, [3]float64) {
		var sum float64
		var c [3]float64
		n := 0.0
		for y := y0; y < y1; y += step {
			for x := x0; x < x1; x += step {
				cr, cg, cb, _ := img.At(r.Min.X+x, r.Min.Y+y).RGBA()
				R, G, B := float64(cr>>8), float64(cg>>8), float64(cb>>8)
				sum += 0.2126*R + 0.7152*G + 0.0722*B
				c[0], c[1], c[2] = c[0]+R, c[1]+G, c[2]+B
				n++
			}
		}
		if n == 0 {
			return 0, c
		}
		return sum / n, [3]float64{c[0] / n, c[1] / n, c[2] / n}
	}
	step := 1 + w/200
	corner, _ := lum(w*7/10, 0, w, max(1, h*16/100), step)
	_, avg := lum(0, 0, w, h, step*2)
	return int(corner + 0.5), fmt.Sprintf("#%02x%02x%02x", int(avg[0]), int(avg[1]), int(avg[2]))
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// norm: название для сравнения (регистр, ё, знаки не важны).
func norm(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	return strings.TrimSpace(nonWord.ReplaceAllString(s, " "))
}

// titleFits: найденное название похоже на искомое (начинается с него или содержит его первые слова).
func titleFits(found, want string) bool {
	f, w := norm(found), norm(want)
	if f == "" || w == "" {
		return false
	}
	if strings.HasPrefix(f, w) || strings.HasPrefix(w, f) || strings.Contains(f, w) {
		return true
	}
	ws := strings.Fields(w)
	if len(ws) > 3 {
		ws = ws[:3]
	}
	return strings.HasPrefix(f, strings.Join(ws, " "))
}

func surname(author string) string {
	a := strings.TrimSpace(strings.Split(author, ",")[0])
	f := strings.Fields(a)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimFunc(f[len(f)-1], func(r rune) bool { return !unicode.IsLetter(r) })
}

func authorFits(found []string, want string) bool {
	sn := norm(surname(want))
	if sn == "" {
		return true
	}
	for _, a := range found {
		if strings.Contains(norm(a), sn) {
			return true
		}
	}
	return false
}

// olSearch: обложки Open Library по названию и автору, самые издаваемые первыми.
func (s *Store) olSearch(ctx context.Context, title, author string) []cand {
	q := url.Values{}
	q.Set("title", title)
	if author != "" {
		q.Set("author", surname(author))
	}
	q.Set("limit", "8")
	q.Set("fields", "title,author_name,cover_i,edition_count")
	b, _, err := s.get(ctx, s.OLSearch+"?"+q.Encode(), 4<<20)
	if err != nil {
		return nil
	}
	var r struct {
		Docs []struct {
			Title   string   `json:"title"`
			Authors []string `json:"author_name"`
			Cover   int64    `json:"cover_i"`
			Eds     int      `json:"edition_count"`
		} `json:"docs"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	docs := r.Docs[:0]
	for _, d := range r.Docs {
		if d.Cover > 0 && titleFits(d.Title, title) && (author == "" || authorFits(d.Authors, author)) {
			docs = append(docs, d)
		}
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Eds > docs[j].Eds })
	var out []cand
	for i, d := range docs {
		if i >= 2 {
			break
		}
		out = append(out, cand{"ol", fmt.Sprintf("%s/b/id/%d-L.jpg?default=false", s.OLCovers, d.Cover)})
	}
	return out
}

// gbooks: обложки Google Books; ru: только русские издания; big: крупный размер.
func (s *Store) gbooks(ctx context.Context, title, author string, ru, big bool) []cand {
	q := url.Values{}
	qs := "intitle:" + title
	if sn := surname(author); sn != "" {
		qs += " inauthor:" + sn
	}
	q.Set("q", qs)
	q.Set("maxResults", "10")
	q.Set("printType", "books")
	if ru {
		q.Set("langRestrict", "ru")
	}
	b, _, err := s.get(ctx, s.GBooks+"?"+q.Encode(), 4<<20)
	if err != nil {
		return nil
	}
	var r struct {
		Items []struct {
			Info struct {
				Title    string   `json:"title"`
				Authors  []string `json:"authors"`
				Language string   `json:"language"`
				Images   struct {
					Thumb string `json:"thumbnail"`
				} `json:"imageLinks"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	var out []cand
	src := "gb_en"
	if ru {
		src = "gb_ru"
		if !big {
			src = "gb_ru_s"
		}
	}
	for _, it := range r.Items {
		in := it.Info
		if in.Images.Thumb == "" || !titleFits(in.Title, title) || !authorFits(in.Authors, author) {
			continue
		}
		if ru && in.Language != "" && in.Language != "ru" {
			continue
		}
		out = append(out, cand{src, gbImage(in.Images.Thumb, big)})
		if len(out) >= 2 {
			break
		}
	}
	return out
}

// gbImage: адрес картинки Google Books по https, без загнутого угла, крупно при big.
func gbImage(u string, big bool) string {
	u = strings.Replace(u, "http://", "https://", 1)
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := p.Query()
	q.Del("edge")
	if big {
		q.Set("fife", "w800")
	} else {
		q.Del("fife")
	}
	p.RawQuery = q.Encode()
	return p.String()
}

var fileRe = regexp.MustCompile(`^bk_[a-z0-9_]{1,60}\.(jpg|png|gif)$`)

// Path: файл обложки по опубликованному имени ("" если нет такого).
func (s *Store) Path(name string) string {
	if !fileRe.MatchString(name) {
		return ""
	}
	p := filepath.Join(s.Dir, name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// URL: опубликованный адрес обложки книги ("" если обложки нет).
func (s *Store) URL(id string) string {
	e, ok := s.Get(id)
	if !ok {
		return ""
	}
	return "/covers/" + e.File + "?v=" + e.Hash
}
