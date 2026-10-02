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
	if err = json.Unmarshal(tb, &rich.tools); err != nil {
		richErr = err
		return
	}
	var diag []json.RawMessage
	if err = json.Unmarshal(db, &diag); err != nil {
		richErr = err
		return
	}
	rich.byID = make(map[string]int, len(rich.tools))
	for i, t := range rich.tools {
		rich.byID[t.ID] = i
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
	rich.json = buf.Bytes()
	var z bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
	_, _ = zw.Write(rich.json)
	_ = zw.Close()
	rich.gz = z.Bytes()
	rich.etag = `"lr-` + ver + `"`
}

// LibRich returns the whole rich library as JSON, its gzip and an ETag.
func LibRich() (plain, gz []byte, etag string, err error) {
	richOnce.Do(loadRich)
	return rich.json, rich.gz, rich.etag, richErr
}

// RichTools lists the rich tools (in library order).
func RichTools() []RichTool {
	richOnce.Do(loadRich)
	return rich.tools
}

// RichToolByID finds one tool; nil when there is none.
func RichToolByID(id string) *RichTool {
	richOnce.Do(loadRich)
	if i, ok := rich.byID[id]; ok {
		return &rich.tools[i]
	}
	return nil
}
