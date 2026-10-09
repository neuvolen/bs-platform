// Package gcal: Google Calendar of the club's meetings, kept by the server
// (R67). Before the cutover the sheet's Apps Script did it as the owner: a
// meeting marked «Встреча прошла» turned its event green with «✅ » in the
// title. After the cutover nothing did, and the calendar stopped changing.
//
// The server acts as the owner through his own OAuth consent (one link,
// /api/v1/gcal/setup): the refresh token is kept in bot_meta. Every change the
// server makes is quiet: sendUpdates=none (no e-mails to guests) and no
// reminders on the event (the bot reminds the team an hour before instead).
package gcal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Meta keys (bot_meta).
const (
	MetaClient   = "gcal_client"   // {"id","secret"} typed on the setup page
	MetaToken    = "gcal_token"    // {"refresh","at"}
	MetaCalendar = "gcal_calendar" // the calendar id (Business Surgery Meetings)
	MetaError    = "gcal_error"    // last error the owner should see ("" when fine)
	MetaSetupKey = "gcal_setup"    // "<key>|<unix expiry>"
	MetaBackfill = "gcal_backfill" // "done:<RFC3339>": the one-time catch-up ran
)

// Scope: events and the calendar list (to find «Business Surgery Meetings»
// and switch its own default reminders off).
const Scope = "https://www.googleapis.com/auth/calendar"

// DoneColor is «Базилик» (CalendarApp.EventColor.GREEN), as the script used.
const DoneColor = "10"

// DonePrefix starts the title of a meeting that took place.
const DonePrefix = "✅ "

// CalendarName is the club's calendar the script created.
const CalendarName = "Business Surgery Meetings"

// ErrNotConnected: no consent yet (or it was withdrawn).
var ErrNotConnected = errors.New("Google Календарь не подключён")

// Store keeps the settings (bot_meta).
type Store interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Client talks to Google as the owner.
type Client struct {
	Store Store
	HTTP  *http.Client
	// Endpoints (tests point them at a fake server).
	TokenURL, AuthURL, API string
	Now                    func() time.Time

	mu     sync.Mutex
	access string
	expiry time.Time
}

func New(store Store) *Client {
	return &Client{Store: store, HTTP: &http.Client{Timeout: 25 * time.Second},
		TokenURL: "https://oauth2.googleapis.com/token", AuthURL: "https://accounts.google.com/o/oauth2/v2/auth",
		API: "https://www.googleapis.com/calendar/v3", Now: time.Now}
}

// Creds: the OAuth client (GOOGLE_CAL_CLIENT_ID/SECRET, else typed on the setup page).
type Creds struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

func (c *Client) Creds(ctx context.Context) (Creds, bool) {
	if id, sec := strings.TrimSpace(os.Getenv("GOOGLE_CAL_CLIENT_ID")), strings.TrimSpace(os.Getenv("GOOGLE_CAL_CLIENT_SECRET")); id != "" && sec != "" {
		return Creds{ID: id, Secret: sec}, true
	}
	var cr Creds
	if v, _ := c.Store.GetMeta(ctx, MetaClient); v != "" && json.Unmarshal([]byte(v), &cr) == nil && cr.ID != "" && cr.Secret != "" {
		return cr, true
	}
	return Creds{}, false
}

func (c *Client) SaveCreds(ctx context.Context, cr Creds) error {
	cr.ID, cr.Secret = strings.TrimSpace(cr.ID), strings.TrimSpace(cr.Secret)
	if !strings.HasSuffix(cr.ID, ".apps.googleusercontent.com") {
		return errors.New("Client ID должен заканчиваться на .apps.googleusercontent.com")
	}
	if len(cr.Secret) < 10 {
		return errors.New("Client Secret слишком короткий")
	}
	b, _ := json.Marshal(cr)
	return c.Store.SetMeta(ctx, MetaClient, string(b))
}

// Connected: a refresh token is kept.
func (c *Client) Connected(ctx context.Context) bool {
	_, ok := c.refresh(ctx)
	_, okc := c.Creds(ctx)
	return ok && okc
}

func (c *Client) refresh(ctx context.Context) (string, bool) {
	var t struct {
		Refresh string `json:"refresh"`
	}
	v, _ := c.Store.GetMeta(ctx, MetaToken)
	if v == "" || json.Unmarshal([]byte(v), &t) != nil || t.Refresh == "" {
		return "", false
	}
	return t.Refresh, true
}

// ConsentURL: the Google page where the owner allows the calendar.
func (c *Client) ConsentURL(ctx context.Context, redirect, state string) (string, error) {
	cr, ok := c.Creds(ctx)
	if !ok {
		return "", errors.New("нет Client ID")
	}
	q := url.Values{"client_id": {cr.ID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {Scope},
		"access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"true"}, "state": {state}}
	return c.AuthURL + "?" + q.Encode(), nil
}

// Exchange turns the consent code into the refresh token and keeps it.
func (c *Client) Exchange(ctx context.Context, code, redirect string) error {
	cr, ok := c.Creds(ctx)
	if !ok {
		return errors.New("нет Client ID")
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Scope   string `json:"scope"`
	}
	if err := c.form(ctx, url.Values{"code": {code}, "client_id": {cr.ID}, "client_secret": {cr.Secret},
		"redirect_uri": {redirect}, "grant_type": {"authorization_code"}}, &tok); err != nil {
		return err
	}
	if tok.Refresh == "" {
		return errors.New("Google не выдал постоянный доступ: откройте ссылку ещё раз и нажмите «Разрешить»")
	}
	if tok.Scope != "" && !strings.Contains(tok.Scope, "auth/calendar") {
		return errors.New("доступ к календарю не выдан: поставьте галочку «Календарь» на странице Google")
	}
	b, _ := json.Marshal(map[string]string{"refresh": tok.Refresh, "at": c.Now().UTC().Format(time.RFC3339)})
	if err := c.Store.SetMeta(ctx, MetaToken, string(b)); err != nil {
		return err
	}
	c.mu.Lock()
	c.access, c.expiry = tok.Access, c.Now().Add(time.Duration(tok.Expires-60)*time.Second)
	c.mu.Unlock()
	_ = c.Store.SetMeta(ctx, MetaError, "")
	_ = c.Store.SetMeta(ctx, MetaCalendar, "") // found again under this account
	return nil
}

// googleErr is an answer of the token endpoint or the API.
type googleErr struct {
	Status int
	Code   string
	Msg    string
}

func (e *googleErr) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("Google %d: %s %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("Google %d: %s", e.Status, e.Code)
}

func readErr(resp *http.Response, body []byte) error {
	ge := &googleErr{Status: resp.StatusCode}
	var j struct {
		Error any    `json:"error"`
		Desc  string `json:"error_description"`
	}
	if json.Unmarshal(body, &j) == nil {
		switch v := j.Error.(type) {
		case string:
			ge.Code, ge.Msg = v, j.Desc
		case map[string]any:
			ge.Code, _ = v["status"].(string)
			ge.Msg, _ = v["message"].(string)
		}
	}
	if ge.Code == "" {
		ge.Msg = strings.TrimSpace(string(body))
		if len(ge.Msg) > 300 {
			ge.Msg = ge.Msg[:300]
		}
	}
	return ge
}

func (c *Client) form(ctx context.Context, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return readErr(resp, body)
	}
	return json.Unmarshal(body, out)
}

// NeedsConsent: the token was withdrawn or expired (the owner opens the link again).
func NeedsConsent(err error) bool {
	if errors.Is(err, ErrNotConnected) {
		return true
	}
	var ge *googleErr
	return errors.As(err, &ge) && (ge.Code == "invalid_grant" || ge.Code == "unauthorized_client" || ge.Status == http.StatusUnauthorized)
}

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.access != "" && c.Now().Before(c.expiry) {
		a := c.access
		c.mu.Unlock()
		return a, nil
	}
	c.mu.Unlock()
	rt, ok := c.refresh(ctx)
	cr, okc := c.Creds(ctx)
	if !ok || !okc {
		return "", ErrNotConnected
	}
	var tok struct {
		Access  string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err := c.form(ctx, url.Values{"refresh_token": {rt}, "client_id": {cr.ID}, "client_secret": {cr.Secret}, "grant_type": {"refresh_token"}}, &tok); err != nil {
		if NeedsConsent(err) {
			_ = c.Store.SetMeta(ctx, MetaError, "доступ к Google Календарю отозван или истёк: нужно открыть ссылку подключения ещё раз ("+err.Error()+")")
		}
		return "", err
	}
	c.mu.Lock()
	c.access, c.expiry = tok.Access, c.Now().Add(time.Duration(tok.Expires-60)*time.Second)
	c.mu.Unlock()
	return tok.Access, nil
}

// call: one API request; out may be nil.
func (c *Client) call(ctx context.Context, method, path string, q url.Values, in, out any) error {
	at, err := c.token(ctx)
	if err != nil {
		return err
	}
	u := c.API + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+at)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		c.access = ""
		c.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		return readErr(resp, b)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ── Events ──

type EventTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type Attendee struct {
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus,omitempty"`
	Self           bool   `json:"self,omitempty"`
	Organizer      bool   `json:"organizer,omitempty"`
}

type Reminders struct {
	UseDefault bool       `json:"useDefault"`
	Overrides  []Reminder `json:"overrides"`
}

type Reminder struct {
	Method  string `json:"method"`
	Minutes int    `json:"minutes"`
}

type Event struct {
	ID          string     `json:"id,omitempty"`
	Status      string     `json:"status,omitempty"`
	Summary     string     `json:"summary"`
	Description string     `json:"description,omitempty"`
	Location    string     `json:"location,omitempty"`
	ColorID     string     `json:"colorId,omitempty"`
	Start       EventTime  `json:"start"`
	End         EventTime  `json:"end"`
	Attendees   []Attendee `json:"attendees,omitempty"`
	Reminders   *Reminders `json:"reminders,omitempty"`
	HangoutLink string     `json:"hangoutLink,omitempty"`
	Recurring   string     `json:"recurringEventId,omitempty"`
	Creator     struct {
		Email string `json:"email,omitempty"`
		Self  bool   `json:"self,omitempty"`
	} `json:"creator,omitempty"`
}

// StartAt: the event's start (zero for an all-day event).
func (e Event) StartAt() time.Time {
	t, err := time.Parse(time.RFC3339, e.Start.DateTime)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Quiet: no reminders on the event (the bot reminds the team instead).
func Quiet() *Reminders { return &Reminders{UseDefault: false, Overrides: []Reminder{}} }

// IsQuiet: the event already has no reminders.
func (e Event) IsQuiet() bool {
	return e.Reminders != nil && !e.Reminders.UseDefault && len(e.Reminders.Overrides) == 0
}

// IsDone: the event is already shown as held.
func (e Event) IsDone() bool { return strings.HasPrefix(e.Summary, "✅") && e.ColorID == DoneColor }

// Calendar: the club's calendar id, found once by its name (else the primary one).
func (c *Client) Calendar(ctx context.Context) (string, error) {
	if id := strings.TrimSpace(os.Getenv("GCAL_CALENDAR_ID")); id != "" {
		return id, nil
	}
	if v, _ := c.Store.GetMeta(ctx, MetaCalendar); v != "" {
		return v, nil
	}
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			Summary    string `json:"summary"`
			AccessRole string `json:"accessRole"`
			Primary    bool   `json:"primary"`
		} `json:"items"`
	}
	if err := c.call(ctx, http.MethodGet, "/users/me/calendarList", url.Values{"maxResults": {"250"}}, nil, &list); err != nil {
		return "", err
	}
	id := ""
	for _, it := range list.Items {
		if strings.EqualFold(strings.TrimSpace(it.Summary), CalendarName) && (it.AccessRole == "owner" || it.AccessRole == "writer") {
			id = it.ID
			break
		}
	}
	if id == "" {
		id = "primary"
	}
	_ = c.Store.SetMeta(ctx, MetaCalendar, id)
	return id, nil
}

// Events lists single events (no cancelled ones) between from and to.
func (c *Client) Events(ctx context.Context, cal string, from, to time.Time) ([]Event, error) {
	var out []Event
	tok := ""
	for i := 0; i < 20; i++ {
		q := url.Values{"timeMin": {from.UTC().Format(time.RFC3339)}, "timeMax": {to.UTC().Format(time.RFC3339)},
			"singleEvents": {"true"}, "maxResults": {"250"}, "orderBy": {"startTime"}}
		if tok != "" {
			q.Set("pageToken", tok)
		}
		var page struct {
			Items []Event `json:"items"`
			Next  string  `json:"nextPageToken"`
		}
		if err := c.call(ctx, http.MethodGet, "/calendars/"+url.PathEscape(cal)+"/events", q, nil, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Items {
			if e.Status != "cancelled" {
				out = append(out, e)
			}
		}
		if page.Next == "" {
			break
		}
		tok = page.Next
	}
	return out, nil
}

// quiet is every write's query: no e-mails to anyone.
func quiet() url.Values { return url.Values{"sendUpdates": {"none"}} }

// Patch changes some fields of an event, quietly.
func (c *Client) Patch(ctx context.Context, cal, id string, fields map[string]any) error {
	return c.call(ctx, http.MethodPatch, "/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(id), quiet(), fields, nil)
}

// Insert creates an event quietly; meet asks Google for a Meet link.
func (c *Client) Insert(ctx context.Context, cal string, e Event, meet bool) (Event, error) {
	q := quiet()
	body := map[string]any{}
	b, _ := json.Marshal(e)
	_ = json.Unmarshal(b, &body)
	body["reminders"] = Quiet()
	if meet {
		q.Set("conferenceDataVersion", "1")
		body["conferenceData"] = map[string]any{"createRequest": map[string]any{
			"requestId": fmt.Sprintf("bs-%d", c.Now().UnixNano()), "conferenceSolutionKey": map[string]string{"type": "hangoutsMeet"}}}
	}
	var out Event
	err := c.call(ctx, http.MethodPost, "/calendars/"+url.PathEscape(cal)+"/events", q, body, &out)
	return out, err
}

// Delete removes an event quietly.
func (c *Client) Delete(ctx context.Context, cal, id string) error {
	err := c.call(ctx, http.MethodDelete, "/calendars/"+url.PathEscape(cal)+"/events/"+url.PathEscape(id), quiet(), nil, nil)
	var ge *googleErr
	if errors.As(err, &ge) && (ge.Status == http.StatusGone || ge.Status == http.StatusNotFound) {
		return nil
	}
	return err
}

// QuietCalendar switches off the calendar's own default reminders and its
// e-mail notifications for the owner.
func (c *Client) QuietCalendar(ctx context.Context, cal string) error {
	return c.call(ctx, http.MethodPatch, "/users/me/calendarList/"+url.PathEscape(cal), nil,
		map[string]any{"defaultReminders": []Reminder{}, "notificationSettings": map[string]any{"notifications": []any{}}}, nil)
}
