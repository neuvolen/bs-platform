package gcal

// R71: «Календарь работы» of each person syncs with their own Google
// Calendar. The resident (or a team member, on their own calendar) presses
// «Подключить Google Календарь»: Google's consent through the same OAuth
// client the owner set up in /calendar (the same redirect URI, the state
// starts with "u."), the refresh token is kept encrypted (AES-GCM, the key
// derived from JWT_SECRET). Then, every 5 minutes, when the calendar opens and
// a few seconds after an edit:
//
//   - the platform's hour slots become events (consecutive hours of one type
//     are one event) in «BS · Календарь работы» (made by us) or the calendar
//     the person chose; quiet: no reminders, no e-mails;
//   - an event of ours changed or deleted in Google changes the slots back;
//     when both sides changed the same block, the later edit wins and the
//     conflict is written to gcal_user_log;
//   - the person's other Google events (primary by default) are shown in the
//     platform calendar, read only;
//   - incremental: events.list with syncToken, a full window read when there
//     is none or Google answers 410.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// UserScopes: events (calendar.events), the list of calendars to choose from
// (calendar.readonly) and the person's own «BS · Календарь работы»
// (calendar.app.created: only calendars this app makes).
const UserScopes = "https://www.googleapis.com/auth/calendar.events https://www.googleapis.com/auth/calendar.readonly https://www.googleapis.com/auth/calendar.app.created"

// UserCalName: the dedicated calendar made in the person's Google account.
const UserCalName = "BS · Календарь работы"

// WriteOwn: the write target meaning «our own calendar».
const WriteOwn = "bs"

// SyncEvery: the background pass.
const SyncEvery = 5 * time.Minute

// Window: what is pushed and shown (days back, days ahead).
const (
	WindowBack  = 14
	WindowAhead = 180
)

// ErrNoClient: the owner has not set up the OAuth client in /calendar yet.
var ErrNoClient = errors.New("Подключение станет доступно после настройки Google в /calendar")

// ErrCalConflict: the work calendar changed while the sync wrote it.
var ErrCalConflict = errors.New("calendar_conflict")

// UserConn is one person's connection (gcal_users).
type UserConn struct {
	Scope       string
	Email       string
	RefreshEnc  string
	Granted     string
	ReadCal     string // the calendar shown read only ("primary" by default)
	WriteCal    string // where the platform's blocks go
	WriteName   string
	OwnCal      bool // WriteCal is «BS · Календарь работы»
	SyncRead    string
	SyncWrite   string
	LastSync    time.Time
	Error       string
	NeedConsent bool
	ConnectedAt time.Time
}

// UserEvent: a cached Google event (gcal_user_events). Ours carry Sig, the
// block they were last synced as; Updated is Google's «updated» we last saw
// or wrote, so our own writes coming back are not taken for edits.
type UserEvent struct {
	Cal, ID string
	Ours    bool
	Sig     string
	Summary string
	Start   time.Time
	End     time.Time
	AllDay  bool
	Updated string
}

// UserStore keeps the connections, the cache and the log.
type UserStore interface {
	GcalConns(ctx context.Context) ([]UserConn, error)
	GcalConn(ctx context.Context, scope string) (*UserConn, error)
	GcalSaveConn(ctx context.Context, c UserConn) error
	GcalDeleteConn(ctx context.Context, scope string) error
	GcalEvents(ctx context.Context, scope string) ([]UserEvent, error)
	GcalPutEvent(ctx context.Context, scope string, e UserEvent) error
	GcalDelEvent(ctx context.Context, scope, cal, id string) error
	GcalDelCal(ctx context.Context, scope, cal string, ours bool) error
	GcalLog(ctx context.Context, scope, kind, text string) error
}

// CalDocs reads and writes the work calendar (bs_mycal of the scope).
type CalDocs interface {
	LoadCal(ctx context.Context, scope string) (value string, version int, updated time.Time, err error)
	// SaveCal returns ErrCalConflict when version is not the current one.
	SaveCal(ctx context.Context, scope string, version int, value string) error
}

// UserSync is the per-person sync.
type UserSync struct {
	Owner  *Client // the OAuth client (Creds), endpoints, HTTP, Now
	Store  UserStore
	Docs   CalDocs
	Secret []byte
	// Notify tells the owner (the «Testing» hint).
	Notify func(ctx context.Context, text string)
	// After: called when a sync changed the work calendar (scope).
	After func(scope string)

	mu      sync.Mutex
	tokens  map[string]userTok
	running map[string]bool
	again   map[string]bool
	timers  map[string]*time.Timer
	hinted  map[string]bool
}

type userTok struct {
	at  string
	exp time.Time
}

func (u *UserSync) now() time.Time {
	if u.Owner != nil && u.Owner.Now != nil {
		return u.Owner.Now()
	}
	return time.Now()
}

// Available: the owner's OAuth client exists.
func (u *UserSync) Available(ctx context.Context) bool {
	if u == nil || u.Owner == nil {
		return false
	}
	_, ok := u.Owner.Creds(ctx)
	return ok
}

// ── Secrets ──

func (u *UserSync) key(purpose string) []byte {
	h := sha256.New()
	h.Write([]byte("bs-platform/gcal-user/" + purpose + "\x00"))
	h.Write(u.Secret)
	return h.Sum(nil)
}

func (u *UserSync) seal(plain string) (string, error) {
	if len(u.Secret) == 0 {
		return "", errors.New("нет секрета сервера для шифрования")
	}
	blk, err := aes.NewCipher(u.key("token"))
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(plain), []byte("gcal"))), nil
}

func (u *UserSync) open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	if err != nil || !strings.HasPrefix(sealed, "v1:") {
		return "", errors.New("токен повреждён")
	}
	blk, err := aes.NewCipher(u.key("token"))
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("токен повреждён")
	}
	p, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte("gcal"))
	if err != nil {
		return "", errors.New("токен не расшифровывается: сменился секрет сервера, подключите Google Календарь заново")
	}
	return string(p), nil
}

// State: "u.<payload>.<sig>"; payload = scope|origin|expiry|nonce.
func (u *UserSync) State(scope, origin string) string {
	n := make([]byte, 8)
	_, _ = rand.Read(n)
	p := base64.RawURLEncoding.EncodeToString([]byte(scope + "|" + origin + "|" + strconv.FormatInt(u.now().Add(30*time.Minute).Unix(), 10) + "|" + hex.EncodeToString(n)))
	m := hmac.New(sha256.New, u.key("state"))
	m.Write([]byte(p))
	return "u." + p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// IsUserState: the callback belongs to a person, not to the owner's setup.
func IsUserState(s string) bool { return strings.HasPrefix(s, "u.") }

// ReadState checks the state and returns the scope and the origin.
func (u *UserSync) ReadState(s string) (scope, origin string, err error) {
	parts := strings.Split(strings.TrimPrefix(s, "u."), ".")
	if len(parts) != 2 {
		return "", "", errors.New("ссылка повреждена")
	}
	m := hmac.New(sha256.New, u.key("state"))
	m.Write([]byte(parts[0]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !hmac.Equal(sig, m.Sum(nil)) {
		return "", "", errors.New("ссылка повреждена")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	f := strings.Split(string(raw), "|")
	if len(f) != 4 {
		return "", "", errors.New("ссылка повреждена")
	}
	exp, _ := strconv.ParseInt(f[2], 10, 64)
	if u.now().Unix() > exp {
		return "", "", errors.New("ссылка устарела: нажмите «Подключить Google Календарь» ещё раз")
	}
	return f[0], f[1], nil
}

// ConsentURL: Google's page for this person.
func (u *UserSync) ConsentURL(ctx context.Context, scope, origin, redirect string) (string, error) {
	cr, ok := u.Owner.Creds(ctx)
	if !ok {
		return "", ErrNoClient
	}
	q := url.Values{"client_id": {cr.ID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {UserScopes},
		"access_type": {"offline"}, "prompt": {"consent select_account"}, "state": {u.State(scope, origin)}}
	return u.Owner.AuthURL + "?" + q.Encode(), nil
}

func has(granted, scope string) bool {
	for _, s := range strings.Fields(granted) {
		if s == scope || s == "https://www.googleapis.com/auth/calendar" {
			return true
		}
	}
	return false
}

const (
	scEvents   = "https://www.googleapis.com/auth/calendar.events"
	scReadonly = "https://www.googleapis.com/auth/calendar.readonly"
	scCreated  = "https://www.googleapis.com/auth/calendar.app.created"
)

// Connect finishes the consent: keeps the token, finds or makes the
// calendar. The first sync runs in the background (the caller starts it).
func (u *UserSync) Connect(ctx context.Context, scope, code, redirect string) error {
	cr, ok := u.Owner.Creds(ctx)
	if !ok {
		return ErrNoClient
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Scope   string `json:"scope"`
	}
	if err := u.Owner.form(ctx, url.Values{"code": {code}, "client_id": {cr.ID}, "client_secret": {cr.Secret},
		"redirect_uri": {redirect}, "grant_type": {"authorization_code"}}, &tok); err != nil {
		return err
	}
	if tok.Refresh == "" {
		return errors.New("Google не выдал постоянный доступ: нажмите «Подключить» ещё раз и «Разрешить»")
	}
	if tok.Scope == "" {
		tok.Scope = UserScopes
	}
	if !has(tok.Scope, scEvents) {
		return errors.New("доступ к событиям не выдан: на странице Google поставьте все галочки календаря")
	}
	enc, err := u.seal(tok.Refresh)
	if err != nil {
		return err
	}
	old, _ := u.Store.GcalConn(ctx, scope)
	c := UserConn{Scope: scope, RefreshEnc: enc, Granted: tok.Scope, ReadCal: "primary", ConnectedAt: u.now()}
	if old != nil && old.ReadCal != "" {
		c.ReadCal = old.ReadCal
	}
	u.mu.Lock()
	if u.tokens == nil {
		u.tokens = map[string]userTok{}
	}
	u.tokens[scope] = userTok{tok.Access, u.now().Add(time.Duration(tok.Expires-60) * time.Second)}
	delete(u.hinted, scope)
	u.mu.Unlock()
	if has(c.Granted, scReadonly) {
		var p struct {
			ID string `json:"id"`
		}
		if err := u.call(ctx, &c, http.MethodGet, "/users/me/calendarList/primary", nil, nil, &p); err == nil {
			c.Email = p.ID
		}
	}
	if err := u.ensureWrite(ctx, &c, WriteOwn); err != nil {
		return err
	}
	if err := u.Store.GcalSaveConn(ctx, c); err != nil {
		return err
	}
	if old != nil && old.WriteCal != c.WriteCal {
		_ = u.Store.GcalDelCal(ctx, scope, old.WriteCal, true)
	}
	_ = u.Store.GcalLog(ctx, scope, "connect", "подключён "+c.Email+" → "+c.WriteName)
	return nil
}

// CalendarInfo: one of the person's calendars.
type CalendarInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Primary  bool   `json:"primary"`
	Writable bool   `json:"writable"`
	Ours     bool   `json:"ours"`
}

// Calendars lists the person's calendars (calendar.readonly).
func (u *UserSync) Calendars(ctx context.Context, c *UserConn) ([]CalendarInfo, error) {
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			Summary    string `json:"summary"`
			Override   string `json:"summaryOverride"`
			AccessRole string `json:"accessRole"`
			Primary    bool   `json:"primary"`
		} `json:"items"`
	}
	if err := u.call(ctx, c, http.MethodGet, "/users/me/calendarList", url.Values{"maxResults": {"250"}}, nil, &list); err != nil {
		return nil, err
	}
	var out []CalendarInfo
	for _, it := range list.Items {
		n := it.Summary
		if it.Override != "" {
			n = it.Override
		}
		if it.Primary {
			n += " (основной)"
		}
		out = append(out, CalendarInfo{ID: it.ID, Name: n, Primary: it.Primary,
			Writable: it.AccessRole == "owner" || it.AccessRole == "writer", Ours: strings.TrimSpace(it.Summary) == UserCalName})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Primary && !out[j].Primary })
	return out, nil
}

// ensureWrite sets the write calendar: WriteOwn (find or make «BS ·
// Календарь работы»; without that permission, the primary one) or an id.
func (u *UserSync) ensureWrite(ctx context.Context, c *UserConn, want string) error {
	if want != WriteOwn && want != "" {
		c.WriteCal, c.OwnCal, c.WriteName = want, false, want
		if want == "primary" {
			c.WriteName = "основной календарь"
		}
		if cals, err := u.Calendars(ctx, c); err == nil {
			for _, k := range cals {
				if k.ID == want {
					if !k.Writable {
						return errors.New("в этот календарь нельзя записывать: выберите другой")
					}
					c.WriteName = k.Name
				}
			}
		}
		return nil
	}
	if has(c.Granted, scReadonly) {
		if cals, err := u.Calendars(ctx, c); err == nil {
			for _, k := range cals {
				if k.Ours && k.Writable {
					c.WriteCal, c.OwnCal, c.WriteName = k.ID, true, UserCalName
					return nil
				}
			}
		}
	}
	if has(c.Granted, scCreated) {
		var made struct {
			ID string `json:"id"`
		}
		err := u.call(ctx, c, http.MethodPost, "/calendars", nil,
			map[string]any{"summary": UserCalName, "timeZone": "Asia/Almaty", "description": "Календарь работы с платформы Business Surgery"}, &made)
		if err == nil && made.ID != "" {
			c.WriteCal, c.OwnCal, c.WriteName = made.ID, true, UserCalName
			return nil
		}
		if err != nil {
			log.Printf("gcal user %s: create calendar: %v", c.Scope, err)
		}
	}
	c.WriteCal, c.OwnCal, c.WriteName = "primary", false, "основной календарь"
	return nil
}

// Settings changes the calendars: read (an id) and write (WriteOwn or an id).
func (u *UserSync) Settings(ctx context.Context, scope, read, write string) (*UserConn, error) {
	c, err := u.Store.GcalConn(ctx, scope)
	if err != nil || c == nil {
		return nil, errors.New("Google Календарь не подключён")
	}
	u.lock(scope)
	defer u.unlock(scope)
	if read = strings.TrimSpace(read); read != "" && read != c.ReadCal {
		_ = u.Store.GcalDelCal(ctx, scope, c.ReadCal, false)
		c.ReadCal, c.SyncRead = read, ""
	}
	if write = strings.TrimSpace(write); write != "" && !(write == WriteOwn && c.OwnCal) && write != c.WriteCal {
		old := c.WriteCal
		if err := u.ensureWrite(ctx, c, write); err != nil {
			return nil, err
		}
		if old != c.WriteCal {
			// the blocks move: ours leave the old calendar quietly
			evs, _ := u.Store.GcalEvents(ctx, scope)
			for _, e := range evs {
				if e.Ours && e.Cal == old {
					_ = u.del(ctx, c, old, e.ID)
				}
			}
			_ = u.Store.GcalDelCal(ctx, scope, old, true)
			c.SyncWrite = ""
		}
	}
	if err := u.Store.GcalSaveConn(ctx, *c); err != nil {
		return nil, err
	}
	_ = u.Store.GcalLog(ctx, scope, "settings", "показывать: "+c.ReadCal+", записывать: "+c.WriteName)
	return c, nil
}

// Disconnect revokes the token at Google and forgets everything.
func (u *UserSync) Disconnect(ctx context.Context, scope string) error {
	c, _ := u.Store.GcalConn(ctx, scope)
	if c != nil {
		if rt, err := u.open(c.RefreshEnc); err == nil && rt != "" {
			rev := strings.TrimSuffix(u.Owner.TokenURL, "/token") + "/revoke"
			if err := u.Owner.formTo(ctx, rev, url.Values{"token": {rt}}, nil); err != nil {
				log.Printf("gcal user %s: revoke: %v", scope, err)
			}
		}
	}
	u.mu.Lock()
	delete(u.tokens, scope)
	u.mu.Unlock()
	_ = u.Store.GcalLog(ctx, scope, "disconnect", "отключён, токены удалены")
	return u.Store.GcalDeleteConn(ctx, scope)
}

// ── API as the person ──

func (u *UserSync) token(ctx context.Context, c *UserConn) (string, error) {
	u.mu.Lock()
	if t, ok := u.tokens[c.Scope]; ok && t.at != "" && u.now().Before(t.exp) {
		u.mu.Unlock()
		return t.at, nil
	}
	u.mu.Unlock()
	cr, ok := u.Owner.Creds(ctx)
	if !ok {
		return "", ErrNoClient
	}
	rt, err := u.open(c.RefreshEnc)
	if err != nil {
		c.NeedConsent, c.Error = true, err.Error()
		_ = u.Store.GcalSaveConn(ctx, *c)
		return "", err
	}
	var tok struct {
		Access  string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err := u.Owner.form(ctx, url.Values{"refresh_token": {rt}, "client_id": {cr.ID}, "client_secret": {cr.Secret}, "grant_type": {"refresh_token"}}, &tok); err != nil {
		if NeedsConsent(err) {
			u.lost(ctx, c, err)
		}
		return "", err
	}
	u.mu.Lock()
	if u.tokens == nil {
		u.tokens = map[string]userTok{}
	}
	u.tokens[c.Scope] = userTok{tok.Access, u.now().Add(time.Duration(tok.Expires-60) * time.Second)}
	u.mu.Unlock()
	return tok.Access, nil
}

// lost: the person's access is gone. About 7 days after connecting it is the
// OAuth app left in «Testing»: the owner is told how to publish it.
func (u *UserSync) lost(ctx context.Context, c *UserConn, err error) {
	c.NeedConsent = true
	c.Error = "Google отозвал доступ: подключите Google Календарь заново"
	_ = u.Store.GcalSaveConn(ctx, *c)
	_ = u.Store.GcalLog(ctx, c.Scope, "lost", err.Error())
	age := u.now().Sub(c.ConnectedAt)
	u.mu.Lock()
	told := u.hinted[c.Scope]
	if u.hinted == nil {
		u.hinted = map[string]bool{}
	}
	u.hinted[c.Scope] = true
	u.mu.Unlock()
	if !told && age > 6*24*time.Hour && age < 8*24*time.Hour && u.Notify != nil {
		u.Notify(ctx, "📅 Google отключил календарь участника ровно через неделю после подключения. Так бывает, когда приложение Google в режиме «Testing».\n\n"+
			"Откройте console.cloud.google.com/auth/audience под своим аккаунтом → «Publish app» → «Confirm». Статус должен стать «In production». После этого участнику нужно один раз нажать «Подключить Google Календарь» снова.")
	}
}

func (u *UserSync) call(ctx context.Context, c *UserConn, method, path string, q url.Values, in, out any) error {
	at, err := u.token(ctx, c)
	if err != nil {
		return err
	}
	st, err := apiDo(ctx, u.Owner.HTTP, u.Owner.API, at, method, path, q, in, out)
	if st == http.StatusUnauthorized {
		u.mu.Lock()
		delete(u.tokens, c.Scope)
		u.mu.Unlock()
	}
	return err
}

func (u *UserSync) del(ctx context.Context, c *UserConn, cal, id string) error {
	err := u.call(ctx, c, http.MethodDelete, "/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(id), quiet(), nil, nil)
	var ge *googleErr
	if errors.As(err, &ge) && (ge.Status == http.StatusGone || ge.Status == http.StatusNotFound) {
		return nil
	}
	return err
}

// ── The work calendar as blocks ──

// TypeNames: the platform's slot types.
var TypeNames = map[string]string{"work": "Работа", "meet": "Встреча", "focus": "Фокус-блок", "rest": "Отдых", "busy": "Занято"}

var typeColors = map[string]string{"work": "9", "meet": "8", "focus": "10", "rest": "2", "busy": "11"}

// Block: hours [H0, H1) of one day, one type and note.
type Block struct {
	Date   string // YYYY-MM-DD
	H0, H1 int
	Type   string
	Note   string
}

func (b Block) Sig() string { return fmt.Sprintf("%s|%d|%d|%s|%s", b.Date, b.H0, b.H1, b.Type, b.Note) }

func (b Block) Summary() string {
	n := TypeNames[b.Type]
	if n == "" {
		n = b.Type
	}
	if b.Note != "" {
		return n + " · " + b.Note
	}
	return n
}

func (b Block) times() (time.Time, time.Time, bool) {
	d, err := time.ParseInLocation("2006-01-02", b.Date, club.Almaty)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return d.Add(time.Duration(b.H0) * time.Hour), d.Add(time.Duration(b.H1) * time.Hour), true
}

// calDoc: bs_mycal with every other field kept as it is.
type calDoc struct {
	raw   map[string]json.RawMessage
	slots map[string]string
	notes map[string]string
}

func parseCal(v string) *calDoc {
	d := &calDoc{raw: map[string]json.RawMessage{}, slots: map[string]string{}, notes: map[string]string{}}
	_ = json.Unmarshal([]byte(v), &d.raw)
	if d.raw == nil {
		d.raw = map[string]json.RawMessage{}
	}
	var s map[string]any
	if json.Unmarshal(d.raw["slots"], &s) == nil {
		for k, x := range s {
			if t, ok := x.(string); ok && t != "" {
				d.slots[k] = t
			}
		}
	}
	var n map[string]any
	if json.Unmarshal(d.raw["notes"], &n) == nil {
		for k, x := range n {
			if t, ok := x.(string); ok && t != "" {
				d.notes[k] = t
			}
		}
	}
	return d
}

func (d *calDoc) json() string {
	d.raw["slots"], _ = json.Marshal(d.slots)
	d.raw["notes"], _ = json.Marshal(d.notes)
	b, _ := json.Marshal(d.raw)
	return string(b)
}

func slotKey(date string, h int) string { return date + "|" + strconv.Itoa(h) }

// Blocks groups the slots: consecutive hours of one type; an hour with
// another note starts a new block.
func (d *calDoc) Blocks() []Block {
	byDay := map[string][]int{}
	for k, t := range d.slots {
		date, hs, ok := strings.Cut(k, "|")
		h, err := strconv.Atoi(hs)
		if !ok || err != nil || h < 0 || h > 23 || len(date) != 10 || t == "" {
			continue
		}
		byDay[date] = append(byDay[date], h)
	}
	var out []Block
	for date, hs := range byDay {
		sort.Ints(hs)
		var cur *Block
		for _, h := range hs {
			t, n := d.slots[slotKey(date, h)], d.notes[slotKey(date, h)]
			if cur != nil && cur.H1 == h && cur.Type == t && (n == "" || n == cur.Note) {
				cur.H1 = h + 1
				continue
			}
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Block{Date: date, H0: h, H1: h + 1, Type: t, Note: n}
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sig() < out[j].Sig() })
	return out
}

func (d *calDoc) remove(b Block) {
	for h := b.H0; h < b.H1 && h < 24; h++ {
		k := slotKey(b.Date, h)
		if d.slots[k] == b.Type {
			delete(d.slots, k)
			delete(d.notes, k)
		}
	}
}

func (d *calDoc) put(b Block) {
	for h := b.H0; h < b.H1 && h < 24; h++ {
		k := slotKey(b.Date, h)
		d.slots[k] = b.Type
		delete(d.notes, k)
	}
	if b.Note != "" && b.H0 < 24 {
		d.notes[slotKey(b.Date, b.H0)] = b.Note
	}
}

func sigBlock(sig string) (Block, bool) {
	p := strings.SplitN(sig, "|", 5)
	if len(p) != 5 {
		return Block{}, false
	}
	h0, e0 := strconv.Atoi(p[1])
	h1, e1 := strconv.Atoi(p[2])
	if e0 != nil || e1 != nil {
		return Block{}, false
	}
	return Block{Date: p[0], H0: h0, H1: h1, Type: p[3], Note: p[4]}, true
}

// ── Google events ──

type gEvent struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	Updated     string    `json:"updated"`
	Start       EventTime `json:"start"`
	End         EventTime `json:"end"`
	Transparent string    `json:"transparency"`
	Ext         struct {
		Private map[string]string `json:"private"`
	} `json:"extendedProperties"`
}

func (e gEvent) ours() bool { return e.Ext.Private["bs"] == "mycal" }

func (e gEvent) span() (time.Time, time.Time, bool) {
	if e.Start.Date != "" {
		s, err1 := time.ParseInLocation("2006-01-02", e.Start.Date, club.Almaty)
		en, err2 := time.ParseInLocation("2006-01-02", e.End.Date, club.Almaty)
		if err1 != nil || err2 != nil {
			return time.Time{}, time.Time{}, true
		}
		return s, en, true
	}
	s, _ := time.Parse(time.RFC3339, e.Start.DateTime)
	en, _ := time.Parse(time.RFC3339, e.End.DateTime)
	return s, en, false
}

// block: our event read back as a block (start day, whole hours).
func (e gEvent) block() (Block, bool) {
	s, en, allDay := e.span()
	if allDay || s.IsZero() {
		return Block{}, false
	}
	s, en = s.In(club.Almaty), en.In(club.Almaty)
	day := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, club.Almaty)
	h0 := s.Hour()
	h1 := 24
	if en.Before(day.Add(24 * time.Hour)) {
		h1 = en.Hour()
		if en.Minute() > 0 || en.Second() > 0 {
			h1++
		}
	}
	if h1 <= h0 {
		h1 = h0 + 1
	}
	t := e.Ext.Private["bsType"]
	if TypeNames[t] == "" {
		t = "work"
	}
	note := strings.TrimSpace(e.Summary)
	for id, n := range TypeNames {
		if note == n {
			note = ""
			if e.Ext.Private["bsType"] == "" {
				t = id
			}
			break
		}
		if strings.HasPrefix(note, n+" · ") {
			note = strings.TrimSpace(strings.TrimPrefix(note, n+" · "))
			if e.Ext.Private["bsType"] == "" {
				t = id
			}
			break
		}
	}
	if len([]rune(note)) > 200 {
		note = string([]rune(note)[:200])
	}
	return Block{Date: day.Format("2006-01-02"), H0: h0, H1: h1, Type: t, Note: note}, true
}

func (u *UserSync) eventBody(b Block) map[string]any {
	s, e, _ := b.times()
	body := map[string]any{
		"summary":            b.Summary(),
		"description":        "Из «Календаря работы» Business Surgery. Изменения здесь вернутся на платформу.",
		"start":              map[string]string{"dateTime": s.Format(time.RFC3339), "timeZone": "Asia/Almaty"},
		"end":                map[string]string{"dateTime": e.Format(time.RFC3339), "timeZone": "Asia/Almaty"},
		"reminders":          Quiet(),
		"colorId":            typeColors[b.Type],
		"extendedProperties": map[string]any{"private": map[string]string{"bs": "mycal", "bsType": b.Type}},
	}
	if b.Type == "rest" {
		body["transparency"] = "transparent"
	} else {
		body["transparency"] = "opaque"
	}
	return body
}

// ── The sync ──

func (u *UserSync) lock(scope string) {
	for {
		u.mu.Lock()
		if u.running == nil {
			u.running = map[string]bool{}
		}
		if !u.running[scope] {
			u.running[scope] = true
			u.mu.Unlock()
			return
		}
		u.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
}

func (u *UserSync) unlock(scope string) {
	u.mu.Lock()
	delete(u.running, scope)
	u.mu.Unlock()
}

// Touch: sync soon (a few seconds after the last edit).
func (u *UserSync) Touch(scope string, after time.Duration) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.timers == nil {
		u.timers = map[string]*time.Timer{}
	}
	if t := u.timers[scope]; t != nil {
		t.Stop()
	}
	u.timers[scope] = time.AfterFunc(after, func() {
		u.mu.Lock()
		delete(u.timers, scope)
		u.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := u.SyncOne(ctx, scope); err != nil && !errors.Is(err, errNoConn) {
			log.Printf("gcal user %s: %v", scope, err)
		}
	})
}

// Opened: the calendar was opened; sync if the last pass is older than a minute.
func (u *UserSync) Opened(ctx context.Context, scope string) {
	if u == nil {
		return
	}
	c, _ := u.Store.GcalConn(ctx, scope)
	if c != nil && !c.NeedConsent && u.now().Sub(c.LastSync) > time.Minute {
		u.Touch(scope, 200*time.Millisecond)
	}
}

var errNoConn = errors.New("no connection")

// Result of one pass.
type Result struct {
	Pulled, Inserted, Patched, Deleted, Conflicts int
	DocChanged                                    bool
}

// Loop: every SyncEvery, every connection.
func (u *UserSync) Loop(ctx context.Context) {
	t := time.NewTicker(SyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		u.SyncAll(ctx)
	}
}

func (u *UserSync) SyncAll(ctx context.Context) {
	cs, err := u.Store.GcalConns(ctx)
	if err != nil {
		log.Printf("gcal users: %v", err)
		return
	}
	for _, c := range cs {
		if c.NeedConsent {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if _, err := u.SyncOne(cctx, c.Scope); err != nil {
			log.Printf("gcal user %s: %v", c.Scope, err)
		}
		cancel()
	}
}

// SyncOne: one full pass for a person: pull Google, apply its edits to the
// work calendar, push the work calendar.
func (u *UserSync) SyncOne(ctx context.Context, scope string) (Result, error) {
	u.lock(scope)
	defer u.unlock(scope)
	var res Result
	c, err := u.Store.GcalConn(ctx, scope)
	if err != nil {
		return res, err
	}
	if c == nil {
		return res, errNoConn
	}
	if c.NeedConsent {
		return res, nil
	}
	err = u.pass(ctx, c, &res)
	c.LastSync = u.now()
	if err != nil {
		if !c.NeedConsent {
			c.Error = "синхронизация не прошла: " + err.Error()
			if len(c.Error) > 300 {
				c.Error = c.Error[:300]
			}
		}
	} else {
		c.Error = ""
	}
	if serr := u.Store.GcalSaveConn(ctx, *c); serr != nil && err == nil {
		err = serr
	}
	if res.DocChanged && u.After != nil {
		u.After(scope)
	}
	return res, err
}

func (u *UserSync) window() (time.Time, time.Time) {
	n := u.now().In(club.Almaty)
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, club.Almaty)
	return day.AddDate(0, 0, -WindowBack), day.AddDate(0, 0, WindowAhead)
}

func (u *UserSync) pass(ctx context.Context, c *UserConn, res *Result) error {
	evs, err := u.Store.GcalEvents(ctx, c.Scope)
	if err != nil {
		return err
	}
	recs := map[string]UserEvent{} // ours in the write calendar, by id
	for _, e := range evs {
		if e.Ours && e.Cal == c.WriteCal {
			recs[e.ID] = e
		}
	}
	// 1. Pull: the write calendar (ours and the person's own), then the read one.
	changed := map[string]*gEvent{} // ours edited or deleted in Google
	cals := []string{c.WriteCal}
	if c.ReadCal != "" && c.ReadCal != c.WriteCal {
		cals = append(cals, c.ReadCal)
	}
	for _, cal := range cals {
		tok := &c.SyncWrite
		if cal != c.WriteCal {
			tok = &c.SyncRead
		}
		items, full, next, err := u.list(ctx, c, cal, *tok)
		if err != nil {
			return err
		}
		if full {
			_ = u.Store.GcalDelCal(ctx, c.Scope, cal, false)
		}
		from, to := u.window()
		for i := range items {
			e := items[i]
			res.Pulled++
			if cal == c.WriteCal && e.ours() {
				r, known := recs[e.ID]
				if known && r.Updated == e.Updated {
					continue // our own write coming back
				}
				if !known && e.Status == "cancelled" {
					continue
				}
				changed[e.ID] = &e
				continue
			}
			if e.Status == "cancelled" {
				_ = u.Store.GcalDelEvent(ctx, c.Scope, cal, e.ID)
				continue
			}
			s, en, allDay := e.span()
			if s.IsZero() || !en.After(from) || !s.Before(to) {
				_ = u.Store.GcalDelEvent(ctx, c.Scope, cal, e.ID)
				continue
			}
			title := strings.TrimSpace(e.Summary)
			if title == "" {
				title = "Занято"
			}
			_ = u.Store.GcalPutEvent(ctx, c.Scope, UserEvent{Cal: cal, ID: e.ID, Summary: title, Start: s, End: en, AllDay: allDay, Updated: e.Updated})
		}
		if full && cal == c.WriteCal {
			// a full read shows no deleted events: ours missing from it were deleted in Google
			seen := map[string]bool{}
			for _, e := range items {
				seen[e.ID] = true
			}
			for id, r := range recs {
				if !seen[id] && r.Start.Before(to) && r.End.After(from) && changed[id] == nil {
					changed[id] = &gEvent{ID: id, Status: "cancelled", Updated: u.now().UTC().Format(time.RFC3339)}
				}
			}
		}
		*tok = next
	}
	// 2. Google's edits of ours go into the work calendar (the later edit wins).
	val, ver, docAt, err := u.Docs.LoadCal(ctx, c.Scope)
	if err != nil {
		return err
	}
	doc := parseCal(val)
	desired := map[string]bool{}
	for _, b := range doc.Blocks() {
		desired[b.Sig()] = true
	}
	docDirty := false
	var pending []func() // the cache follows only once the work calendar is saved
	later := func(f func()) { pending = append(pending, f) }
	logLater := func(kind, text string) { later(func() { _ = u.Store.GcalLog(ctx, c.Scope, kind, text) }) }
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		id, e := id, changed[id]
		r, known := recs[id]
		oldB, hadOld := sigBlock(r.Sig)
		platformKept := known && desired[r.Sig]
		gAt, _ := time.Parse(time.RFC3339, e.Updated)
		if known && !platformKept && !docAt.IsZero() && docAt.After(gAt) {
			// both sides changed the block: the platform's edit is later
			res.Conflicts++
			logLater("conflict", fmt.Sprintf("блок %s изменён и на платформе (%s), и в Google (%s): осталась правка платформы",
				r.Sig, docAt.In(club.Almaty).Format("02.01 15:04"), gAt.In(club.Almaty).Format("02.01 15:04")))
			r.Updated = e.Updated
			if e.Status == "cancelled" {
				later(func() { _ = u.Store.GcalDelEvent(ctx, c.Scope, c.WriteCal, id) })
				delete(recs, id)
			} else {
				rr := r
				later(func() { _ = u.Store.GcalPutEvent(ctx, c.Scope, rr) })
				recs[id] = r
			}
			continue
		}
		if known && !platformKept {
			res.Conflicts++
			logLater("conflict", fmt.Sprintf("блок %s изменён и на платформе, и в Google (%s): осталась правка Google",
				r.Sig, gAt.In(club.Almaty).Format("02.01 15:04")))
		}
		if hadOld {
			doc.remove(oldB)
			docDirty = true
		}
		if e.Status == "cancelled" {
			later(func() { _ = u.Store.GcalDelEvent(ctx, c.Scope, c.WriteCal, id) })
			delete(recs, id)
			logLater("google", "удалено в Google: "+r.Sig)
			continue
		}
		nb, ok := e.block()
		if !ok {
			continue
		}
		doc.put(nb)
		docDirty = true
		s, en, _ := e.span()
		rec := UserEvent{Cal: c.WriteCal, ID: id, Ours: true, Sig: nb.Sig(), Summary: e.Summary, Start: s, End: en, Updated: e.Updated}
		later(func() { _ = u.Store.GcalPutEvent(ctx, c.Scope, rec) })
		recs[id] = rec
		logLater("google", "изменено в Google: "+nb.Sig())
	}
	if docDirty {
		nv := doc.json()
		if nv != val {
			if err := u.Docs.SaveCal(ctx, c.Scope, ver, nv); err != nil {
				// the calendar changed meanwhile: the pass repeats from Google's state next time
				c.SyncWrite = ""
				return err
			}
			res.DocChanged = true
		}
	}
	for _, f := range pending {
		f()
	}
	// 3. Push the work calendar.
	from, to := u.window()
	inWin := func(date string) bool {
		d, err := time.ParseInLocation("2006-01-02", date, club.Almaty)
		return err == nil && !d.Before(from) && d.Before(to)
	}
	want := map[string]Block{}
	for _, b := range doc.Blocks() {
		if inWin(b.Date) {
			want[b.Sig()] = b
		}
	}
	bySig := map[string]string{}
	var spare []UserEvent
	recIDs := make([]string, 0, len(recs))
	for id := range recs {
		recIDs = append(recIDs, id)
	}
	sort.Strings(recIDs)
	for _, id := range recIDs {
		r := recs[id]
		b, ok := sigBlock(r.Sig)
		if ok && !inWin(b.Date) {
			continue // outside the window: left as it is
		}
		if ok && want[r.Sig].Date != "" && bySig[r.Sig] == "" {
			bySig[r.Sig] = id
			continue
		}
		spare = append(spare, r)
	}
	var todo []Block
	for sig, b := range want {
		if bySig[sig] == "" {
			todo = append(todo, b)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Sig() < todo[j].Sig() })
	const maxOps = 150
	ops := 0
	for _, b := range todo {
		if ops >= maxOps {
			break
		}
		ops++
		// reuse a spare event of the same day (a moved or retyped block)
		idx := -1
		for i, s := range spare {
			if ob, ok := sigBlock(s.Sig); ok && ob.Date == b.Date && ob.H0 < b.H1 && b.H0 < ob.H1 {
				idx = i
				break
			}
		}
		var out gEvent
		if idx >= 0 {
			s := spare[idx]
			spare = append(spare[:idx], spare[idx+1:]...)
			err := u.call(ctx, c, http.MethodPatch, "/calendars/"+url.PathEscape(c.WriteCal)+"/events/"+url.PathEscape(s.ID), quiet(), u.eventBody(b), &out)
			var ge *googleErr
			if errors.As(err, &ge) && (ge.Status == http.StatusNotFound || ge.Status == http.StatusGone) {
				_ = u.Store.GcalDelEvent(ctx, c.Scope, c.WriteCal, s.ID)
				err = u.call(ctx, c, http.MethodPost, "/calendars/"+url.PathEscape(c.WriteCal)+"/events", quiet(), u.eventBody(b), &out)
				res.Inserted++
			} else {
				res.Patched++
			}
			if err != nil {
				return err
			}
		} else {
			if err := u.call(ctx, c, http.MethodPost, "/calendars/"+url.PathEscape(c.WriteCal)+"/events", quiet(), u.eventBody(b), &out); err != nil {
				return err
			}
			res.Inserted++
		}
		s, en, _ := b.times()
		_ = u.Store.GcalPutEvent(ctx, c.Scope, UserEvent{Cal: c.WriteCal, ID: out.ID, Ours: true, Sig: b.Sig(), Summary: b.Summary(), Start: s, End: en, Updated: out.Updated})
	}
	for _, s := range spare {
		if ops >= maxOps {
			break
		}
		ops++
		if err := u.del(ctx, c, c.WriteCal, s.ID); err != nil {
			return err
		}
		_ = u.Store.GcalDelEvent(ctx, c.Scope, c.WriteCal, s.ID)
		res.Deleted++
	}
	return nil
}

// list: the changes since tok, or (no tok, or 410) the window; full says the
// cache of that calendar is to be replaced.
func (u *UserSync) list(ctx context.Context, c *UserConn, cal, tok string) (items []gEvent, full bool, next string, err error) {
	from, to := u.window()
	for attempt := 0; attempt < 2; attempt++ {
		full = tok == ""
		items = items[:0]
		page := ""
		var gerr error
		for i := 0; i < 40; i++ {
			q := url.Values{"singleEvents": {"true"}, "maxResults": {"250"}}
			if full {
				q.Set("timeMin", from.UTC().Format(time.RFC3339))
				q.Set("timeMax", to.UTC().Format(time.RFC3339))
			} else {
				q.Set("syncToken", tok)
			}
			if page != "" {
				q.Set("pageToken", page)
			}
			var p struct {
				Items []gEvent `json:"items"`
				Next  string   `json:"nextPageToken"`
				Sync  string   `json:"nextSyncToken"`
			}
			if gerr = u.call(ctx, c, http.MethodGet, "/calendars/"+url.PathEscape(cal)+"/events", q, nil, &p); gerr != nil {
				break
			}
			items = append(items, p.Items...)
			if p.Next == "" {
				next = p.Sync
				break
			}
			page = p.Next
		}
		var ge *googleErr
		if errors.As(gerr, &ge) && ge.Status == http.StatusGone && !full {
			tok = "" // the token expired: read the window again
			continue
		}
		return items, full, next, gerr
	}
	return items, full, next, nil
}

// ── What the calendar shows ──

// Status for the button.
type Status struct {
	Available bool      `json:"available"`
	Connected bool      `json:"connected"`
	Email     string    `json:"email,omitempty"`
	Read      string    `json:"read,omitempty"`
	Write     string    `json:"write,omitempty"`
	WriteName string    `json:"writeName,omitempty"`
	Own       bool      `json:"own,omitempty"`
	Need      bool      `json:"need,omitempty"`
	Error     string    `json:"error,omitempty"`
	Last      time.Time `json:"last,omitempty"`
	Hint      string    `json:"hint,omitempty"`
}

// ShownEvent: a Google event on the platform's grid (Almaty, one day).
type ShownEvent struct {
	Date   string `json:"d"`
	H0     int    `json:"h0"`
	H1     int    `json:"h1"`
	From   string `json:"a"`
	To     string `json:"b"`
	Title  string `json:"t"`
	AllDay bool   `json:"ad,omitempty"`
}

// View: the status and the person's Google events. Titles only for the
// person (owner); the team sees «Занято (Google)».
func (u *UserSync) View(ctx context.Context, scope string, owner bool) (Status, []ShownEvent) {
	st := Status{Available: u.Available(ctx)}
	if !st.Available {
		st.Hint = ErrNoClient.Error()
	}
	c, err := u.Store.GcalConn(ctx, scope)
	if err != nil || c == nil {
		return st, nil
	}
	st.Connected, st.Email, st.Read, st.Write, st.WriteName, st.Own = true, c.Email, c.ReadCal, c.WriteCal, c.WriteName, c.OwnCal
	st.Need, st.Error, st.Last = c.NeedConsent, c.Error, c.LastSync
	if !owner {
		st.Email = ""
	}
	evs, _ := u.Store.GcalEvents(ctx, scope)
	from := u.now().In(club.Almaty).AddDate(0, 0, -WindowBack)
	to := u.now().In(club.Almaty).AddDate(0, 0, 62)
	var out []ShownEvent
	for _, e := range evs {
		if e.Ours || !e.End.After(from) || !e.Start.Before(to) {
			continue
		}
		t := e.Summary
		if !owner {
			t = "Занято (Google)"
		}
		s, en := e.Start.In(club.Almaty), e.End.In(club.Almaty)
		for day := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, club.Almaty); day.Before(en) && len(out) < 600; day = day.AddDate(0, 0, 1) {
			se := ShownEvent{Date: day.Format("2006-01-02"), Title: t, AllDay: e.AllDay}
			if e.AllDay {
				se.H0, se.H1 = 0, 24
			} else {
				a, b := s, en
				if a.Before(day) {
					a = day
				}
				if b.After(day.AddDate(0, 0, 1)) {
					b = day.AddDate(0, 0, 1)
				}
				se.H0 = a.Hour()
				se.H1 = 24
				if b.Before(day.AddDate(0, 0, 1)) {
					se.H1 = b.Hour()
					if b.Minute() > 0 {
						se.H1++
					}
				}
				if se.H1 <= se.H0 {
					se.H1 = se.H0 + 1
				}
				se.From, se.To = a.Format("15:04"), b.Format("15:04")
				if se.To == "00:00" {
					se.To = "24:00"
				}
			}
			out = append(out, se)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].H0 < out[j].H0
	})
	return st, out
}
