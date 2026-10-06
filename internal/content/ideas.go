package content

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// R46: бизнес-идеи (1000+) для тех, кто пришёл на разбор без бизнеса.
// Файлы ideas/<код категории>.json: массив идей (схема в IdeaItem). Страница
// и приложение берут каталог одним JSON по своему адресу, лениво, когда
// открыли раздел «Бизнес-идеи» (handlers/http/ideas.go).

//go:embed ideas/*.json
var ideasFS embed.FS

// IdeaCats: коды и названия категорий в порядке вкладок.
var IdeaCats = []struct {
	ID, N, Short, Icon string
}{
	{"serv", "Услуги для населения", "Услуги", "🧺"},
	{"b2b", "Услуги для бизнеса (B2B)", "B2B", "💼"},
	{"prod", "Производство", "Производство", "🏭"},
	{"trade", "Торговля (розница, опт, маркетплейсы)", "Торговля", "🛒"},
	{"food", "Общепит и еда", "Еда", "☕"},
	{"it", "Онлайн, IT и digital", "Онлайн и IT", "💻"},
	{"edu", "Образование и дети", "Образование", "📚"},
	{"beauty", "Красота, здоровье, спорт", "Красота и спорт", "💅"},
	{"build", "Строительство, ремонт, недвижимость", "Стройка и ремонт", "🔨"},
	{"auto", "Транспорт, авто, логистика", "Авто и логистика", "🚚"},
	{"agro", "Сельское хозяйство и переработка", "Агро", "🌾"},
	{"tour", "Туризм, события, развлечения", "Туризм и события", "🎉"},
}

// IdeaItem: одна идея (поля как в файлах).
type IdeaItem struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Cat        string   `json:"cat"`
	Sub        string   `json:"sub"`
	Short      string   `json:"short"`
	Desc       string   `json:"desc"`
	Budget     [2]int64 `json:"budget"`
	Payback    string   `json:"payback"`
	Margin     string   `json:"margin"`
	Difficulty int      `json:"difficulty"`
	Format     []string `json:"format"`
	Team       string   `json:"team"`
	Need       []string `json:"need"`
	FirstSteps []string `json:"first_steps"`
	Risks      []string `json:"risks"`
	Tags       []string `json:"tags"`
	Cover      struct {
		Icon string `json:"icon"`
		Hue  int    `json:"hue"`
	} `json:"cover"`
}

type ideaCat struct {
	ID    string   `json:"id"`
	N     string   `json:"n"`
	Short string   `json:"s"`
	Icon  string   `json:"icon"`
	Count int      `json:"count"`
	Subs  []string `json:"subs"`
}

type ideasCatalog struct {
	Version string     `json:"version"`
	Total   int        `json:"total"`
	Cats    []ideaCat  `json:"cats"`
	Items   []IdeaItem `json:"items"`
}

var (
	ideasOnce  sync.Once
	ideasJSON  []byte
	ideasGz    []byte
	ideasEtag  string
	ideasErr   error
	ideasByID  map[string]*IdeaItem
	ideasCount int
)

func loadIdeas() {
	files, err := fs.Glob(ideasFS, "ideas/*.json")
	if err != nil {
		ideasErr = err
		return
	}
	sort.Strings(files)
	byCat := map[string][]IdeaItem{}
	seen := map[string]bool{}
	for _, f := range files {
		b, err := ideasFS.ReadFile(f)
		if err != nil {
			ideasErr = err
			return
		}
		var items []IdeaItem
		if err := json.Unmarshal(b, &items); err != nil {
			ideasErr = fmt.Errorf("%s: %w", f, err)
			return
		}
		for _, it := range items {
			it.Title = strings.TrimSpace(it.Title)
			if it.ID == "" || it.Title == "" || seen[it.ID] {
				continue
			}
			// длинное тире в текстах BS не используется
			it.Short = strings.ReplaceAll(it.Short, "—", "-")
			it.Desc = strings.ReplaceAll(it.Desc, "—", "-")
			seen[it.ID] = true
			byCat[it.Cat] = append(byCat[it.Cat], it)
		}
	}
	cat := ideasCatalog{}
	for _, c := range IdeaCats {
		items := byCat[c.ID]
		sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		subs, sseen := []string{}, map[string]bool{}
		for _, it := range items {
			if s := strings.TrimSpace(it.Sub); s != "" && !sseen[s] {
				sseen[s] = true
				subs = append(subs, s)
			}
		}
		cat.Cats = append(cat.Cats, ideaCat{ID: c.ID, N: c.N, Short: c.Short, Icon: c.Icon, Count: len(items), Subs: subs})
		cat.Items = append(cat.Items, items...)
	}
	cat.Total = len(cat.Items)
	body, _ := json.Marshal(cat.Items)
	sum := sha256.Sum256(body)
	cat.Version = hex.EncodeToString(sum[:8])
	ideasJSON, err = json.Marshal(cat)
	if err != nil {
		ideasErr = err
		return
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(ideasJSON)
	_ = zw.Close()
	ideasGz = buf.Bytes()
	ideasEtag = `"i-` + cat.Version + `"`
	ideasByID = make(map[string]*IdeaItem, len(cat.Items))
	for i := range cat.Items {
		ideasByID[cat.Items[i].ID] = &cat.Items[i]
	}
	ideasCount = cat.Total
}

// Ideas: весь каталог {version, total, cats, items} (JSON и gzip) и его ETag.
func Ideas() (plain, gz []byte, etag string, err error) {
	ideasOnce.Do(loadIdeas)
	return ideasJSON, ideasGz, ideasEtag, ideasErr
}

// IdeaByID: одна идея (nil, если такой нет).
func IdeaByID(id string) *IdeaItem {
	ideasOnce.Do(loadIdeas)
	return ideasByID[id]
}

// IdeasTotal: сколько идей в каталоге.
func IdeasTotal() int {
	ideasOnce.Do(loadIdeas)
	return ideasCount
}
