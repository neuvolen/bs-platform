package http

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// The resident's library in the app: books, tools, diagnoses and resources
// from the platform's knowledge base, the same records the team edits there.
// Residents and the team only; files are served through the same check.

// AppLibrarySource reads the platform's club documents and files.
type AppLibrarySource interface {
	GetDoc(ctx context.Context, scope, key string) (*pg.PlatformDoc, error)
	GetFile(ctx context.Context, id string) (*pg.PlatformFile, error)
}

type libItem struct {
	Kind     string   `json:"kind"` // book, tool, diag, resource
	Title    string   `json:"title"`
	Organ    string   `json:"organ,omitempty"`
	Short    string   `json:"short,omitempty"`
	Why      string   `json:"why,omitempty"`
	How      []string `json:"how,omitempty"`
	Example  string   `json:"example,omitempty"`
	Check    []string `json:"check,omitempty"`
	Signs    []string `json:"signs,omitempty"`
	Risk     string   `json:"risk,omitempty"`
	Link     string   `json:"link,omitempty"`
	Video    string   `json:"video,omitempty"`
	Sample   string   `json:"sample,omitempty"`
	File     string   `json:"file,omitempty"`
	FileName string   `json:"fileName,omitempty"`
	URL      string   `json:"url,omitempty"`
	Cat      string   `json:"cat,omitempty"`
}

// libraryAllowed: an active resident or the team.
func (g *AppGateway) libraryAllowed(c *gin.Context) bool {
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return false
	}
	if _, admin := g.Admins[u.ID]; admin {
		return true
	}
	if g.Boards != nil {
		if _, active, err := g.Boards.ResidentByTg(c.Request.Context(), u.ID); err == nil && active {
			return true
		}
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "residents_only"})
	return false
}

func (g *AppGateway) docJSON(ctx context.Context, key string, out any) {
	if g.Library == nil {
		return
	}
	d, err := g.Library.GetDoc(ctx, "club", key)
	if err != nil || d == nil || d.Deleted {
		return
	}
	_ = json.Unmarshal([]byte(d.Value), out)
}

// BuildLibrary turns the platform's documents into the app's list.
func BuildLibrary(tools, diag []map[string]any, res []map[string]any) []libItem {
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
	list := func(m map[string]any, k string) []string {
		var out []string
		if a, ok := m[k].([]any); ok {
			for _, x := range a {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	items := []libItem{}
	add := func(kind string, m map[string]any) {
		it := libItem{Kind: kind, Title: str(m, "title"), Organ: str(m, "organ"), Short: str(m, "short"),
			Why: str(m, "why"), How: list(m, "how"), Example: str(m, "example"), Check: list(m, "check"),
			Link: str(m, "link"), Video: str(m, "video"), Sample: str(m, "sample"), File: str(m, "file"), FileName: str(m, "fileName")}
		if kind == "diag" {
			it.Short, it.Signs, it.Risk = str(m, "desc"), list(m, "signs"), str(m, "risk")
		}
		if it.Title != "" {
			items = append(items, it)
		}
	}
	for _, t := range tools {
		if b, _ := t["isBook"].(bool); b {
			add("book", t)
		}
	}
	for _, t := range tools {
		if b, _ := t["isBook"].(bool); !b {
			add("tool", t)
		}
	}
	for _, d := range diag {
		add("diag", d)
	}
	for _, r := range res {
		it := libItem{Kind: "resource", Title: str(r, "t"), Short: str(r, "d"), URL: str(r, "url"), Cat: str(r, "cat")}
		if it.Title != "" {
			items = append(items, it)
		}
	}
	return items
}

// Library: GET /api/v1/app/library?_tg=initData
func (g *AppGateway) Library_(c *gin.Context) {
	if !g.libraryAllowed(c) {
		return
	}
	ctx := c.Request.Context()
	var tools, diag, res []map[string]any
	g.docJSON(ctx, "bs_tools", &tools)
	g.docJSON(ctx, "bs_diag", &diag)
	// R78: the platform keeps «Ресурсы клуба» as {items:[…]}; an older copy as a bare list
	var resDoc struct {
		Items []map[string]any `json:"items"`
	}
	if g.docJSON(ctx, "bs_reslib", &res); res == nil {
		g.docJSON(ctx, "bs_reslib", &resDoc)
		res = resDoc.Items
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.JSON(http.StatusOK, gin.H{"items": BuildLibrary(tools, diag, res)})
}

// LibraryFile: GET /api/v1/app/file/:id?_tg=initData
func (g *AppGateway) LibraryFile(c *gin.Context) {
	if g.Library == nil || !platformIDRe.MatchString(c.Param("id")) {
		c.Status(http.StatusNotFound)
		return
	}
	if !g.libraryAllowed(c) {
		return
	}
	f, err := g.Library.GetFile(c.Request.Context(), c.Param("id"))
	if err != nil || f == nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": f.Name}))
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, f.Mime, f.Data)
}
