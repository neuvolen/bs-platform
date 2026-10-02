package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/gin-gonic/gin"
)

// The app's bundle from the server (moving the club off the sheet, step 2).
//
// Once the stage is "server", the gateway answers getBotCache itself: the
// club's sections (residents, debts, fines, reports, schedule, money,
// finished meetings, team profiles) come from the server's tables, always
// current, also while the script is slow or down. The sections still kept
// in the script's own sheets (wheels, leads, tasks, content, checklists, the
// avatar) are taken from the script's last bundle for that person, refreshed
// in the background. If the server cannot build its part, the script
// answers, as before.

// AppBundleSource gives the club data the bundle is built from.
type AppBundleSource interface {
	LoadBundle(ctx context.Context, since time.Time) (*club.Snapshot, error)
}

const (
	auxMaxAge      = 24 * time.Hour   // the script's part is served up to a day old
	auxWait        = 25 * time.Second // the first open waits this long for the script's part
	bundleLoadSpan = 31 * 24 * time.Hour
)

// What the app gets for a section the script did not give (as the script's
// own fallbacks).
var auxDefaults = map[string]json.RawMessage{
	"wheelSummary": json.RawMessage(`null`), "wheelAll": json.RawMessage(`null`),
	"leads": json.RawMessage(`{"leads":[]}`), "problems": json.RawMessage(`{"problems":[],"total":0}`),
	"resTasks": json.RawMessage(`{"tasks":[]}`), "smm": json.RawMessage(`null`),
	"leadmagnets": json.RawMessage(`{"items":[]}`), "contentPlan": json.RawMessage(`{"items":[]}`),
	"checklistSchedule": json.RawMessage(`[]`), "adminProfiles": json.RawMessage(`[]`), "myAvatar": json.RawMessage(`""`),
}

// ServerBundle builds the server's sections of the bundle; built lists them.
func (g *AppGateway) ServerBundle(ctx context.Context) (map[string]json.RawMessage, []string, error) {
	now := g.now()
	snap, err := g.Club.LoadBundle(ctx, now.Add(-bundleLoadSpan))
	if err != nil {
		return nil, nil, err
	}
	parts, built := club.AppBundle(snap, now)
	out := make(map[string]json.RawMessage, len(parts))
	for k, v := range parts {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		out[k] = b
	}
	return out, built, nil
}

// mergeBundle puts the server's sections over the script's bundle, in the
// script's order of keys.
func mergeBundle(server map[string]json.RawMessage, script []byte, now time.Time) []byte {
	top := map[string]json.RawMessage{}
	if len(script) > 0 {
		_ = json.Unmarshal(script, &top)
	}
	delete(top, "_cached")
	for k, v := range server {
		top[k] = v
	}
	for k, v := range auxDefaults {
		if _, ok := top[k]; !ok {
			top[k] = v
		}
	}
	top["ts"] = json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10))
	var b bytes.Buffer
	b.WriteByte('{')
	seen := map[string]bool{}
	first := true
	write := func(k string) {
		v, ok := top[k]
		if !ok || seen[k] {
			return
		}
		seen[k] = true
		if !first {
			b.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(v)
	}
	for _, k := range club.BundleKeys {
		write(k)
	}
	for k := range top {
		write(k)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// serveServerBundle answers getBotCache from the server; false: it could not.
func (g *AppGateway) serveServerBundle(c *gin.Context, q url.Values, u *platformTgUser, force bool) bool {
	ctx := c.Request.Context()
	srv, _, err := g.ServerBundle(ctx)
	if err != nil {
		log.Printf("app bundle from the server: %v", err)
		g.mu.Lock()
		g.srvStats.fallbacks++
		g.mu.Unlock()
		return false
	}
	aux := g.auxFor(ctx, q, u, force)
	body := mergeBundle(srv, aux, g.now())
	g.mu.Lock()
	g.srvStats.served++
	g.mu.Unlock()
	g.note(true, u.ID)
	c.Header("X-BS-Bundle-Source", "server")
	c.Header("X-BS-Bundle-Age", "0")
	c.Data(http.StatusOK, "application/json; charset=utf-8", g.forUser(u.ID, hideDone(body, g.doneSet(ctx), g.now())))
	return true
}

// auxFor is the script's bundle for this person: kept up to a day and
// refreshed behind; the first time it is waited for, briefly.
func (g *AppGateway) auxFor(ctx context.Context, q url.Values, u *platformTgUser, force bool) []byte {
	key := q.Get("chatId")
	g.mu.Lock()
	if g.aux == nil {
		g.aux, g.auxBusy = map[string]*cachedBundle{}, map[string]bool{}
	}
	a := g.aux[key]
	var body []byte
	age := time.Duration(-1)
	stale := false
	if a != nil {
		body, age, stale = a.body, g.now().Sub(a.at), a.stale
	}
	refresh := (a == nil || force || stale || age > bundleFresh) && !g.auxBusy[key]
	if refresh {
		g.auxBusy[key] = true
	}
	g.mu.Unlock()
	if body != nil && age < auxMaxAge {
		if refresh {
			go g.refreshAux(key, q, u)
		}
		return body
	}
	if !refresh {
		return body // someone is already fetching it; serve what there is
	}
	done := make(chan struct{})
	go func() {
		g.refreshAux(key, q, u)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(auxWait):
	case <-ctx.Done():
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if a := g.aux[key]; a != nil {
		return a.body
	}
	return body
}

func (g *AppGateway) refreshAux(key string, q url.Values, u *platformTgUser) {
	ctx, cancel := context.WithTimeout(context.Background(), appScriptTimout)
	defer cancel()
	nq := g.params(q, "getBotCache", u)
	nq.Del("fresh")
	fresh, err := g.get(ctx, nq)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.auxBusy[key] = false
	if err != nil {
		log.Printf("app bundle, the script's part: %v", err)
		return
	}
	g.aux[key] = &cachedBundle{body: fresh, at: g.now()}
}

// ScriptBundle fetches the script's own bundle for a person, freshly built,
// through the same signed path as the app.
func (g *AppGateway) ScriptBundle(ctx context.Context, tgID int64) ([]byte, error) {
	u := &platformTgUser{ID: tgID}
	return g.get(ctx, g.params(url.Values{"fresh": {"1"}}, "getBotCache", u))
}

// ServerStats: how often the server answered the bundle, and fell back.
func (g *AppGateway) ServerStats() (served, fallbacks int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.srvStats.served, g.srvStats.fallbacks
}
