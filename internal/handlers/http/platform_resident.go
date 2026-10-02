package http

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// What a resident may see and do on the platform.
//
// A resident sees only boards made for their name (board info.res matches
// the name in the resident list from the Google Sheet), may edit them (tick
// tasks, fill tests, write the report), and may not create or delete boards.
// Club sections they get read-only: the method libraries. Everything else a
// resident saves lands in their personal scope and never touches club data.
// Finance and other residents' data are cut out of the shared seed.

// Club sections a resident may read (never write).
var residentReadableKeys = map[string]bool{
	"bs_diag": true, "bs_tools": true, "bs_libver": true,
	"bs_questions": true, "bs_qver": true,
	"bs_reslib": true, "bs_stickers": true, "bs_useful": true,
	"bs_achdefs": true, "bs_teams": true, "bs_checklists": true, "bs_guides": true, "bs_stickerpack_srv": true, "bs_bookfiles": true,
}

// Written only by the server (seed data cut out of the page).
const platformSeedKey = "bs_seed"

func platformRole(c *gin.Context) string {
	v, _ := c.Get("role")
	s, _ := v.(string)
	return s
}

func isResident(c *gin.Context) bool { return platformRole(c) == "resident" }

func platformTgID(c *gin.Context) int64 {
	id, err := strconv.ParseInt(strings.TrimPrefix(platformUser(c), "tg:"), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// normName makes "Пётр  Иванов", "петр иванов" and "Пётр Иванов" equal.
func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	return strings.Join(strings.Fields(s), " ")
}

// residentNames caches tg id -> name for a minute so each request does not hit the DB.
type residentNames struct {
	mu   sync.Mutex
	m    map[int64]residentName
	repo *pg.PlatformRepo
}

type residentName struct {
	name   string
	active bool
	at     time.Time
}

func (rn *residentNames) get(ctx context.Context, id int64) (string, bool) {
	rn.mu.Lock()
	if v, ok := rn.m[id]; ok && time.Since(v.at) < time.Minute {
		rn.mu.Unlock()
		return v.name, v.active
	}
	rn.mu.Unlock()
	name, active, err := rn.repo.ResidentByTg(ctx, id)
	if err != nil {
		return "", false
	}
	rn.mu.Lock()
	rn.m[id] = residentName{name: name, active: active, at: time.Now()}
	rn.mu.Unlock()
	return name, active
}

func (rn *residentNames) forget() {
	rn.mu.Lock()
	rn.m = map[int64]residentName{}
	rn.mu.Unlock()
}

// residentOf returns the calling resident's name, or "" if they lost access.
func (h *PlatformHandler) residentOf(c *gin.Context) string {
	name, active := h.names.get(c.Request.Context(), platformTgID(c))
	if !active {
		return ""
	}
	return name
}

func boardBelongsTo(b *pg.PlatformBoard, name string) bool {
	return name != "" && normName(b.Resident) == normName(name)
}

// filterForResident trims a sync answer down to what this resident may see.
func filterForResident(boards []pg.PlatformBoard, docs []pg.PlatformDoc, name, userScope string) ([]pg.PlatformBoard, []pg.PlatformDoc) {
	ob := make([]pg.PlatformBoard, 0, len(boards))
	for i := range boards {
		if boardBelongsTo(&boards[i], name) {
			ob = append(ob, boards[i])
		}
	}
	od := make([]pg.PlatformDoc, 0, len(docs))
	for _, d := range docs {
		switch {
		case d.Scope == userScope:
			od = append(od, d)
		case d.Scope == "club" && residentReadableKeys[d.Key]:
			od = append(od, d)
		case d.Scope == "club" && d.Key == platformSeedKey:
			if !d.Deleted {
				d.Value = residentSeed(d.Value, name)
			}
			od = append(od, d)
		}
	}
	return ob, od
}

// residentSeed keeps from the club seed only what a resident may see.
//
// Everyone's name, format and start date (the club roster) and visit counts.
// Their own money, fines and meetings only once the seed is live data from
// the server (LIVE): a snapshot baked into the page would show outdated money.
// Other residents' money, fines and meetings, NPS, leads and the P&L never.
func residentSeed(seed, name string) string {
	var s map[string]json.RawMessage
	if json.Unmarshal([]byte(seed), &s) != nil {
		return "{}"
	}
	var live struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(s["LIVE"], &live)
	isLive := live.Source == "server"
	me := normName(name)

	var residents []map[string]any
	_ = json.Unmarshal(s["RESIDENTS"], &residents)
	roster := make([]map[string]any, 0, len(residents))
	for _, r := range residents {
		if isLive && me != "" && normName(toStr(r["name"])) == me {
			roster = append(roster, r) // their own line, money included
			continue
		}
		roster = append(roster, map[string]any{
			"name": r["name"], "format": r["format"], "start": r["start"],
		})
	}
	var sdata map[string]json.RawMessage
	_ = json.Unmarshal(s["SDATA"], &sdata)
	var visits []map[string]any
	_ = json.Unmarshal(sdata["visits"], &visits)
	cleanVisits := make([]map[string]any, 0, len(visits))
	for _, v := range visits {
		cleanVisits = append(cleanVisits, map[string]any{
			"name": v["name"], "per": v["per"], "done": v["done"], "months": v["months"],
		})
	}
	fines := []any{}
	schedule := []any{}
	if isLive && me != "" {
		var all []map[string]any
		_ = json.Unmarshal(s["FINES"], &all)
		for _, f := range all {
			if normName(toStr(f["res"])) == me {
				fines = append(fines, f)
			}
		}
		var meets []map[string]any
		_ = json.Unmarshal(sdata["schedule"], &meets)
		for _, m := range meets {
			if normName(toStr(m["res"])) == me {
				schedule = append(schedule, m)
			}
		}
	}
	out := map[string]any{
		"PL_ROWS":   []any{},
		"RESIDENTS": roster,
		"FINES":     fines,
		"SDATA": map[string]any{
			"schedule": schedule, "restasks": []any{}, "visits": cleanVisits,
			"nps": []any{}, "leads": []any{}, "profit": []any{},
		},
	}
	if isLive {
		// Not "server": a resident's page must not treat the empty P&L as live.
		out["LIVE"] = map[string]string{"source": "resident"}
	}
	buf, _ := json.Marshal(out)
	return string(buf)
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}
