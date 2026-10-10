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
	auxMaxAge      = 24 * time.Hour // the script's part is served up to a day old
	bundleLoadSpan = 31 * 24 * time.Hour
)

// scriptBudget is how long an open of the app waits for the script when the
// server has the data itself (R22: the app opened slowly). After it the
// server's own data answers and the script's answer is kept for the next
// open. The sections only the script has are then listed in "_partial": the
// app keeps its own copy of them and asks again shortly.
var scriptBudget = 1500 * time.Millisecond

// What the app gets for a section the script did not give (as the script's
// own fallbacks).
var auxDefaults = map[string]json.RawMessage{
	"wheelSummary": json.RawMessage(`null`), "wheelAll": json.RawMessage(`null`),
	"leads": json.RawMessage(`{"leads":[]}`), "problems": json.RawMessage(`{"problems":[],"total":0}`),
	"resTasks": json.RawMessage(`{"tasks":[]}`), "smm": json.RawMessage(`null`),
	"leadmagnets": json.RawMessage(`{"items":[]}`), "contentPlan": json.RawMessage(`{"items":[]}`),
	"checklistSchedule": json.RawMessage(`[]`), "adminProfiles": json.RawMessage(`[]`), "myAvatar": json.RawMessage(`""`),
}

// ServerBundle builds the server's sections of the bundle for one person
// (tgID: the caller; 0 for none); built lists them. Since step 3 that is
// also the app's own sections (wheels, leads, tasks, content, checklists)
// once the import brings their sheets, and the caller's avatar.
func (g *AppGateway) ServerBundle(ctx context.Context, tgID int64) (map[string]json.RawMessage, []string, error) {
	now := g.now()
	snap, err := g.Club.LoadBundle(ctx, now.Add(-bundleLoadSpan))
	if err != nil {
		return nil, nil, err
	}
	parts, built := club.AppBundle(snap, now)
	// R81: debts and fines from the payments ledger, as «Учёт → Долги и штрафы»
	if rep, at, ok := g.ledger(ctx); ok {
		applyLedger(parts, snap, rep, at)
		built = append(built, "ledger")
	}
	cid := ""
	if tgID != 0 {
		cid = strconv.FormatInt(tgID, 10)
	}
	more, moreBuilt := club.AppSections(snap, cid, now)
	for k, v := range more {
		parts[k] = v
	}
	built = append(built, moreBuilt...)
	if av, ok := g.myAvatar(ctx, tgID); ok {
		parts["myAvatar"] = av
		built = append(built, "myAvatar")
	}
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
	srv, _, err := g.ServerBundle(ctx, u.ID)
	if err != nil {
		log.Printf("app bundle from the server: %v", err)
		g.mu.Lock()
		g.srvStats.fallbacks++
		g.mu.Unlock()
		return false
	}
	// The script is asked only for sections the server does not build.
	var aux []byte
	if len(missingAux(srv)) > 0 && club.SheetLegacy() { // after the cutover the defaults stand for a missing part
		aux = g.auxFor(ctx, q, u, force)
		if aux == nil {
			markPartial(srv)
		}
	}
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
	t := time.NewTimer(scriptBudget)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
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

// missingAux: the sections the server did not build (the script has them).
func missingAux(srv map[string]json.RawMessage) []string {
	var out []string
	for _, k := range club.BundleKeys {
		if _, def := auxDefaults[k]; def {
			if _, ok := srv[k]; !ok {
				out = append(out, k)
			}
		}
	}
	for k := range auxDefaults { // keys outside the bundle's order
		if _, ok := srv[k]; !ok && !contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// markPartial lists in "_partial" the sections answered with placeholders
// because the script's part is not here yet.
func markPartial(srv map[string]json.RawMessage) {
	if m := missingAux(srv); len(m) > 0 {
		b, _ := json.Marshal(m)
		srv["_partial"] = b
	}
}

// scriptCall is one fetch of the script's bundle for a person; done closes
// when it ends. Opens that come meanwhile share it.
type scriptCall struct {
	done chan struct{}
	body []byte
	err  error
	gen  int
}

// scriptFetch fetches the script's bundle in the background, so an open that
// stops waiting does not lose it: the answer is kept for the next open
// (unless the data changed meanwhile).
func (g *AppGateway) scriptFetch(key string, q url.Values, u *platformTgUser) *scriptCall {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*scriptCall{}
	}
	if sc := g.calls[key]; sc != nil && sc.gen == g.gen {
		g.mu.Unlock()
		return sc
	}
	sc := &scriptCall{done: make(chan struct{}), gen: g.gen}
	g.calls[key] = sc
	g.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), appScriptTimout)
		defer cancel()
		body, err := g.get(ctx, q)
		g.note(err == nil, u.ID)
		g.mu.Lock()
		sc.body, sc.err = body, err
		if err == nil && sc.gen == g.gen {
			g.bundles[key] = &cachedBundle{body: body, at: g.now()}
		}
		if g.calls[key] == sc {
			delete(g.calls, key)
		}
		g.mu.Unlock()
		close(sc.done)
	}()
	return sc
}

// interimOK: in stage shadow the server has the club's data (imported, and
// every app write goes to it first), so an open need not wait for the script.
func (g *AppGateway) interimOK(ctx context.Context) bool {
	return g.Stage != nil && g.Club != nil && g.Stage(ctx) == StageShadow
}

// serveInterim answers getBotCache from the server while the script is slow;
// false: the server could not build it.
func (g *AppGateway) serveInterim(c *gin.Context, u *platformTgUser) bool {
	ctx := c.Request.Context()
	srv, _, err := g.ServerBundle(ctx, u.ID)
	if err != nil {
		log.Printf("app bundle, the server's while the script is slow: %v", err)
		return false
	}
	markPartial(srv)
	body := mergeBundle(srv, nil, g.now())
	g.mu.Lock()
	g.srvStats.interim++
	g.mu.Unlock()
	c.Header("X-BS-Bundle-Source", "server-interim")
	c.Header("X-BS-Bundle-Age", "0")
	c.Data(http.StatusOK, "application/json; charset=utf-8", g.forUser(u.ID, hideDone(body, g.doneSet(ctx), g.now())))
	return true
}

// serverRole answers checkUserRole from the server's club data (stages
// shadow and server): a resident is on the debet sheet with this Telegram ID
// and not former. The team is left to the script, which knows its admins.
func (g *AppGateway) serverRole(c *gin.Context, u *platformTgUser) bool {
	ctx := c.Request.Context()
	if g.Stage == nil || g.Club == nil || g.Stage(ctx) == StageSheet {
		return false
	}
	if _, team := g.Admins[u.ID]; team {
		return false
	}
	snap, err := g.Club.LoadBundle(ctx, g.now())
	if err != nil || snap == nil || len(snap.Residents) == 0 {
		return false
	}
	role, former := "lead", false
	for _, r := range snap.Residents {
		if r.TgID != u.ID || r.Archived {
			continue
		}
		if r.Former {
			former = true
		} else {
			role = "resident"
		}
	}
	out := gin.H{"role": role, "source": "server"}
	if role == "lead" && former {
		out["former"] = true
	}
	c.JSON(http.StatusOK, out)
	return true
}
