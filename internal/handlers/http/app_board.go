package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// The Telegram app shows a resident their own board from the platform:
// point A and B, diagnoses, the strategy, tools and tasks of the cycle.
// The resident is found by the Telegram ID Telegram itself signed, so
// nobody can read someone else's board by changing a parameter.

// AppBoardSource is what the gateway needs from the platform storage.
type AppBoardSource interface {
	ResidentByTg(ctx context.Context, tgID int64) (string, bool, error)
	LiveBoards(ctx context.Context) ([]pg.PlatformBoard, error)
}

type appBoardNode struct {
	Type  string `json:"type"`
	Role  string `json:"role,omitempty"`
	Title string `json:"title"`
	Desc  string `json:"desc,omitempty"`
	Organ string `json:"organ,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Due   string `json:"due,omitempty"`
}

type AppBoard struct {
	Resident   string         `json:"resident"`
	Name       string         `json:"name"`
	Updated    string         `json:"updated"`
	Cycle      int            `json:"cycle"`
	PointA     string         `json:"pointA"`
	PointB     string         `json:"pointB"`
	Strategy   string         `json:"strategy"`
	Experience string         `json:"experience"`
	Diagnoses  []appBoardNode `json:"diagnoses"`
	Tools      []appBoardNode `json:"tools"`
	Tasks      []appBoardNode `json:"tasks"`
	// Measurements made on the platform: Gallup, the 7-organ test, health.
	Tests  json.RawMessage `json:"tests,omitempty"`
	Health json.RawMessage `json:"health,omitempty"`
}

// Placeholders of a fresh board are not content.
var boardPlaceholders = map[string]bool{
	"Имя резидента": true, "Сфера деятельности": true, "Оборот, прибыль сейчас": true,
	"Оборот, прибыль цель": true, "Как дойдём": true, "Что уже пробовал": true,
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	if boardPlaceholders[s] {
		return ""
	}
	return s
}

// SummarizeBoard turns a platform board into what the app shows.
func SummarizeBoard(b *pg.PlatformBoard) AppBoard {
	var d struct {
		Name    string            `json:"name"`
		Updated string            `json:"updated"`
		Info    map[string]string `json:"info"`
		History []json.RawMessage `json:"history"`
		Tests   json.RawMessage   `json:"tests"`
		Health  json.RawMessage   `json:"health"`
		Nodes   []struct {
			Type  string `json:"type"`
			Role  string `json:"role"`
			Title string `json:"title"`
			Desc  string `json:"desc"`
			Organ string `json:"organ"`
			Task  *struct {
				Completed bool   `json:"completed"`
				Date      string `json:"date"`
			} `json:"task"`
			Done bool `json:"done"`
		} `json:"nodes"`
	}
	_ = json.Unmarshal(b.Data, &d)
	out := AppBoard{
		Resident: b.Resident, Name: d.Name, Updated: d.Updated, Cycle: len(d.History) + 1,
		PointA: strings.TrimSpace(d.Info["a"]), PointB: strings.TrimSpace(d.Info["b"]),
		Diagnoses: []appBoardNode{}, Tools: []appBoardNode{}, Tasks: []appBoardNode{},
		Tests: nonEmptyJSON(d.Tests), Health: nonEmptyJSON(d.Health),
	}
	join := func(title, desc string) string {
		t, s := clean(title), clean(desc)
		switch {
		case t != "" && s != "":
			return t + ". " + s
		case t != "":
			return t
		}
		return s
	}
	for _, n := range d.Nodes {
		node := appBoardNode{Type: n.Type, Role: n.Role, Title: strings.TrimSpace(n.Title), Desc: strings.TrimSpace(n.Desc), Organ: n.Organ}
		switch n.Type {
		case "diag", "dna":
			if node.Title != "" {
				out.Diagnoses = append(out.Diagnoses, node)
			}
		case "tool":
			if node.Title != "" {
				out.Tools = append(out.Tools, node)
			}
		case "task":
			node.Done = n.Done || (n.Task != nil && n.Task.Completed)
			if n.Task != nil {
				node.Due = strings.TrimSpace(n.Task.Date)
			}
			if node.Title != "" {
				out.Tasks = append(out.Tasks, node)
			}
		case "strat":
			if n.Role == "exp" {
				out.Experience = join(n.Title, n.Desc)
			} else if s := join(n.Title, n.Desc); s != "" && out.Strategy == "" {
				out.Strategy = s
			}
		case "point":
			// the info fields hold the numbers; the node text is a fallback
			if n.Role == "pointA" && out.PointA == "" {
				out.PointA = join(n.Title, n.Desc)
			}
			if n.Role == "pointB" && out.PointB == "" {
				out.PointB = join(n.Title, n.Desc)
			}
		}
	}
	return out
}

func nonEmptyJSON(b json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" || t == "{}" || t == "[]" {
		return nil
	}
	return b
}

// latestBoardOf picks the most recently changed board of a resident.
func latestBoardOf(boards []pg.PlatformBoard, name string) *pg.PlatformBoard {
	var mine []pg.PlatformBoard
	for i := range boards {
		if !boards[i].Deleted && boardBelongsTo(&boards[i], name) {
			mine = append(mine, boards[i])
		}
	}
	if len(mine) == 0 {
		return nil
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].UpdatedAt.After(mine[j].UpdatedAt) })
	return &mine[0]
}

// MyBoard: GET /api/v1/app/myboard?_tg=initData[&name=... for the team]
func (g *AppGateway) MyBoard(c *gin.Context) {
	if g.Boards == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "boards_not_configured"})
		return
	}
	u, ok := g.identify(c, c.Query("_tg"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	name := ""
	if _, admin := g.Admins[u.ID]; admin && strings.TrimSpace(c.Query("name")) != "" {
		name = strings.TrimSpace(c.Query("name")) // the team previews a resident
	} else {
		n, active, err := g.Boards.ResidentByTg(ctx, u.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup_failed"})
			return
		}
		if !active || n == "" {
			c.JSON(http.StatusOK, gin.H{"board": nil, "reason": "not_resident"})
			return
		}
		name = n
	}
	boards, err := g.Boards.LiveBoards(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "boards_failed"})
		return
	}
	b := latestBoardOf(boards, name)
	if b == nil {
		c.JSON(http.StatusOK, gin.H{"board": nil, "resident": name, "reason": "no_board"})
		return
	}
	s := SummarizeBoard(b)
	c.JSON(http.StatusOK, gin.H{"board": s, "resident": name, "tests": s.Tests, "health": s.Health})
}
