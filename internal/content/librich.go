package content

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// Богатая библиотека (R25): для каждого инструмента и диагноза клуба
// подробная карточка (история, шаги, метрики, шаблон, тест, лечение).
// Ключ: id пункта. seed_dx_N / seed_tl_N: исходные пункты страницы по
// позиции в var DIAG / var TOOLS, dx_* / tl_*: пункты library_ext.
// Поле "_seed" хранит хэши исходного текста пересекающихся полей: страница
// сравнивает с ними текст из документов клуба и не перекрывает правки
// команды (handlers/http/library_rich.go, web/platform.html lib2*).

//go:embed library_rich/*.json
var libRichFS embed.FS

// RichTemplate is the printable worksheet of a tool.
type RichTemplate struct {
	Title  string      `json:"title"`
	Intro  string      `json:"intro"`
	Blocks []RichBlock `json:"blocks"`
}

// RichBlock: fields | table | checklist | scale | note.
type RichBlock struct {
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Fields    []string   `json:"fields,omitempty"`
	Columns   []string   `json:"columns,omitempty"`
	Rows      [][]string `json:"rows,omitempty"`
	EmptyRows int        `json:"empty_rows,omitempty"`
	Items     []string   `json:"items,omitempty"`
	Min       int        `json:"min,omitempty"`
	Max       int        `json:"max,omitempty"`
	Lines     int        `json:"lines,omitempty"`
}

// RichTool: the fields the template renderer needs; the page gets the raw JSON.
type RichTool struct {
	ID       string        `json:"id"`
	Organ    string        `json:"organ"`
	Title    string        `json:"title"`
	Subtitle string        `json:"subtitle"`
	Time     string        `json:"time"`
	Level    string        `json:"level"`
	Source   string        `json:"source"`
	Metrics  []string      `json:"metrics"`
	Template *RichTemplate `json:"template"`
}

type richLib struct {
	json  []byte // {"version":..,"tools":[..],"diag":[..]}
	gz    []byte
	etag  string
	tools []RichTool
	byID  map[string]int
}

var (
	richOnce sync.Once
	rich     richLib
	richErr  error
	// The embedded files and the items added at runtime (SetRichExtra:
	// recommendations the server put into the library itself, kept in the DB).
	richMu                 sync.RWMutex
	richTB, richDB         []byte
	richExtraT, richExtraD []json.RawMessage
)

func loadRich() {
	tb, err := libRichFS.ReadFile("library_rich/tools.json")
	if err != nil {
		richErr = err
		return
	}
	db, err := libRichFS.ReadFile("library_rich/diag.json")
	if err != nil {
		richErr = err
		return
	}
	richTB, richDB = tb, db
	richErr = buildRich()
}

// buildRich joins the embedded items and the runtime ones (an id already in
// the embedded files is not added twice). Callers hold richMu for writing.
func buildRich() error {
	var tools []json.RawMessage
	var diag []json.RawMessage
	if err := json.Unmarshal(richTB, &tools); err != nil {
		return err
	}
	if err := json.Unmarshal(richDB, &diag); err != nil {
		return err
	}
	seen := map[string]bool{}
	idOf := func(r json.RawMessage) string {
		var x struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r, &x)
		return x.ID
	}
	for _, l := range [][]json.RawMessage{tools, diag} {
		for _, r := range l {
			seen[idOf(r)] = true
		}
	}
	for _, r := range richExtraT {
		if id := idOf(r); id != "" && !seen[id] {
			seen[id] = true
			tools = append(tools, r)
		}
	}
	for _, r := range richExtraD {
		if id := idOf(r); id != "" && !seen[id] {
			seen[id] = true
			diag = append(diag, r)
		}
	}
	tb, _ := json.Marshal(tools)
	db, _ := json.Marshal(diag)
	if len(richExtraT) == 0 && len(richExtraD) == 0 {
		tb, db = richTB, richDB // byte for byte as embedded: the same ETag as before
	}
	var parsed []RichTool
	if err := json.Unmarshal(tb, &parsed); err != nil {
		return err
	}
	next := richLib{tools: parsed, byID: make(map[string]int, len(parsed))}
	for i, t := range parsed {
		next.byID[t.ID] = i
	}
	sum := sha256.New()
	sum.Write(tb)
	sum.Write(db)
	ver := hex.EncodeToString(sum.Sum(nil)[:6])
	var buf bytes.Buffer
	buf.WriteString(`{"version":"` + ver + `","tools":`)
	buf.Write(tb)
	buf.WriteString(`,"diag":`)
	buf.Write(db)
	buf.WriteString(`}`)
	next.json = buf.Bytes()
	var z bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
	_, _ = zw.Write(next.json)
	_ = zw.Close()
	next.gz = z.Bytes()
	next.etag = `"lr-` + ver + `"`
	rich = next
	return nil
}

// SetRichExtra replaces the items added at runtime (tools and diagnoses in
// the rich format, each with an id) and rebuilds the library.
func SetRichExtra(tools, diag []json.RawMessage) error {
	richOnce.Do(loadRich)
	richMu.Lock()
	defer richMu.Unlock()
	if richErr != nil && richTB == nil {
		return richErr
	}
	prevT, prevD := richExtraT, richExtraD
	richExtraT, richExtraD = tools, diag
	if err := buildRich(); err != nil {
		richExtraT, richExtraD = prevT, prevD
		_ = buildRich()
		return err
	}
	richErr = nil
	return nil
}

// LibRich returns the whole rich library as JSON, its gzip and an ETag.
func LibRich() (plain, gz []byte, etag string, err error) {
	richOnce.Do(loadRich)
	richMu.RLock()
	defer richMu.RUnlock()
	return rich.json, rich.gz, rich.etag, richErr
}

// RichTools lists the rich tools (in library order).
func RichTools() []RichTool {
	richOnce.Do(loadRich)
	richMu.RLock()
	defer richMu.RUnlock()
	return rich.tools
}

// RichToolByID finds one tool; nil when there is none.
func RichToolByID(id string) *RichTool {
	richOnce.Do(loadRich)
	richMu.RLock()
	defer richMu.RUnlock()
	if i, ok := rich.byID[id]; ok {
		t := rich.tools[i]
		return &t
	}
	return nil
}

// RichTitles: titles of the rich tools and diagnoses (for cure checks).
func RichTitles() (tools, diag []string) {
	richOnce.Do(loadRich)
	richMu.RLock()
	defer richMu.RUnlock()
	for _, t := range rich.tools {
		tools = append(tools, t.Title)
	}
	var all struct {
		Diag []struct {
			Title string `json:"title"`
		} `json:"diag"`
	}
	_ = json.Unmarshal(rich.json, &all)
	for _, d := range all.Diag {
		diag = append(diag, d.Title)
	}
	return tools, diag
}
