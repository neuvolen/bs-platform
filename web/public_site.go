package web

// R49: открытая часть app.bxclub.kz для поиска и ИИ-ассистентов (ChatGPT,
// Claude, Perplexity, Gemini, Google AI Overviews).
//
//   /robots.txt      поисковые и ИИ-краулеры видят открытые страницы, платформа и API закрыты
//   /sitemap.xml     все открытые страницы
//   /llms.txt        короткое описание клуба со ссылками (формат llmstxt.org)
//   /llms-full.txt   полное описание, FAQ и библиотека одним текстом
//   /about           страница клуба: факты, метод, цены, основатели, FAQ, JSON-LD
//   /club            301 на /about
//   /library         открытая библиотека: диагнозы и инструменты по органам бизнеса
//   /library/<slug>  одна карточка, отрисованная на сервере
//   /site/<file>     картинка для соцсетей и логотип
//
// Страницы лёгкие: без скриптов (только JSON-LD), стили внутри страницы,
// шрифт клуба из /a/ (общий с платформой). Каждая страница собирается один раз
// при первом запросе и отдаётся сжатой (brotli или gzip) с ETag.
//
// Факты о клубе (цены, основатели, контакты) живут только в bsProfile ниже:
// страница, JSON-LD, llms.txt и тексты для Tilda берут их отсюда, чтобы
// ИИ везде видел одни и те же цифры.

import (
	"embed"
	"encoding/json"
	"html"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

//go:embed site
var siteFS embed.FS

// SiteURL: адрес открытых страниц (canonical, sitemap, JSON-LD).
var SiteURL = func() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_SITE_URL")), "/"); v != "" {
		return v
	}
	return "https://app.bxclub.kz"
}()

// siteUpdated: дата последней правки фактов о клубе (dateModified, lastmod).
const siteUpdated = "2026-10-06"

// ── Факты о клубе ──

type bsOffer struct {
	ID, Name, Desc string
	Price          int // ₸
}

type bsPerson struct {
	ID, Name, Latin, Role, Bio string
}

type bsFAQ struct{ Q, A string }

type bsOrgan struct{ Name, What string }

// bsAddress: the club's address (Dostyk Hub coworking, Dostyk avenue 44, Almaty).
const bsAddress = "Алматы, проспект Достык, 44, Dostyk Hub"

var bsProfile = struct {
	Name, Short, Slogan, Lead, City, Country   string
	Venue, Street, Address                     string // where the club meets (schema.org PostalAddress)
	MainSite, Bot, BotHandle, Channel, WAPhone string
	WALink, Instagram, Threads, YouTube        string
	Alt                                        []string
	Offers                                     []bsOffer
	Founders                                   []bsPerson
	Organs                                     []bsOrgan
	Cycle                                      []string
	FAQ                                        []bsFAQ
	Facts                                      [][2]string
}{
	Name:      "Business Surgery",
	Short:     "BS",
	Slogan:    "Точная работа с бизнесом на уровне его ДНК",
	Lead:      "Business Surgery (BS): клуб бизнес-трекинга в Алматы для собственников малого и среднего бизнеса. Каждые 10 дней два основателя клуба разбирают бизнес резидента: ставят диагноз по 7 органам бизнеса, выбирают стратегию на цикл и составляют план задач. Между разборами резидент каждый день отчитывается о выполнении.",
	City:      "Алматы",
	Country:   "Казахстан",
	Venue:     "Dostyk Hub",
	Street:    "пр. Достык, 44",
	Address:   bsAddress,
	MainSite:  "https://bxclub.kz",
	Bot:       "https://t.me/bsurgery_bot",
	BotHandle: "@bsurgery_bot",
	Channel:   "https://t.me/bsurgery_kz",
	WAPhone:   "+7 702 403 50 36",
	WALink:    "https://wa.me/77024035036",
	Instagram: "https://www.instagram.com/business.surgery/",
	Threads:   "https://www.threads.net/@business.surgery",
	YouTube:   "https://www.youtube.com/@Businessurgery",
	Alt:       []string{"BS", "BS Club", "Business Surgery Club", "Бизнес-клуб Business Surgery"},
	Offers: []bsOffer{
		{"express", "Экспресс-разбор бизнеса", "Встреча на 1 час с обоими основателями клуба: диагноз, главная причина проблемы и первые шаги. Точка входа в клуб.", 50000},
		{"res3", "Резидентство на 3 месяца", "Разбор каждые 10 дней, план задач на цикл, ежедневная отчётность, группа из 5 резидентов, доска резидента на платформе.", 500000},
		{"res12", "Резидентство на 12 месяцев", "Тот же формат резидентства на год.", 1500000},
	},
	Founders: []bsPerson{
		{"bereke", "Береке Ерниязов", "Bereke Yerniyazov", "Сооснователь Business Surgery", "Предприниматель, ментор и инвестор. Больше 10 лет в бизнесе, опыт в 20+ нишах."},
		{"rustam", "Рустам Кабден", "Rustam Kabden", "Сооснователь Business Surgery, бизнес-трекер", "Бизнес-трекер, выпускник акселераторов Seedstars и SmartCity."},
	},
	Organs: []bsOrgan{
		{"Стратегия", "цель с цифрой и датой, фокус, от чего отказаться"},
		{"Маркетинг", "откуда приходят клиенты и сколько стоит каждый"},
		{"Продажи", "как заявка превращается в деньги и кто продаёт"},
		{"Команда", "роли, найм, кто принимает решения без собственника"},
		{"Финансы", "управленческий учёт, прибыль, платёжный календарь"},
		{"Процессы", "регламенты: работа идёт без ручного управления"},
		{"Аналитика", "цифры, по которым принимаются решения"},
	},
	Cycle: []string{
		"Разбор: диагноз по 7 органам бизнеса и главная причина, которая тормозит рост.",
		"Стратегия и план: что меняем за 10 дней, задачи с ответственными и сроками.",
		"Ежедневный отчёт в Telegram-группе. Пропущенный отчёт стоит 10 000 ₸ штрафа.",
		"Через 10 дней следующий разбор: что сделано, что изменилось в цифрах, новый план.",
	},
	Facts: [][2]string{
		{"Что это", "Клуб бизнес-трекинга для собственников малого и среднего бизнеса"},
		{"Город", "Алматы, Казахстан"},
		{"Адрес", bsAddress},
		{"Формат", "Разбор каждые 10 дней, ежедневная отчётность, группы по 5 резидентов"},
		{"Метод", "Бизнес как живой организм: диагноз по 7 органам бизнеса"},
		{"Основатели", "Береке Ерниязов и Рустам Кабден, оба на каждом разборе"},
		{"Вход", "Экспресс-разбор, 1 час, 50 000 ₸"},
		{"Для кого", "Собственники с чистой прибылью от 2 до 20 млн ₸ в месяц"},
		{"По данным клуба", "Более 700 проведённых разборов, около 20 резидентов"},
	},
	FAQ: []bsFAQ{
		{"Что такое Business Surgery?", "Business Surgery (BS): клуб бизнес-трекинга в Алматы для собственников малого и среднего бизнеса. Каждые 10 дней основатели клуба разбирают бизнес резидента: ставят диагноз, выбирают стратегию на цикл и составляют план задач. Между разборами резидент каждый день отчитывается о выполнении."},
		{"Чем клуб отличается от курса или бизнес-школы?", "В клубе нет уроков и общей программы. Резидент работает над задачами своего бизнеса: получает диагноз, план на 10 дней и проверку результата на следующем разборе. Ежедневный отчёт и штраф 10 000 ₸ за пропуск держат темп внедрения."},
		{"Чем BS отличается от нетворкинг-клуба?", "Окружение в клубе есть: резиденты объединены в группы по 5 человек. Основа работы при этом трекинг: разбор каждые 10 дней, план задач и ежедневная отчётность."},
		{"Кому подходит клуб?", "Собственникам действующего бизнеса, которые упёрлись в потолок: бизнес держится на владельце, прибыль годами на одном уровне, команда не принимает решений сама, знания из курсов не внедряются. Основная аудитория клуба: собственники с чистой прибылью от 2 до 20 млн ₸ в месяц."},
		{"Кому клуб не подойдёт?", "Тем, кто ищет курс с уроками или разовую консультацию без внедрения, и тем, у кого нет нескольких минут в день на отчёт о выполнении задач."},
		{"Сколько стоит участие?", "Экспресс-разбор: 50 000 ₸. Резидентство на 3 месяца: 500 000 ₸. Резидентство на 12 месяцев: 1 500 000 ₸. Актуальные условия уточняйте при записи."},
		{"Что происходит на экспресс-разборе?", "Встреча длится 1 час, на ней присутствуют оба основателя клуба. Вы рассказываете о бизнесе и цифрах, основатели определяют, какой орган бизнеса болит, в чём причина и с чего начать. После разбора вы сами решаете, идти ли в клуб."},
		{"Как устроен 10-дневный цикл?", "Цикл начинается с разбора: диагноз, стратегия на 10 дней и план задач. Дальше резидент каждый день отправляет короткий отчёт в Telegram-группу, за пропущенный отчёт штраф 10 000 ₸. Через 10 дней следующий разбор: что сделано, что изменилось в цифрах, какой план дальше."},
		{"Что значит «7 органов бизнеса»?", "Метод клуба рассматривает бизнес как живой организм из семи органов: стратегия, маркетинг, продажи, команда, финансы, процессы и аналитика. На разборе находят самый слабый орган и причину его проблем, с них и начинается план."},
		{"Кто ведёт разборы?", "Основатели клуба Береке Ерниязов и Рустам Кабден. На каждом разборе присутствуют оба."},
		{"Где проходят встречи и можно ли участвовать из другого города?", "Клуб базируется в Алматы, встречи проходят по адресу: проспект Достык, 44, Dostyk Hub. Ежедневная отчётность идёт в Telegram, задачи и прогресс резидента собраны на платформе app.bxclub.kz и в Telegram-приложении. Формат участия из другого города уточняйте при записи."},
		{"Есть ли бесплатные материалы?", "Да. В Telegram-боте @bsurgery_bot можно бесплатно получить 99 чек-листов и пройти диагностику бизнеса. На app.bxclub.kz/library открыта библиотека диагнозов и инструментов клуба."},
		{"Сколько разборов провёл клуб?", "По данным клуба, основатели провели более 700 разборов бизнеса. В клубе около 20 резидентов."},
		{"Гарантирует ли клуб рост прибыли?", "Нет. Клуб даёт диагноз, план и ежедневный контроль. Результат зависит от того, насколько последовательно собственник выполняет план."},
		{"Как записаться?", "Напишите в WhatsApp +7 702 403 50 36 или в Telegram-бот @bsurgery_bot. Первый шаг всегда один: экспресс-разбор."},
	},
}

// bsEvent: ближайшее открытое мероприятие (бизнес-завтрак). Только из
// переменной BS_PUBLIC_EVENT (JSON), чтобы на странице не было выдуманной даты:
// {"name":"Бизнес-завтрак BS","start":"2026-10-24T09:00+05:00","end":"2026-10-24T11:00+05:00",
//
//	"place":"Название места","address":"улица, дом, Алматы","price":0,"url":"https://..."}
type bsEvent struct {
	Name    string `json:"name"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Place   string `json:"place"`
	Address string `json:"address"`
	Price   int    `json:"price"`
	URL     string `json:"url"`
	Desc    string `json:"desc"`
}

func publicEvent() *bsEvent {
	raw := strings.TrimSpace(os.Getenv("BS_PUBLIC_EVENT"))
	if raw == "" {
		return nil
	}
	var e bsEvent
	if err := json.Unmarshal([]byte(raw), &e); err != nil || e.Name == "" || e.Start == "" || e.Place == "" {
		log.Printf("web: BS_PUBLIC_EVENT skipped: needs name, start and place")
		return nil
	}
	return &e
}

// ── Библиотека ──

type libEntry struct {
	content.PublicItem
	Slug string
}

type libIndex struct {
	items   []libEntry
	bySlug  map[string]int
	byTitle map[string]int   // tool title (lower) → index
	helps   map[string][]int // tool slug → diagnoses that list it in cure
	organs  []string         // in the club's order
	byOrgan map[string][]int
}

var (
	libOnce sync.Once
	lib     libIndex
)

// organOrder: the club's 7 organs first, then the rest of the library's sections.
var organOrder = []string{"Стратегия", "Маркетинг", "Продажи", "Команда", "Финансы", "Процессы", "Аналитика", "Продукт", "Мышление", "Цели", "Энергия", "Окружение"}

func publicLib() *libIndex {
	libOnce.Do(func() {
		items, err := content.PublicLibrary()
		if err != nil {
			log.Printf("web: public library: %v", err)
		}
		lib = libIndex{bySlug: map[string]int{}, byTitle: map[string]int{}, helps: map[string][]int{}, byOrgan: map[string][]int{}}
		seen := map[string]bool{}
		for _, it := range items {
			s := slugify(it.Title)
			if s == "" {
				s = slugify(it.ID)
			}
			base := s
			for n := 2; seen[s]; n++ {
				s = base + "-" + strconv.Itoa(n)
			}
			seen[s] = true
			lib.bySlug[s] = len(lib.items)
			lib.items = append(lib.items, libEntry{PublicItem: it, Slug: s})
		}
		for i, e := range lib.items {
			if e.Kind == "tool" {
				lib.byTitle[strings.ToLower(e.Title)] = i
			}
			lib.byOrgan[e.Organ] = append(lib.byOrgan[e.Organ], i)
		}
		for i, e := range lib.items {
			if e.Kind != "diag" {
				continue
			}
			for _, c := range e.Cure {
				if j, ok := lib.byTitle[strings.ToLower(c)]; ok {
					lib.helps[lib.items[j].Slug] = append(lib.helps[lib.items[j].Slug], i)
				}
			}
		}
		known := map[string]bool{}
		for _, o := range organOrder {
			if len(lib.byOrgan[o]) > 0 {
				lib.organs = append(lib.organs, o)
				known[o] = true
			}
		}
		var rest []string
		for o := range lib.byOrgan {
			if !known[o] {
				rest = append(rest, o)
			}
		}
		sort.Strings(rest)
		lib.organs = append(lib.organs, rest...)
	})
	return &lib
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ә': "a", 'ғ': "g", 'қ': "k", 'ң': "n", 'ө': "o", 'ұ': "u", 'ү': "u", 'һ': "h", 'і': "i",
}

// slugify: «Кассовые разрывы» → kassovye-razryvy.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if t, ok := translit[r]; ok {
			b.WriteString(t)
			dash = false
			continue
		}
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 70 {
		out = strings.TrimRight(out[:70], "-")
	}
	return out
}

func libURL(slug string) string { return SiteURL + "/library/" + slug }

// ── Страницы: сборка и кэш ──

var (
	pubMu    sync.Mutex
	pubPages = map[string]page{}
)

func cachedPage(key string, make func() string) page {
	pubMu.Lock()
	defer pubMu.Unlock()
	if p, ok := pubPages[key]; ok {
		return p
	}
	// brotli 9: the first visit of a card is not kept waiting (11 is for the big platform page)
	plain := []byte(make())
	sum := contentHash(plain)
	p := page{plain: plain, gz: gzipBytes(plain), br: brotliBytes(plain, 9), hash: sum, etag: `"` + sum + `"`}
	pubPages[key] = p
	return p
}

func servePublic(c *gin.Context, ctype string, p page) {
	h := c.Writer.Header()
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy", "upgrade-insecure-requests") // R52: as the platform page
	sendBytes(c, ctype, "public, max-age=3600", p.hash, p.plain, p.gz, p.br)
}

// PublicCase: a published resident case on /about (R51, handlers/http/sales_cases.go).
type PublicCase struct {
	Title   string
	Who     string
	Metrics []string
	Story   string
}

var (
	pubCasesMu sync.RWMutex
	pubCases   []PublicCase
)

// PublicCases: the cases /about shows now.
func PublicCases() []PublicCase {
	pubCasesMu.RLock()
	defer pubCasesMu.RUnlock()
	return pubCases
}

// SetPublicCases replaces the cases and drops the cached /about.
func SetPublicCases(list []PublicCase) {
	pubCasesMu.Lock()
	if len(list) > 12 {
		list = list[:12]
	}
	pubCases = list
	pubCasesMu.Unlock()
	pubMu.Lock()
	delete(pubPages, "about")
	pubMu.Unlock()
}

// RegisterPublic mounts the open pages (called from Register).
func RegisterPublic(r *gin.Engine) {
	text := func(key, ctype string, make func() string) gin.HandlerFunc {
		return func(c *gin.Context) { servePublic(c, ctype, cachedPage(key, make)) }
	}
	routes := map[string]gin.HandlerFunc{
		"/robots.txt":    text("robots", "text/plain; charset=utf-8", robotsTxt),
		"/sitemap.xml":   text("sitemap", "application/xml; charset=utf-8", sitemapXML),
		"/llms.txt":      text("llms", "text/plain; charset=utf-8", llmsTxt),
		"/llms-full.txt": text("llmsfull", "text/plain; charset=utf-8", llmsFullTxt),
		"/about":         text("about", "text/html; charset=utf-8", aboutHTML),
		"/library":       text("library", "text/html; charset=utf-8", libraryHTML),
		"/library/:slug": func(c *gin.Context) {
			l := publicLib()
			slug := c.Param("slug")
			i, ok := l.bySlug[slug]
			if !ok {
				c.Header("X-Robots-Tag", "noindex")
				c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte(notFoundHTML()))
				return
			}
			servePublic(c, "text/html; charset=utf-8", cachedPage("lib/"+slug, func() string { return libItemHTML(l, i) }))
		},
		"/site/:file": serveSiteFile,
	}
	for p, h := range routes {
		r.GET(p, h)
		r.HEAD(p, h)
	}
	club := func(c *gin.Context) { c.Redirect(http.StatusMovedPermanently, "/about") }
	r.GET("/club", club)
	r.HEAD("/club", club)
}

func serveSiteFile(c *gin.Context) {
	name := c.Param("file")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		c.Status(http.StatusNotFound)
		return
	}
	b, err := siteFS.ReadFile("site/" + name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	ct := "application/octet-stream"
	switch path.Ext(name) {
	case ".png":
		ct = "image/png"
	case ".svg":
		ct = "image/svg+xml"
	}
	sendBytes(c, ct, "public, max-age=604800", contentHash(b), b, nil, nil)
}

// ── robots.txt ──

// Боты, которым открыта публичная часть. У бота со своим разделом общий
// раздел «*» не действует, поэтому правила повторяются в каждом разделе.
var aiAgents = []string{
	// поиск и ответы ИИ
	"OAI-SearchBot", "ChatGPT-User", "Claude-SearchBot", "Claude-User", "PerplexityBot", "Perplexity-User",
	"DuckAssistBot", "MistralAI-User",
	// обучение моделей: чтобы модели знали клуб, им тоже открыты публичные страницы
	"GPTBot", "ClaudeBot", "Google-Extended", "Applebot-Extended", "CCBot", "meta-externalagent",
	// поисковики (Bing питает поиск ChatGPT и Copilot, Яндекс и Google ищут в Казахстане)
	"Googlebot", "Bingbot", "YandexBot", "Applebot",
}

var robotsClosed = []string{"/api/", "/platform", "/dl/", "/voice/", "/swagger/", "/tts/"}

func robotsRules() string {
	var b strings.Builder
	b.WriteString("Allow: /about\nAllow: /library\nAllow: /llms.txt\nAllow: /llms-full.txt\nAllow: /site/\n")
	for _, p := range robotsClosed {
		b.WriteString("Disallow: " + p + "\n")
	}
	return b.String()
}

func robotsTxt() string {
	var b strings.Builder
	b.WriteString("# Business Surgery, app.bxclub.kz\n")
	b.WriteString("# Открыты: страница клуба, библиотека, llms.txt. Закрыты: платформа резидентов и API.\n\n")
	rules := robotsRules()
	for _, a := range aiAgents {
		b.WriteString("User-agent: " + a + "\n" + rules + "\n")
	}
	b.WriteString("User-agent: *\n" + rules + "\n")
	b.WriteString("Sitemap: " + SiteURL + "/sitemap.xml\n")
	return b.String()
}

// ── sitemap.xml ──

func sitemapXML() string {
	l := publicLib()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	add := func(loc, pri string) {
		b.WriteString("  <url><loc>" + html.EscapeString(loc) + "</loc><lastmod>" + siteUpdated + "</lastmod><priority>" + pri + "</priority></url>\n")
	}
	add(SiteURL+"/about", "1.0")
	add(SiteURL+"/library", "0.8")
	for _, e := range l.items {
		add(libURL(e.Slug), "0.6")
	}
	b.WriteString("</urlset>\n")
	return b.String()
}

// ── llms.txt ──

func price(n int) string {
	s := strconv.Itoa(n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, " ") + " ₸"
}

func llmsHead() string {
	p := bsProfile
	var b strings.Builder
	b.WriteString("# " + p.Name + " (" + p.Short + ")\n\n")
	b.WriteString("> " + p.Lead + "\n\n")
	b.WriteString("Ключевые факты:\n")
	for _, f := range p.Facts {
		b.WriteString("- " + f[0] + ": " + f[1] + "\n")
	}
	b.WriteString("\nЦены:\n")
	for _, o := range p.Offers {
		b.WriteString("- " + o.Name + ": " + price(o.Price) + ". " + o.Desc + "\n")
	}
	b.WriteString("\nОснователи:\n")
	for _, f := range p.Founders {
		b.WriteString("- " + f.Name + " (" + f.Latin + "): " + f.Role + ". " + f.Bio + "\n")
	}
	b.WriteString("\nКонтакты: WhatsApp " + p.WAPhone + " (" + p.WALink + "), Telegram-бот " + p.BotHandle + " (" + p.Bot + "), Telegram-канал " + p.Channel +
		", Instagram и Threads @business.surgery, YouTube " + p.YouTube + ", сайт " + p.MainSite + ".\n")
	b.WriteString("Название пишется латиницей: Business Surgery, сокращённо BS. Клуб в Алматы (Казахстан); не путать с медицинской хирургией и с англоязычными курсами «business of surgery».\n")
	return b.String()
}

func llmsTxt() string {
	l := publicLib()
	var b strings.Builder
	b.WriteString(llmsHead())
	b.WriteString("\n## Главное\n\n")
	b.WriteString("- [О клубе Business Surgery](" + SiteURL + "/about): метод, 10-дневный цикл, цены, основатели, 15 ответов на частые вопросы\n")
	b.WriteString("- [Полное описание для ИИ](" + SiteURL + "/llms-full.txt): всё из этого файла, FAQ и вся открытая библиотека одним текстом\n")
	b.WriteString("- [Сайт клуба](" + bsProfile.MainSite + "): запись на экспресс-разбор\n")
	b.WriteString("- [Telegram-бот](" + bsProfile.Bot + "): 99 бесплатных чек-листов и диагностика бизнеса\n")
	b.WriteString("\n## Открытая библиотека: диагнозы бизнеса\n\n")
	for _, e := range l.items {
		if e.Kind == "diag" {
			b.WriteString("- [" + e.Title + "](" + libURL(e.Slug) + "): " + e.Organ + ". " + e.Subtitle + "\n")
		}
	}
	b.WriteString("\n## Optional\n\n")
	b.WriteString("- [Библиотека целиком](" + SiteURL + "/library): " + strconv.Itoa(len(l.items)) + " карточек: диагнозы и инструменты по органам бизнеса\n")
	for _, e := range l.items {
		if e.Kind == "tool" {
			b.WriteString("- [" + e.Title + "](" + libURL(e.Slug) + "): инструмент, " + e.Organ + ". " + e.Subtitle + "\n")
		}
	}
	return b.String()
}

func llmsFullTxt() string {
	p := bsProfile
	l := publicLib()
	var b strings.Builder
	b.WriteString(llmsHead())
	b.WriteString("\n## Как устроена работа\n\n")
	for i, s := range p.Cycle {
		b.WriteString(strconv.Itoa(i+1) + ". " + s + "\n")
	}
	b.WriteString("\n## 7 органов бизнеса\n\n")
	for _, o := range p.Organs {
		b.WriteString("- " + o.Name + ": " + o.What + "\n")
	}
	b.WriteString("\n## Частые вопросы\n\n")
	for _, f := range p.FAQ {
		b.WriteString("### " + f.Q + "\n\n" + f.A + "\n\n")
	}
	b.WriteString("## Открытая библиотека\n\nКарточки клуба по органам бизнеса. Полный текст на странице каждой карточки.\n\n")
	for _, e := range l.items {
		kind := "Диагноз"
		if e.Kind == "tool" {
			kind = "Инструмент"
		}
		b.WriteString("### " + e.Title + "\n\n" + kind + ", " + e.Organ + ". " + libURL(e.Slug) + "\n\n" + e.Subtitle + "\n\n")
		if e.Kind == "diag" {
			b.WriteString(e.Desc + "\n\n")
			if len(e.FirstSteps) > 0 {
				b.WriteString("Первые шаги:\n")
				for _, s := range e.FirstSteps {
					b.WriteString("- " + s + "\n")
				}
				b.WriteString("\n")
			}
		} else {
			b.WriteString(e.Promise + "\n\n")
			if len(e.Steps) > 0 {
				b.WriteString("Шаги:\n")
				for i, s := range e.Steps {
					b.WriteString(strconv.Itoa(i+1) + ". " + s.Title + "\n")
				}
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// ── JSON-LD ──

type obj = map[string]any

func orgID() string { return SiteURL + "/about#org" }

func personNodes() []any {
	var out []any
	for _, f := range bsProfile.Founders {
		out = append(out, obj{
			"@type": "Person", "@id": SiteURL + "/about#" + f.ID, "name": f.Name, "alternateName": f.Latin,
			"jobTitle": f.Role, "description": f.Bio, "worksFor": obj{"@id": orgID()},
			"knowsAbout": []string{"бизнес-трекинг", "управление малым бизнесом", "стратегия"},
		})
	}
	return out
}

func orgNode() obj {
	p := bsProfile
	var founders []any
	for _, f := range p.Founders {
		founders = append(founders, obj{"@id": SiteURL + "/about#" + f.ID})
	}
	var offers []any
	for _, o := range p.Offers {
		offers = append(offers, offerNode(o))
	}
	return obj{
		"@type": []string{"Organization", "ProfessionalService"}, "@id": orgID(),
		"name": p.Name, "alternateName": p.Alt, "url": p.MainSite,
		"logo":  obj{"@type": "ImageObject", "url": SiteURL + "/site/logo.png", "width": 512, "height": 512},
		"image": SiteURL + "/site/og.png", "slogan": p.Slogan, "description": p.Lead,
		"address":    postalAddress(),
		"location":   obj{"@type": "Place", "name": p.Venue, "address": postalAddress()},
		"areaServed": []any{obj{"@type": "City", "name": "Алматы"}, obj{"@type": "Country", "name": "Казахстан"}},
		"telephone":  "+77024035036",
		"contactPoint": obj{"@type": "ContactPoint", "telephone": "+77024035036", "contactType": "customer service",
			"availableLanguage": []string{"ru"}, "url": p.WALink},
		"founder":            founders,
		"sameAs":             []string{p.MainSite, p.Instagram, p.Threads, p.YouTube, p.Channel, p.Bot},
		"knowsAbout":         []string{"бизнес-трекинг", "диагностика бизнеса", "управленческий учёт", "система продаж", "найм и команда", "стратегия малого бизнеса"},
		"priceRange":         "от " + price(p.Offers[0].Price),
		"currenciesAccepted": "KZT",
		"hasOfferCatalog":    obj{"@type": "OfferCatalog", "name": "Программы Business Surgery", "itemListElement": offers},
	}
}

// postalAddress: the club's address for JSON-LD.
func postalAddress() obj {
	return obj{"@type": "PostalAddress", "streetAddress": bsProfile.Street, "addressLocality": bsProfile.City, "addressCountry": "KZ"}
}

func offerNode(o bsOffer) obj {
	return obj{
		"@type": "Offer", "@id": SiteURL + "/about#" + o.ID, "name": o.Name, "description": o.Desc,
		"price": strconv.Itoa(o.Price), "priceCurrency": "KZT", "availability": "https://schema.org/InStock",
		"url": SiteURL + "/about#prices", "seller": obj{"@id": orgID()},
		"itemOffered": obj{"@type": "Service", "name": o.Name, "serviceType": "Бизнес-трекинг", "provider": obj{"@id": orgID()},
			"areaServed": obj{"@type": "City", "name": "Алматы"}},
	}
}

func eventNode(e *bsEvent) obj {
	n := obj{
		"@type": "Event", "@id": SiteURL + "/about#event", "name": e.Name, "startDate": e.Start,
		"eventAttendanceMode": "https://schema.org/OfflineEventAttendanceMode", "eventStatus": "https://schema.org/EventScheduled",
		"location":  obj{"@type": "Place", "name": e.Place, "address": obj{"@type": "PostalAddress", "streetAddress": e.Address, "addressLocality": "Алматы", "addressCountry": "KZ"}},
		"organizer": obj{"@id": orgID()}, "image": SiteURL + "/site/og.png",
		"offers": obj{"@type": "Offer", "price": strconv.Itoa(e.Price), "priceCurrency": "KZT", "availability": "https://schema.org/InStock", "url": firstNonEmpty(e.URL, SiteURL+"/about#event")},
	}
	if e.End != "" {
		n["endDate"] = e.End
	}
	if e.Desc != "" {
		n["description"] = e.Desc
	}
	return n
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func websiteNode() obj {
	return obj{"@type": "WebSite", "@id": SiteURL + "/#website", "url": SiteURL + "/about", "name": "Business Surgery", "inLanguage": "ru", "publisher": obj{"@id": orgID()}}
}

func aboutJSONLD(ev *bsEvent) string {
	var faq []any
	for _, f := range bsProfile.FAQ {
		faq = append(faq, obj{"@type": "Question", "name": f.Q, "acceptedAnswer": obj{"@type": "Answer", "text": f.A}})
	}
	graph := []any{
		websiteNode(),
		orgNode(),
		obj{"@type": "AboutPage", "@id": SiteURL + "/about#page", "url": SiteURL + "/about", "name": "Business Surgery: клуб бизнес-трекинга в Алматы",
			"inLanguage": "ru", "isPartOf": obj{"@id": SiteURL + "/#website"}, "about": obj{"@id": orgID()}, "mainEntity": obj{"@id": orgID()},
			"primaryImageOfPage": SiteURL + "/site/og.png", "dateModified": siteUpdated},
		obj{"@type": "FAQPage", "@id": SiteURL + "/about#faq", "url": SiteURL + "/about#faq", "inLanguage": "ru", "mainEntity": faq},
	}
	graph = append(graph, personNodes()...)
	if ev != nil {
		graph = append(graph, eventNode(ev))
	}
	return ldScript(obj{"@context": "https://schema.org", "@graph": graph})
}

func ldScript(v any) string {
	b, err := json.Marshal(v) // json escapes <, > and &: nothing closes the script early
	if err != nil {
		return ""
	}
	return `<script type="application/ld+json">` + string(b) + `</script>`
}

// ── HTML ──

var hx = html.EscapeString

const siteCSS = `:root{--bg:#050505;--card:#0C0C0C;--line:rgba(255,255,255,.1);--mute:#9A9A9A;--soft:#C9C9C9;--gold:#D4B886}
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
html{background:var(--bg);-webkit-text-size-adjust:100%}
body{background:var(--bg);color:#fff;font:16px/1.6 'Manrope',-apple-system,'Segoe UI',Arial,sans-serif;-webkit-font-smoothing:antialiased;overflow-x:hidden}
a{color:inherit}
.w{max-width:1040px;margin:0 auto;padding:0 40px}
.top{display:flex;align-items:center;justify-content:space-between;gap:16px;height:76px;border-bottom:1px solid var(--line)}
.top img{height:34px;width:auto;display:block}
.top nav{display:flex;gap:18px;font-size:14px;font-weight:600}
.top nav a{text-decoration:none;color:var(--soft)}.top nav a:hover{color:#fff}
.hero{padding:64px 0 36px;background-image:linear-gradient(rgba(255,255,255,.04) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.04) 1px,transparent 1px);background-size:72px 72px;background-position:center -1px}
.pill{display:inline-block;padding:6px 14px;border:1px solid var(--line);border-radius:999px;font-size:13px;font-weight:600;color:var(--soft);background:rgba(255,255,255,.03)}
h1{margin-top:18px;font-size:54px;line-height:1.05;font-weight:800;letter-spacing:-.035em;max-width:880px}
h1 span{color:#8A8A8A}
.lead{margin-top:20px;max-width:760px;font-size:19px;line-height:1.6;color:var(--soft)}
.cta{display:flex;flex-wrap:wrap;gap:12px;margin-top:28px}
.b{display:inline-flex;align-items:center;justify-content:center;text-align:center;min-height:48px;padding:0 22px;border-radius:999px;font-weight:700;font-size:15px;text-decoration:none;border:1px solid rgba(255,255,255,.22)}
.b.p{background:#fff;color:#050505;border-color:#fff}.b:hover{border-color:#fff}
section{padding:44px 0;border-top:1px solid var(--line)}
h2{font-size:32px;line-height:1.15;font-weight:800;letter-spacing:-.025em;margin-bottom:18px}
h3{font-size:18px;line-height:1.35;font-weight:700;margin-bottom:6px}
p+p{margin-top:12px}
.mute{color:var(--mute)}
.facts{display:grid;grid-template-columns:200px 1fr;border:1px solid var(--line);border-radius:16px;overflow:hidden}
.facts dt,.facts dd{padding:13px 18px;border-top:1px solid var(--line)}
.facts dt{color:var(--mute);font-size:14px;font-weight:600}
.facts dt:first-of-type,.facts dt:first-of-type+dd{border-top:0}
ol.cy{list-style:none;counter-reset:c;display:grid;grid-template-columns:repeat(2,1fr);gap:12px}
ol.cy li{counter-increment:c;position:relative;padding:18px 18px 18px 58px;border:1px solid var(--line);border-radius:14px;background:var(--card)}
ol.cy li::before{content:counter(c);position:absolute;left:18px;top:16px;width:28px;height:28px;border-radius:50%;background:#fff;color:#050505;font-weight:800;font-size:14px;display:flex;align-items:center;justify-content:center}
.org{display:grid;grid-template-columns:repeat(4,1fr);gap:10px}
.org div{padding:16px;border:1px solid var(--line);border-radius:14px}
.org b{display:block;font-size:16px}.org span{font-size:14px;color:var(--mute)}
.pr{display:grid;grid-template-columns:repeat(3,1fr);gap:12px}
.pr div{padding:22px;border:1px solid var(--line);border-radius:16px;background:var(--card)}
.pr div:first-child{border-color:rgba(212,184,134,.55)}
.pr b{display:block;font-size:28px;font-weight:800;letter-spacing:-.02em;margin:8px 0}
.pr h3{font-size:16px}.pr p{font-size:14px;color:var(--mute)}
.fd{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.fd div{padding:22px;border:1px solid var(--line);border-radius:16px}
.fd span{display:block;color:var(--mute);font-size:14px;margin-bottom:8px}
.faq h3{margin-top:22px}.faq p{color:var(--soft)}
ul.l,ol.l{padding-left:22px}ul.l li,ol.l li{margin-top:8px}
.ct{display:grid;grid-template-columns:repeat(2,1fr);gap:10px}
.ct a{display:block;padding:14px 16px;border:1px solid var(--line);border-radius:12px;text-decoration:none}
.ct a span{display:block;font-size:13px;color:var(--mute)}
.ix h3{margin-top:26px;font-size:20px}
.ix ul{list-style:none;display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-top:10px}
.ix li a{display:block;height:100%;padding:12px 14px;border:1px solid var(--line);border-radius:12px;text-decoration:none}
.ix li a:hover{border-color:rgba(255,255,255,.4)}
.ix li span{display:block;font-size:13.5px;color:var(--mute);line-height:1.45;margin-top:2px}
.tag{display:inline-block;font-size:11.5px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;color:var(--gold)}
.crumbs{font-size:13.5px;color:var(--mute);margin-top:24px}.crumbs a{text-decoration:none}
.meta{display:flex;flex-wrap:wrap;gap:8px;margin-top:18px}.meta span{font-size:13px;padding:5px 12px;border:1px solid var(--line);border-radius:999px;color:var(--soft)}
.st{counter-reset:s;list-style:none}
.st li{counter-increment:s;padding:18px 0;border-top:1px solid var(--line)}
.st li:first-child{border-top:0}
.st h3::before{content:counter(s) ". ";color:var(--mute)}
.st em{display:block;margin-top:8px;font-style:normal;font-size:14.5px;color:var(--mute)}
.box{padding:24px;border:1px solid var(--line);border-radius:18px;background:var(--card)}
.cs{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.cs article{padding:22px;border:1px solid var(--line);border-radius:16px;background:var(--card)}
.cs h3{font-size:17px}.cs .who{font-size:14px;color:var(--mute);margin-top:4px}
.cs ul{list-style:none;margin-top:12px}.cs li{font-size:14.5px;padding:7px 0;border-top:1px solid var(--line)}.cs li:first-child{border-top:0}
.cs p{font-size:14px;color:var(--soft);margin-top:10px}
.note{font-size:13.5px;color:var(--mute);margin-top:10px}
footer{padding:36px 0 48px;border-top:1px solid var(--line);font-size:14px;color:var(--mute)}
footer p+p{margin-top:6px}
@media(max-width:820px){.w{padding:0 16px}h1{font-size:36px}.lead{font-size:17px}h2{font-size:26px}
 .top nav{gap:12px;font-size:13px}.top img{height:28px}
 .facts{grid-template-columns:1fr}.facts dt{padding-bottom:0}.facts dd{border-top:0;padding-top:4px}
 ol.cy,.pr,.fd,.ct,.ix ul,.cs{grid-template-columns:1fr}.cta .b{width:100%;padding:0 16px}.org{grid-template-columns:1fr 1fr}.hero{padding:40px 0 28px}}`

func pageHead(title, desc, canonical, ogType, ld string) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"ru\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	b.WriteString("<title>" + hx(title) + "</title>\n<meta name=\"description\" content=\"" + hx(desc) + "\">\n")
	b.WriteString("<link rel=\"canonical\" href=\"" + hx(canonical) + "\">\n<meta name=\"robots\" content=\"index,follow,max-snippet:-1,max-image-preview:large\">\n")
	b.WriteString("<meta name=\"theme-color\" content=\"#050505\">\n<link rel=\"icon\" type=\"image/png\" href=\"/site/logo.png\">\n")
	b.WriteString("<link rel=\"alternate\" type=\"text/plain\" title=\"llms.txt\" href=\"/llms.txt\">\n")
	for _, m := range [][2]string{{"og:type", ogType}, {"og:site_name", "Business Surgery"}, {"og:locale", "ru_RU"}, {"og:title", title}, {"og:description", desc}, {"og:url", canonical}, {"og:image", SiteURL + "/site/og.png"}, {"og:image:width", "1200"}, {"og:image:height", "630"}} {
		b.WriteString("<meta property=\"" + m[0] + "\" content=\"" + hx(m[1]) + "\">\n")
	}
	b.WriteString("<meta name=\"twitter:card\" content=\"summary_large_image\">\n")
	if fontsCSS != nil {
		b.WriteString(fontPreload + "<link rel=\"stylesheet\" href=\"" + fontsCSS.URL() + "\">\n")
	}
	b.WriteString("<style>" + siteCSS + "</style>\n" + ld + "\n</head>\n<body>\n")
	b.WriteString(`<header class="w top"><a href="/about" aria-label="Business Surgery"><img src="/site/logo-white.png" alt="Business Surgery" width="71" height="34"></a><nav><a href="/about">О клубе</a><a href="/library">Библиотека</a><a href="` + bsProfile.MainSite + `">bxclub.kz</a></nav></header>` + "\n")
	return b.String()
}

func pageFoot() string {
	p := bsProfile
	return `<footer><div class="w"><p><b style="color:#fff">Business Surgery</b> · клуб бизнес-трекинга · ` + hx(p.Address) + `</p>` +
		`<p>WhatsApp <a href="` + p.WALink + `">` + p.WAPhone + `</a> · Telegram <a href="` + p.Bot + `">` + p.BotHandle + `</a> · <a href="` + p.MainSite + `">bxclub.kz</a> · <a href="/about">О клубе</a> · <a href="/library">Библиотека</a> · <a href="/llms.txt">llms.txt</a></p>` +
		`<p>Обновлено ` + siteUpdated + `</p></div></footer>` + "\n</body>\n</html>\n"
}

func ctaButtons(src string) string {
	p := bsProfile
	return `<div class="cta"><a class="b p" href="` + p.WALink + `?text=` + waText + `">Записаться на экспресс-разбор · 50&nbsp;000&nbsp;₸</a><a class="b" href="` + p.Bot + `?start=` + src + `">99 чек-листов в Telegram</a></div>`
}

// «Хочу записаться на экспресс-разбор»
const waText = "%D0%A5%D0%BE%D1%87%D1%83%20%D0%B7%D0%B0%D0%BF%D0%B8%D1%81%D0%B0%D1%82%D1%8C%D1%81%D1%8F%20%D0%BD%D0%B0%20%D1%8D%D0%BA%D1%81%D0%BF%D1%80%D0%B5%D1%81%D1%81-%D1%80%D0%B0%D0%B7%D0%B1%D0%BE%D1%80"

func aboutHTML() string {
	p := bsProfile
	ev := publicEvent()
	l := publicLib()
	var b strings.Builder
	b.WriteString(pageHead("Business Surgery: клуб бизнес-трекинга в Алматы для собственников",
		"Business Surgery (BS): клуб бизнес-трекинга в Алматы. Разбор бизнеса каждые 10 дней с двумя основателями, диагноз по 7 органам бизнеса, ежедневная отчётность. Экспресс-разбор 50 000 ₸.",
		SiteURL+"/about", "website", aboutJSONLD(ev)))
	b.WriteString(`<main>` + "\n")
	b.WriteString(`<div class="hero"><div class="w"><span class="pill">Алматы · клуб бизнес-трекинга</span><h1>Business Surgery <span>разбирает бизнес собственника каждые 10 дней</span></h1>`)
	b.WriteString(`<p class="lead">` + hx(p.Lead) + `</p>` + ctaButtons("site") + `</div></div>` + "\n")

	b.WriteString(`<section id="facts"><div class="w"><h2>Коротко о клубе</h2><dl class="facts">`)
	for _, f := range p.Facts {
		b.WriteString("<dt>" + hx(f[0]) + "</dt><dd>" + hx(f[1]) + "</dd>")
	}
	b.WriteString(`</dl><p class="note">Девиз клуба: «` + hx(p.Slogan) + `».</p></div></section>` + "\n")

	b.WriteString(`<section id="method"><div class="w"><h2>Как устроен 10-дневный цикл</h2><ol class="cy">`)
	for _, s := range p.Cycle {
		b.WriteString("<li>" + hx(s) + "</li>")
	}
	b.WriteString(`</ol><p class="note">Резиденты объединены в группы по 5 человек. Задачи, отчёты и прогресс резидента собраны на платформе app.bxclub.kz и в Telegram-приложении клуба.</p></div></section>` + "\n")

	b.WriteString(`<section id="organs"><div class="w"><h2>7 органов бизнеса</h2><p class="mute" style="margin-bottom:18px">Метод клуба рассматривает бизнес как живой организм. На разборе находят самый слабый орган и причину его проблем, с них начинается план.</p><div class="org">`)
	for _, o := range p.Organs {
		b.WriteString("<div><b>" + hx(o.Name) + "</b><span>" + hx(o.What) + "</span></div>")
	}
	b.WriteString(`</div></div></section>` + "\n")

	b.WriteString(`<section id="prices"><div class="w"><h2>Цены</h2><div class="pr">`)
	for _, o := range p.Offers {
		b.WriteString(`<div id="` + o.ID + `"><h3>` + hx(o.Name) + `</h3><b>` + price(o.Price) + `</b><p>` + hx(o.Desc) + `</p></div>`)
	}
	b.WriteString(`</div><p class="note">Первый шаг всегда один: экспресс-разбор. Актуальные условия уточняйте при записи.</p></div></section>` + "\n")

	if ev != nil {
		b.WriteString(`<section id="event"><div class="w"><h2>` + hx(ev.Name) + `</h2><div class="box"><p><b>` + hx(ev.Start) + `</b> · ` + hx(ev.Place) + `, ` + hx(ev.Address) + `</p>`)
		if ev.Desc != "" {
			b.WriteString(`<p>` + hx(ev.Desc) + `</p>`)
		}
		if ev.URL != "" {
			b.WriteString(`<p><a href="` + hx(ev.URL) + `">Регистрация</a></p>`)
		}
		b.WriteString(`</div></div></section>` + "\n")
	}

	if cs := PublicCases(); len(cs) > 0 { // R51: published resident cases (with their consent)
		b.WriteString(`<section id="cases"><div class="w"><h2>Кейсы резидентов: было → стало</h2><p class="mute" style="margin-bottom:18px">Цифры резидентов клуба, опубликованные с их согласия. Анонимные кейсы показывают только нишу, город и цифры.</p><div class="cs">`)
		for _, c := range cs {
			b.WriteString(`<article><h3>` + hx(c.Title) + `</h3><p class="who">` + hx(c.Who) + `</p><ul>`)
			for _, m := range c.Metrics {
				b.WriteString(`<li>` + hx(m) + `</li>`)
			}
			b.WriteString(`</ul>`)
			if c.Story != "" {
				b.WriteString(`<p>` + hx(c.Story) + `</p>`)
			}
			b.WriteString(`</article>`)
		}
		b.WriteString(`</div></div></section>` + "\n")
	}

	b.WriteString(`<section id="founders"><div class="w"><h2>Основатели</h2><div class="fd">`)
	for _, f := range p.Founders {
		b.WriteString(`<div id="` + f.ID + `"><h3>` + hx(f.Name) + `</h3><span>` + hx(f.Role) + `</span><p>` + hx(f.Bio) + `</p></div>`)
	}
	b.WriteString(`</div><p class="note">Оба основателя присутствуют на каждом разборе.</p></div></section>` + "\n")

	b.WriteString(`<section id="faq" class="faq"><div class="w"><h2>Частые вопросы</h2>`)
	for _, f := range p.FAQ {
		b.WriteString("<h3>" + hx(f.Q) + "</h3><p>" + hx(f.A) + "</p>")
	}
	b.WriteString(`</div></section>` + "\n")

	b.WriteString(`<section id="library" class="ix"><div class="w"><h2>Открытая библиотека клуба</h2><p class="mute">Диагнозы, которые клуб чаще всего находит на разборах, и инструменты, которыми их лечат. Без регистрации.</p><ul>`)
	n := 0
	for _, e2 := range l.items {
		if e2.Kind == "diag" && n < 8 {
			b.WriteString(`<li><a href="/library/` + e2.Slug + `"><b>` + hx(e2.Title) + `</b><span>` + hx(e2.Subtitle) + `</span></a></li>`)
			n++
		}
	}
	b.WriteString(`</ul><div class="cta"><a class="b" href="/library">Вся библиотека: ` + strconv.Itoa(len(l.items)) + ` карточек</a></div></div></section>` + "\n")

	b.WriteString(`<section id="contacts"><div class="w"><h2>Контакты</h2><div class="ct">`)
	for _, c := range [][3]string{
		{"WhatsApp", p.WAPhone, p.WALink},
		{"Telegram-бот", p.BotHandle, p.Bot + "?start=site"},
		{"Telegram-канал", "@bsurgery_kz", p.Channel},
		{"Instagram", "@business.surgery", p.Instagram},
		{"Threads", "@business.surgery", p.Threads},
		{"YouTube", "@Businessurgery", p.YouTube},
		{"Сайт клуба", "bxclub.kz", p.MainSite},
		{"Платформа резидентов", "app.bxclub.kz", SiteURL + "/"},
	} {
		b.WriteString(`<a href="` + hx(c[2]) + `"><span>` + hx(c[0]) + `</span>` + hx(c[1]) + `</a>`)
	}
	b.WriteString(`</div><p class="note">Адрес: ` + hx(bsProfile.Address) + `, Казахстан.</p></div></section>` + "\n</main>\n")
	b.WriteString(pageFoot())
	return b.String()
}

func libraryHTML() string {
	l := publicLib()
	var b strings.Builder
	ld := ldScript(obj{"@context": "https://schema.org", "@graph": []any{
		websiteNode(),
		obj{"@type": "CollectionPage", "@id": SiteURL + "/library#page", "url": SiteURL + "/library", "name": "Открытая библиотека Business Surgery",
			"inLanguage": "ru", "isPartOf": obj{"@id": SiteURL + "/#website"}, "publisher": obj{"@id": orgID(), "@type": "Organization", "name": "Business Surgery", "url": bsProfile.MainSite},
			"dateModified": siteUpdated},
		breadcrumbs([2]string{"Business Surgery", SiteURL + "/about"}, [2]string{"Библиотека", SiteURL + "/library"}),
	}})
	b.WriteString(pageHead("Библиотека Business Surgery: диагнозы бизнеса и инструменты для собственника",
		"Открытая библиотека клуба Business Surgery (Алматы): признаки, причины и первые шаги для частых проблем малого бизнеса, пошаговые инструменты по финансам, продажам, команде и стратегии.",
		SiteURL+"/library", "website", ld))
	b.WriteString(`<main><div class="hero"><div class="w"><span class="pill">Открыто без регистрации</span><h1>Библиотека Business Surgery <span>диагнозы и инструменты для собственника</span></h1>`)
	b.WriteString(`<p class="lead">Карточки, по которым клуб работает на разборах. Диагноз: признаки, причины, самопроверка и первые шаги. Инструмент: шаги, частые ошибки, метрики и рабочий лист в PDF.</p>` + ctaButtons("site") + `</div></div>` + "\n")
	b.WriteString(`<section class="ix"><div class="w">`)
	for _, o := range l.organs {
		b.WriteString(`<h2 id="` + slugify(o) + `">` + hx(o) + `</h2>`)
		for _, kind := range []string{"diag", "tool"} {
			var li strings.Builder
			for _, i := range l.byOrgan[o] {
				it := l.items[i]
				if it.Kind == kind {
					li.WriteString(`<li><a href="/library/` + it.Slug + `"><b>` + hx(it.Title) + `</b><span>` + hx(it.Subtitle) + `</span></a></li>`)
				}
			}
			if li.Len() == 0 {
				continue
			}
			h := "Диагнозы"
			if kind == "tool" {
				h = "Инструменты"
			}
			b.WriteString(`<h3>` + h + `</h3><ul>` + li.String() + `</ul>`)
		}
	}
	b.WriteString(`</div></section></main>` + "\n")
	b.WriteString(pageFoot())
	return b.String()
}

func breadcrumbs(items ...[2]string) obj {
	var list []any
	for i, it := range items {
		list = append(list, obj{"@type": "ListItem", "position": i + 1, "name": it[0], "item": it[1]})
	}
	return obj{"@type": "BreadcrumbList", "itemListElement": list}
}

func list(tag string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<" + tag + ` class="l">`)
	for _, s := range items {
		b.WriteString("<li>" + hx(s) + "</li>")
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	out := string(r[:n])
	if i := strings.LastIndex(out, " "); i > n/2 {
		out = out[:i]
	}
	return strings.TrimRight(out, " ,.:;") + "…"
}

func libItemHTML(l *libIndex, i int) string {
	it := l.items[i]
	url := libURL(it.Slug)
	kind, kindLD := "Диагноз", "Диагноз бизнеса"
	if it.Kind == "tool" {
		kind, kindLD = "Инструмент", "Инструмент для собственника"
	}
	lead := it.Subtitle
	desc := it.Desc
	if it.Kind == "tool" {
		desc = it.Promise
	}
	metaDesc := cut(it.Title+": "+lead+". "+desc, 300)
	ld := ldScript(obj{"@context": "https://schema.org", "@graph": []any{
		obj{"@type": "Article", "@id": url + "#article", "headline": cut(it.Title+": "+lead, 110), "description": cut(desc, 300),
			"inLanguage": "ru", "url": url, "mainEntityOfPage": url, "image": SiteURL + "/site/og.png",
			"articleSection": it.Organ, "genre": kindLD, "dateModified": siteUpdated, "datePublished": "2026-10-01",
			"author":    obj{"@type": "Organization", "@id": orgID(), "name": "Business Surgery", "url": bsProfile.MainSite},
			"publisher": obj{"@type": "Organization", "@id": orgID(), "name": "Business Surgery", "url": bsProfile.MainSite, "logo": obj{"@type": "ImageObject", "url": SiteURL + "/site/logo.png"}},
			"isPartOf":  obj{"@type": "CollectionPage", "@id": SiteURL + "/library#page"}},
		breadcrumbs([2]string{"Business Surgery", SiteURL + "/about"}, [2]string{"Библиотека", SiteURL + "/library"}, [2]string{it.Title, url}),
	}})
	var b strings.Builder
	b.WriteString(pageHead(it.Title+": "+strings.ToLower(kind)+" | Business Surgery", metaDesc, url, "article", ld))
	b.WriteString(`<main><div class="w"><p class="crumbs"><a href="/about">Business Surgery</a> / <a href="/library">Библиотека</a> / <a href="/library#` + slugify(it.Organ) + `">` + hx(it.Organ) + `</a></p></div>`)
	b.WriteString(`<div class="hero" style="padding-top:28px"><div class="w"><span class="tag">` + kind + ` · ` + hx(it.Organ) + `</span><h1>` + hx(it.Title) + `</h1><p class="lead">` + hx(lead) + `</p>`)
	if it.Kind == "tool" {
		b.WriteString(`<div class="meta">`)
		for _, m := range []string{it.Time, it.Level} {
			if m != "" {
				b.WriteString("<span>" + hx(m) + "</span>")
			}
		}
		if it.Source != "" {
			b.WriteString("<span>Основа: " + hx(it.Source) + "</span>")
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></div>` + "\n")
	sec := func(id, h, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		b.WriteString(`<section id="` + id + `"><div class="w"><h2>` + h + `</h2>` + body + `</div></section>` + "\n")
	}
	if it.Kind == "diag" {
		sec("what", "Что происходит", "<p>"+hx(it.Desc)+"</p>")
		sec("signs", "Признаки", list("ul", it.Signs))
		sec("causes", "Причины", list("ul", it.Causes))
		if it.Cost != "" {
			sec("cost", "Во что это обходится", "<p>"+hx(it.Cost)+`</p><p class="note">Цифры приведены как пример расчёта. В вашем бизнесе суммы будут свои.</p>`)
		}
		if it.Test != nil && len(it.Test.Questions) > 0 {
			var qs []string
			for _, q := range it.Test.Questions {
				qs = append(qs, q.Q)
			}
			body := `<p class="mute">Посчитайте ответы «да».</p>` + list("ol", qs)
			if it.Test.Scale != "" {
				body += `<p class="note">Как читать результат: ` + hx(it.Test.Scale) + `.</p>`
			}
			sec("test", "Самопроверка", body)
		}
		sec("first", "Первые шаги", list("ol", it.FirstSteps))
		if it.Risk != "" {
			sec("risk", "Если ничего не менять", "<p>"+hx(it.Risk)+"</p>")
		}
		if len(it.Cure) > 0 {
			var li strings.Builder
			li.WriteString(`<ul class="l">`)
			for _, c := range it.Cure {
				if j, ok := l.byTitle[strings.ToLower(c)]; ok {
					li.WriteString(`<li><a href="/library/` + l.items[j].Slug + `">` + hx(c) + `</a></li>`)
				} else {
					li.WriteString("<li>" + hx(c) + "</li>")
				}
			}
			li.WriteString(`</ul>`)
			sec("cure", "Инструменты лечения", li.String())
		}
	} else {
		sec("promise", "Что даёт инструмент", "<p>"+hx(it.Promise)+"</p>")
		sec("when", "Когда он нужен", list("ul", it.When))
		if len(it.Steps) > 0 {
			var st strings.Builder
			st.WriteString(`<ol class="st">`)
			for _, s := range it.Steps {
				st.WriteString("<li><h3>" + hx(s.Title) + "</h3><p>" + hx(s.Do) + "</p>")
				if s.Mistake != "" {
					st.WriteString("<em>Частая ошибка: " + hx(s.Mistake) + "</em>")
				}
				st.WriteString("</li>")
			}
			st.WriteString(`</ol>`)
			sec("steps", "Пошагово", st.String())
		}
		sec("metrics", "Как понять, что работает", list("ul", it.Metrics))
		if it.Template != nil {
			var blocks []string
			for _, bl := range it.Template.Blocks {
				if bl.Title != "" {
					blocks = append(blocks, bl.Title)
				}
			}
			body := "<p><b>" + hx(it.Template.Title) + "</b></p>"
			if it.Template.Intro != "" {
				body += "<p>" + hx(it.Template.Intro) + "</p>"
			}
			if len(blocks) > 0 {
				body += `<p class="note">Разделы: ` + hx(strings.Join(blocks, ", ")) + `.</p>`
			}
			body += `<div class="cta"><a class="b" href="/t/` + hx(it.ID) + `.pdf">Открыть рабочий лист (PDF)</a></div>`
			sec("template", "Рабочий лист", body)
		}
		if ds := l.helps[it.Slug]; len(ds) > 0 {
			var li strings.Builder
			li.WriteString(`<ul class="l">`)
			for _, j := range ds {
				li.WriteString(`<li><a href="/library/` + l.items[j].Slug + `">` + hx(l.items[j].Title) + `</a></li>`)
			}
			li.WriteString(`</ul>`)
			sec("helps", "Помогает при диагнозах", li.String())
		}
	}
	b.WriteString(`<section id="bs"><div class="w"><div class="box"><h2 style="font-size:24px">Разобрать это на вашем бизнесе</h2><p>Business Surgery: клуб бизнес-трекинга в Алматы. На экспресс-разборе оба основателя клуба за 1 час находят главную причину, которая тормозит ваш бизнес, и первые шаги. Стоимость 50 000 ₸. <a href="/about">Подробнее о клубе</a>.</p>` + ctaButtons("site") + `</div></div></section>` + "\n")
	// соседние карточки того же органа
	var near strings.Builder
	n := 0
	for _, j := range l.byOrgan[it.Organ] {
		if j != i && n < 6 {
			near.WriteString(`<li><a href="/library/` + l.items[j].Slug + `"><b>` + hx(l.items[j].Title) + `</b><span>` + hx(l.items[j].Subtitle) + `</span></a></li>`)
			n++
		}
	}
	if n > 0 {
		b.WriteString(`<section class="ix"><div class="w"><h2>Ещё по теме «` + hx(it.Organ) + `»</h2><ul>` + near.String() + `</ul></div></section>` + "\n")
	}
	b.WriteString("</main>\n" + pageFoot())
	return b.String()
}

func notFoundHTML() string {
	return pageHead("Страница не найдена | Business Surgery", "Страница не найдена.", SiteURL+"/library", "website", "") +
		`<main><div class="hero"><div class="w"><h1>Страница не найдена</h1><p class="lead">Возможно, карточку переименовали. Вся библиотека: <a href="/library">app.bxclub.kz/library</a>.</p></div></div></main>` + pageFoot()
}

// FontHead (R57): the self-hosted Manrope for a standalone page (the client's board).
func FontHead() string {
	if fontsCSS == nil {
		return ""
	}
	return fontPreload + `<link rel="stylesheet" href="` + fontsCSS.URL() + `">` + "\n"
}

// Contacts (R57): the club's public WhatsApp and Telegram bot.
func Contacts() (waPhone, waLink, bot string) {
	return bsProfile.WAPhone, bsProfile.WALink, bsProfile.Bot
}
