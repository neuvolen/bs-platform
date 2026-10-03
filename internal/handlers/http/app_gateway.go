package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// The Telegram app (neuvolen.github.io/bs-app) talks to the server instead
// of the Google Apps Script directly.
//
//   - Who is calling is taken from Telegram's signed initData, not from a
//     chatId the page puts in the address: nobody can act as someone else.
//   - The script still does the work: the server passes each call on,
//     signed, with the checked identity. Once every app goes through the
//     server, the script can refuse calls that are not signed (app_gateway).
//   - The app's data bundle is kept on the server per person and handed out
//     at once while a fresh one is fetched in the background.
//
// The page sends initData as the "_tg" parameter (GET) or field (POST), so
// its requests stay "simple" and need no CORS preflight.

// DefaultAppScriptURL is the Apps Script deployment the app has always used.
const DefaultAppScriptURL = "https://script.google.com/macros/s/AKfycbwBbU7pJMyIptJoOtJ5hctU2rYbh3AioA-ScM14Y3dwuIF0UFNYrp7HiWqhOiQX74NpPA/exec"

const (
	appMaxAge       = 7 * 24 * time.Hour // a Mini App may stay open for days
	bundleFresh     = 20 * time.Second   // served as is
	bundleStale     = 30 * time.Minute   // served at once, refreshed behind
	appScriptTimout = 90 * time.Second
)

// AppGateway passes the app's calls to the script.
type AppGateway struct {
	// OnOK is called after every call the script answered (the rollout
	// counts how long the app has been going through the server).
	OnOK func()

	// Admins is the team: for a few admin actions the chatId in the query is
	// the person acted upon, not the caller.
	Admins map[int64]string

	// Boards lets the app show a resident their board from the platform.
	Boards AppBoardSource
	// Library: the platform's knowledge base and files for residents.
	Library AppLibrarySource
	// Sync writes app results (tests, calendar) into the platform storage.
	Sync AppSyncSource
	// Ops keeps the journal of every change of club data.
	Ops AppOpsLog
	// Funnel: checklist progress and the lead's way to разбор.
	Funnel *LeadFunnel
	// Avatars: whose Telegram photos the app may show; TGBase for tests.
	Avatars AppAvatarSource
	TGBase  string
	avatars map[int64]*avatarEntry
	avIDs   map[int64]bool
	avIDsAt time.Time

	// Writes: club changes go to the server first, then to the script.
	Writes *ClubWrites
	// Claims answers «Я резидент BS» (resident_claim.go).
	Claims *ResidentClaims
	// Stage says who answers the app's bundle: "sheet", "shadow" (the script,
	// compared daily with the server) or "server" (migration.go).
	Stage func(ctx context.Context) string
	// Club is the server's club data the bundle is built from.
	Club     AppBundleSource
	aux      map[string]*cachedBundle
	auxBusy  map[string]bool
	calls    map[string]*scriptCall // the script's bundle being fetched, per person
	gen      int                    // bumped when data changes: older fetches are not kept
	srvStats struct{ served, fallbacks, interim int }

	// Done tells which meetings already happened (from the import).
	Done    AppDoneSource
	done    map[string]time.Time
	doneImp map[string]bool
	doneAt  time.Time

	// Fallback: the script's relay deployment (the bot's), see fallbackURL.
	Fallback func() string

	token     string
	scriptURL string
	client    *http.Client
	now       func() time.Time

	mu      sync.Mutex
	bundles map[string]*cachedBundle
	stats   appStats
}

type cachedBundle struct {
	body       []byte
	at         time.Time
	refreshing bool
	stale      bool // data changed since: served, but fetched again behind
}

type appStats struct {
	OK        int       `json:"ok"`
	Failed    int       `json:"failed"`
	Refused   int       `json:"refused"`
	FirstOK   time.Time `json:"firstOk"`
	LastOK    time.Time `json:"lastOk"`
	Users     map[int64]bool
	CacheHits int `json:"cacheHits"`
}

// AppScriptURL: the script deployment the app gateway calls.
func AppScriptURL() string {
	if u := strings.TrimSpace(os.Getenv("APP_SCRIPT_URL")); u != "" {
		return u
	}
	return DefaultAppScriptURL
}

func NewAppGateway(token, scriptURL string) *AppGateway {
	if strings.TrimSpace(scriptURL) == "" {
		scriptURL = DefaultAppScriptURL
	}
	return &AppGateway{
		token: strings.TrimSpace(token), scriptURL: scriptURL,
		client:  &http.Client{Timeout: appScriptTimout}, // follows the script's redirect to its answer
		now:     time.Now,
		bundles: map[string]*cachedBundle{},
		stats:   appStats{Users: map[int64]bool{}},
	}
}

type AppGatewayModule struct{ g *AppGateway }

func NewAppGatewayModule(g *AppGateway) *AppGatewayModule { return &AppGatewayModule{g: g} }

func (m *AppGatewayModule) Register(r *gin.Engine) {
	r.GET("/api/v1/app/call", appGzip, m.g.Call)
	r.POST("/api/v1/app/post", m.g.Post)
	r.GET("/api/v1/app/myboard", appGzip, m.g.MyBoard)
	r.GET("/api/v1/app/library", appGzip, m.g.Library_)
	r.POST("/api/v1/app/mytests", m.g.MyTests)
	r.POST("/api/v1/app/message", m.g.Message)
	r.GET("/api/v1/app/checklists", appGzip, m.g.Guides)
	r.GET("/api/v1/app/guides", appGzip, m.g.Guides)
	r.GET("/api/v1/app/guide/:id", appGzip, m.g.Guide)
	r.POST("/api/v1/app/guide/:id/send", m.g.SendGuide)
	r.GET("/api/v1/public/guide/:id", m.g.PublicGuidePDF)
	r.POST("/api/v1/app/ckprogress", m.g.CkProgress)
	r.GET("/api/v1/public/leadmagnet/:key", m.g.LeadMagnet)
	r.GET("/api/v1/app/mycal", appGzip, m.g.MyCal)
	r.PUT("/api/v1/app/mycal", m.g.MyCal)
	r.GET("/api/v1/app/file/:id", m.g.LibraryFile)
	r.GET("/api/v1/app/avatar/:id", m.g.Avatar)
	r.GET("/api/v1/app/referral", appGzip, m.g.Referral)
	r.GET("/api/v1/app/slots", appGzip, m.g.Slots)
	r.POST("/api/v1/app/book", m.g.Book)
	r.POST("/api/v1/app/book/cancel", m.g.BookCancel)
	r.POST("/api/v1/bot/claim", m.g.ClaimFromScript) // resident_claim.go, signed by the script
}

// AppSign is the signature the script checks on calls from the server:
// hex(HMAC-SHA256("app|" + action + "|" + chatId + "|" + ts, bot token)).
func AppSign(token, action, chatID, ts string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("app|" + action + "|" + chatID + "|" + ts))
	return hex.EncodeToString(m.Sum(nil))
}

func verifyAppInitData(initData, token string, now time.Time) (*platformTgUser, error) {
	q, err := url.ParseQuery(initData)
	if err != nil || q.Get("hash") == "" {
		return nil, errors.New("no Telegram data")
	}
	vals := map[string]string{}
	for k := range q {
		vals[k] = q.Get(k)
	}
	hash := vals["hash"]
	delete(vals, "hash")
	m := hmac.New(sha256.New, []byte("WebAppData"))
	m.Write([]byte(token))
	if !checkTelegramHash(vals, m.Sum(nil), hash) {
		return nil, errors.New("bad signature")
	}
	ts, err := strconv.ParseInt(vals["auth_date"], 10, 64)
	if err != nil || now.Sub(time.Unix(ts, 0)) > appMaxAge || time.Unix(ts, 0).Sub(now) > 5*time.Minute {
		return nil, errors.New("Telegram data expired, reopen the app")
	}
	var u struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
	}
	if err := json.Unmarshal([]byte(vals["user"]), &u); err != nil || u.ID <= 0 {
		return nil, errors.New("no user")
	}
	return &platformTgUser{ID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username}, nil
}

func (g *AppGateway) identify(c *gin.Context, initData string) (*platformTgUser, bool) {
	if g.token == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram_not_configured"})
		return nil, false
	}
	u, err := verifyAppInitData(initData, g.token, g.now())
	if err != nil {
		g.mu.Lock()
		g.stats.Refused++
		g.mu.Unlock()
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not_telegram", "detail": err.Error()})
		return nil, false
	}
	return u, true
}

func (g *AppGateway) note(ok bool, user int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ok {
		if g.OnOK != nil {
			go g.OnOK()
		}
		g.stats.OK++
		if g.stats.FirstOK.IsZero() {
			g.stats.FirstOK = g.now()
		}
		g.stats.LastOK = g.now()
		g.stats.Users[user] = true
	} else {
		g.stats.Failed++
	}
}

// Stats: how the app's calls went since the server started.
func (g *AppGateway) Stats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{"ok": g.stats.OK, "failed": g.stats.Failed, "refused": g.stats.Refused,
		"users": len(g.stats.Users), "cacheHits": g.stats.CacheHits, "firstOk": g.stats.FirstOK, "lastOk": g.stats.LastOK}
}

func fullName(u *platformTgUser) string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

// params builds the script's query: the page's own parameters, with the
// identity replaced by the checked one and the server's signature added.
// Admin actions whose chatId names the person acted upon (a subscriber to
// ban, to move to residents, to update).
var appTargetActions = map[string]bool{"banFromChannel": true, "convertToResident": true, "updateSubscriber": true}

func (g *AppGateway) params(in url.Values, action string, u *platformTgUser) url.Values {
	out := url.Values{}
	for k, v := range in {
		if k == "_tg" || k == "chatId" || k == "userName" || k == "userTg" || strings.HasPrefix(k, "_srv") {
			continue
		}
		out[k] = v
	}
	cid := strconv.FormatInt(u.ID, 10)
	if _, admin := g.Admins[u.ID]; admin && appTargetActions[action] && in.Get("chatId") != "" {
		cid = in.Get("chatId")
	}
	if n := fullName(u); n != "" {
		out.Set("userName", n)
	}
	if u.Username != "" {
		out.Set("userTg", u.Username)
	}
	return g.signed(out, action, cid)
}

// signed adds the action, the chatId and a fresh signature to a query whose
// identity is already settled (a write sent again later).
func (g *AppGateway) signed(in url.Values, action, cid string) url.Values {
	out := url.Values{}
	for k, v := range in {
		if k == "_tg" || strings.HasPrefix(k, "_srv") {
			continue
		}
		out[k] = v
	}
	ts := strconv.FormatInt(g.now().Unix(), 10)
	out.Set("action", action)
	out.Set("chatId", cid)
	out.Set("_srv_ts", ts)
	out.Set("_srv_sig", AppSign(g.token, action, cid, ts))
	return out
}

func (g *AppGateway) get(ctx context.Context, q url.Values) ([]byte, error) {
	return g.getAt(ctx, g.scriptURL, q)
}

// fallbackURL: the bot's deployment of the same script (the relay), when it
// is another one than the app's. The script's self-update moves the relay to
// a new version first; a club write the app's deployment does not know yet
// goes there (ClubWrites.deliver).
func (g *AppGateway) fallbackURL() string {
	if g.Fallback == nil {
		return ""
	}
	u := strings.TrimSpace(g.Fallback())
	if u == "" || strings.Split(u, "?")[0] == strings.Split(g.scriptURL, "?")[0] {
		return ""
	}
	return strings.Split(u, "?")[0]
}

func (g *AppGateway) getAt(ctx context.Context, base string, q url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, fmt.Errorf("%w: script unreachable", errScriptNoAnswer)
		}
		return nil, errors.New("script unreachable")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errScriptNoAnswer, err)
	}
	if resp.StatusCode >= 500 || (resp.StatusCode < 400 && !json.Valid(b)) {
		// The script may have run before it failed: whether it wrote is unknown.
		return nil, fmt.Errorf("%w: script answered %d", errScriptNoAnswer, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, errors.New("script answered " + strconv.Itoa(resp.StatusCode))
	}
	return b, nil
}

// Call godoc
// @Summary  A call of the Telegram app, passed to the script
// @Description  Query: action and its parameters, plus _tg = Telegram.WebApp.initData. The caller is the Telegram user from initData; chatId in the query is ignored.
// @Tags     app
// @Router   /api/v1/app/call [get]
func (g *AppGateway) Call(c *gin.Context) {
	in := c.Request.URL.Query()
	action := in.Get("action")
	if action == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no action"})
		return
	}
	u, ok := g.identify(c, in.Get("_tg"))
	if !ok {
		return
	}
	q := g.params(in, action, u)
	if action == "getBotCache" {
		g.bundle(c, q, u, in.Get("fresh") == "1")
		return
	}
	if action == "checkUserRole" && g.serverRole(c, u) {
		return
	}
	if action == "requestResident" && g.claimFromApp(c, u) {
		return
	}
	if pg.ClubWriteActions[action] && g.Writes != nil {
		// Club data: the server first, then the sheet (app_writes.go)
		_, team := g.Admins[u.ID]
		body := g.Writes.Do(c.Request.Context(), "app", u, action, q, team)
		g.dropBundles()
		g.logOp(c.Request.Context(), "app", u, action, q, body)
		if action == "confirmMeeting" || action == "markAttendance" {
			g.noteDone(action, map[string]string{"res": in.Get("res"), "date": in.Get("date"), "time": in.Get("time"), "names": in.Get("names")})
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		return
	}
	body, err := g.get(c.Request.Context(), q)
	g.note(err == nil, u.ID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// Anything but a read may have changed the data every bundle is built from.
	// The Telegram photo the app saves on every open changes no club data.
	if !strings.HasPrefix(action, "get") && !strings.HasPrefix(action, "check") && action != "saveAvatar" {
		g.dropBundles()
		g.logOp(c.Request.Context(), "app", u, action, q, body)
	}
	if action == "confirmMeeting" || action == "markAttendance" {
		g.noteDone(action, map[string]string{"res": in.Get("res"), "date": in.Get("date"), "time": in.Get("time"), "names": in.Get("names")})
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

func (g *AppGateway) dropBundles() {
	g.mu.Lock()
	g.bundles = map[string]*cachedBundle{}
	g.gen++
	for _, a := range g.aux { // the script's part is refreshed on the next open
		a.stale = true
	}
	g.mu.Unlock()
}

// bundle hands out the person's data bundle: a fresh copy at once, an older
// one at once while a new one is fetched, or waits for the script.
func (g *AppGateway) bundle(c *gin.Context, q url.Values, u *platformTgUser, force bool) {
	if g.Stage != nil && g.Club != nil && g.Stage(c.Request.Context()) == StageServer {
		if g.serveServerBundle(c, q, u, force) {
			return
		}
		// The server could not build it: the script answers, as before.
	}
	key := q.Get("chatId")
	g.mu.Lock()
	b := g.bundles[key]
	var body []byte
	age := time.Duration(-1)
	if b != nil && !force {
		body, age = b.body, g.now().Sub(b.at)
	}
	refresh := b != nil && !force && age > bundleFresh && age < bundleStale && !b.refreshing
	if refresh {
		b.refreshing = true
	}
	if body != nil && age < bundleStale {
		g.stats.CacheHits++
	}
	g.mu.Unlock()

	if body != nil && age >= 0 && age < bundleStale {
		c.Header("X-BS-Bundle-Age", strconv.Itoa(int(age.Seconds())))
		c.Data(http.StatusOK, "application/json; charset=utf-8", g.forUser(u.ID, hideDone(body, g.doneSet(c.Request.Context()), g.now())))
		if refresh {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), appScriptTimout)
				defer cancel()
				nq := g.params(q, "getBotCache", u)
				fresh, err := g.get(ctx, nq)
				g.mu.Lock()
				if cur := g.bundles[key]; cur != nil {
					cur.refreshing = false
					if err == nil {
						cur.body, cur.at = fresh, g.now()
					}
				}
				g.mu.Unlock()
				if err != nil {
					log.Printf("app bundle refresh: %v", err)
				}
			}()
		}
		return
	}
	// No copy to hand out: the script is asked in the background; in stage
	// shadow the server's own data answers if the script takes longer than
	// scriptBudget, and the script's answer is kept for the next open.
	ctx := c.Request.Context()
	call := g.scriptFetch(key, q, u)
	var budget <-chan time.Time
	interim := g.interimOK(ctx)
	if interim {
		t := time.NewTimer(scriptBudget)
		defer t.Stop()
		budget = t.C
	}
	select {
	case <-call.done:
	case <-budget:
		if g.serveInterim(c, u) {
			return
		}
		select {
		case <-call.done:
		case <-ctx.Done():
			return
		}
	case <-ctx.Done():
		return
	}
	fresh, err := call.body, call.err
	if err != nil {
		// The script is slow or down: an old bundle beats an empty screen.
		g.mu.Lock()
		old := g.bundles[key]
		g.mu.Unlock()
		if old != nil {
			c.Header("X-BS-Bundle-Age", strconv.Itoa(int(g.now().Sub(old.at).Seconds())))
			c.Data(http.StatusOK, "application/json; charset=utf-8", g.forUser(u.ID, hideDone(old.body, g.doneSet(ctx), g.now())))
			return
		}
		if interim && g.serveInterim(c, u) {
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Header("X-BS-Bundle-Age", "0")
	c.Data(http.StatusOK, "application/json; charset=utf-8", g.forUser(u.ID, hideDone(fresh, g.doneSet(ctx), g.now())))
}

// Post godoc
// @Summary  A POST of the Telegram app (pictures), passed to the script
// @Description  Body (text/plain JSON): {bsAction: uploadImage|sendImage, …, _tg: initData}. sendImage goes to the caller only.
// @Tags     app
// @Router   /api/v1/app/post [post]
func (g *AppGateway) Post(c *gin.Context) {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 24<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body too large"})
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	tg, _ := body["_tg"].(string)
	u, ok := g.identify(c, tg)
	if !ok {
		return
	}
	action, _ := body["bsAction"].(string)
	if action != "uploadImage" && action != "sendImage" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown bsAction"})
		return
	}
	cid := strconv.FormatInt(u.ID, 10)
	ts := strconv.FormatInt(g.now().Unix(), 10)
	delete(body, "_tg")
	body["chatId"] = cid // a picture is only ever sent to the one who made it
	body["_srv_ts"] = ts
	body["_srv_sig"] = AppSign(g.token, action, cid, ts)
	out, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.scriptURL, bytes.NewReader(out))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	req.Header.Set("Content-Type", "text/plain;charset=utf-8")
	resp, err := g.client.Do(req)
	if err != nil {
		g.note(false, u.ID)
		c.JSON(http.StatusBadGateway, gin.H{"error": "script unreachable"})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	g.note(resp.StatusCode < 400, u.ID)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", b)
}

// CallAs runs an app action in the script on behalf of a team member (the
// platform's own buttons). The same signed path as the app itself.
func (g *AppGateway) CallAs(ctx context.Context, tgID int64, name, action string, params map[string]string) (map[string]any, error) {
	in := url.Values{}
	for k, v := range params {
		in.Set(k, v)
	}
	u := &platformTgUser{ID: tgID, FirstName: name}
	body, err := g.get(ctx, g.params(in, action, u))
	g.note(err == nil, tgID)
	if err != nil {
		return nil, err
	}
	g.dropBundles()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errors.New("script answered without a result")
	}
	return out, nil
}

// AppOpsLog stores the journal of club data changes.
type AppOpsLog interface {
	LogOp(ctx context.Context, op pg.ClubOp) error
}

// logOp writes one change into the journal; a journal failure never fails the action.
func (g *AppGateway) logOp(ctx context.Context, source string, u *platformTgUser, action string, q url.Values, body []byte) {
	if g.Ops == nil {
		return
	}
	p := map[string]string{}
	for k, v := range q {
		if k == "action" || k == "_tg" || strings.HasPrefix(k, "_srv") || len(v) == 0 {
			continue
		}
		val := v[0]
		if len(val) > 500 {
			val = val[:500]
		}
		p[k] = val
	}
	ok, result := true, ""
	var r map[string]any
	if json.Unmarshal(body, &r) == nil {
		if e, _ := r["error"].(string); e != "" {
			ok, result = false, e
		} else if d, _ := r["deduplicated"].(bool); d {
			result = "дубль, не записан"
		}
	}
	who := ""
	var id int64
	if u != nil {
		id = u.ID
		who = strings.TrimSpace(u.FirstName + " " + u.LastName)
		if n, team := g.Admins[u.ID]; team && n != "" {
			who = n
		}
	}
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = ctx
	if err := g.Ops.LogOp(c, pg.ClubOp{Source: source, TgID: id, Who: who, Action: action, Params: p, OK: ok, Result: result}); err != nil {
		log.Printf("club ops: %v", err)
	}
}
