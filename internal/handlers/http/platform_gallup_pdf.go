package http

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/gin-gonic/gin"
)

// POST /api/v1/platform/gallup/pdf {GallupDoc} → the analysis as a branded
// PDF (R29). The platform sends what it shows on screen, the visuals already
// computed; here the text is bounded and the brand rules applied.

const gallupPDFMax = 1 << 20

func gpStr(s string, n int) string {
	s = gallupText(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

func gpList(in []string, max, n int) []string {
	var out []string
	for _, x := range in {
		if x = gpStr(x, n); x != "" && len(out) < max {
			out = append(out, x)
		}
	}
	return out
}

func gpTriples(in []tplpdf.GallupTriple, max int) []tplpdf.GallupTriple {
	var out []tplpdf.GallupTriple
	for _, t := range in {
		t = tplpdf.GallupTriple{A: gpStr(t.A, 200), B: gpStr(t.B, 900), C: gpStr(t.C, 900), D: gpStr(t.D, 900)}
		if t.A != "" || t.B != "" {
			if len(out) < max {
				out = append(out, t)
			}
		}
	}
	return out
}

func gpLinks(in []tplpdf.GallupLink, max int) []tplpdf.GallupLink {
	var out []tplpdf.GallupLink
	for _, l := range in {
		l = tplpdf.GallupLink{Names: gpList(l.Names, 4, 40), Title: gpStr(l.Title, 200), Effect: gpStr(l.Effect, 900), Scene: gpStr(l.Scene, 900), Fix: gpStr(l.Fix, 900)}
		if l.Title != "" && len(out) < max {
			out = append(out, l)
		}
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sanitizeGallupDoc(g *tplpdf.GallupDoc) {
	g.Name, g.Date = gpStr(g.Name, 80), gpStr(g.Date, 40)
	g.Headline, g.Plain, g.Top10 = gpStr(g.Headline, 240), gpStr(g.Plain, 1600), gpStr(g.Top10, 1200)
	if len(g.Top5) > 5 {
		g.Top5 = g.Top5[:5]
	}
	for i := range g.Top5 {
		t := &g.Top5[i]
		t.Rank = clampInt(t.Rank, 1, 34)
		t.Name, t.Ru, t.Domain, t.Line = gpStr(t.Name, 40), gpStr(t.Ru, 40), gpStr(t.Domain, 40), gpStr(t.Line, 400)
	}
	if len(g.Domains) > 4 {
		g.Domains = g.Domains[:4]
	}
	for i := range g.Domains {
		dm := &g.Domains[i]
		dm.Ru, dm.Note = gpStr(dm.Ru, 40), gpStr(dm.Note, 80)
		dm.Score, dm.Top10 = clampInt(dm.Score, 0, 100), clampInt(dm.Top10, 0, 10)
	}
	g.DomainLine = gpStr(g.DomainLine, 400)
	if len(g.Map.Nodes) > 16 {
		g.Map.Nodes = g.Map.Nodes[:16]
	}
	for i := range g.Map.Nodes {
		n := &g.Map.Nodes[i]
		n.Ru, n.Rank = gpStr(n.Ru, 40), clampInt(n.Rank, 1, 34)
		n.X, n.Y, n.R = clampF(n.X, 0, 1), clampF(n.Y, 0, 1), clampF(n.R, 6, 40)
		if n.Kind != "anchor" {
			n.Kind = "top"
		}
		if n.Lab != "a" {
			n.Lab = "b"
		}
	}
	if len(g.Map.Edges) > 40 {
		g.Map.Edges = g.Map.Edges[:40]
	}
	g.Map.Line = gpStr(g.Map.Line, 400)
	g.Amplify, g.Conflict, g.Anchors = gpLinks(g.Amplify, 6), gpLinks(g.Conflict, 4), gpLinks(g.Anchors, 5)
	g.Best, g.Stress, g.Reset = gpList(g.Best, 5, 400), gpList(g.Stress, 5, 400), gpList(g.Reset, 4, 400)
	g.Blind, g.Hires, g.Areas = gpTriples(g.Blind, 5), gpTriples(g.Hires, 4), gpTriples(g.Areas, 6)
	g.Risks, g.Plan = gpTriples(g.Risks, 6), gpTriples(g.Plan, 6)
	g.Keep, g.Delegate, g.WorkWith = gpList(g.Keep, 5, 400), gpList(g.Delegate, 5, 400), gpList(g.WorkWith, 7, 400)
	g.Business, g.Partners = gpList(g.Business, 6, 500), gpList(g.Partners, 5, 400)
	if len(g.Roles) > 10 {
		g.Roles = g.Roles[:10]
	}
	for i := range g.Roles {
		r := &g.Roles[i]
		r.N, r.Energy, r.Risk = clampInt(r.N, 1, 99), clampInt(r.Energy, 0, 100), clampInt(r.Risk, 0, 100)
		r.Name, r.Zone, r.Why = gpStr(r.Name, 80), gpStr(r.Zone, 20), gpStr(r.Why, 200)
	}
	g.RolesLine = gpStr(g.RolesLine, 400)
	if len(g.Talents) > 34 {
		g.Talents = g.Talents[:34]
	}
	for i := range g.Talents {
		t := &g.Talents[i]
		t.Rank = clampInt(t.Rank, 1, 34)
		t.Name, t.Ru, t.DomainRu = gpStr(t.Name, 40), gpStr(t.Ru, 40), gpStr(t.DomainRu, 40)
		t.Essence, t.Business, t.Blind, t.Manage = gpStr(t.Essence, 500), gpStr(t.Business, 600), gpStr(t.Blind, 500), gpStr(t.Manage, 600)
	}
	// R71: the second half of the analysis, built here from the order
	gallupDocPlus(g)
}

func GallupPDF(c *gin.Context) {
	var g tplpdf.GallupDoc
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, gallupPDFMax)
	if err := c.ShouldBindJSON(&g); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	sanitizeGallupDoc(&g)
	if len(g.Talents) == 0 && len(g.Top5) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty"})
		return
	}
	b, err := tplpdf.RenderGallup(&g)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render"})
		return
	}
	title := "Gallup"
	if n := strings.TrimSpace(g.Name); n != "" {
		title += " " + n
	}
	ascii, utf := tplFileNames(title)
	h := c.Writer.Header()
	h.Set("Content-Disposition", `attachment; filename="`+ascii+`"; filename*=UTF-8''`+url.PathEscape(utf))
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/pdf", b)
}
