// Package bot receives the Telegram bot's updates on the server.
//
// Stage 1 (now): Telegram sends every update here. The server stores it,
// answers Telegram at once and passes it on to the Google Apps Script bot,
// which still does all the work. Telegram no longer waits on the slow script,
// nothing is lost while the script is down, and every update is on record
// for the server bot that replaces the script step by step.
package bot

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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// AllowedUpdates is what the Apps Script bot has always asked Telegram for.
var AllowedUpdates = []string{"message", "edited_message", "callback_query", "message_reaction"}

// RelayURLPattern: only a published Apps Script web app may be the relay.
var RelayURLPattern = regexp.MustCompile(`^https://script\.google\.com/macros/s/[\w-]+/exec$`)

const (
	metaRelayURL = "relay_url"
	metaAlertAt  = "alert_at"
	maxTries     = 10
	maxAge       = 72 * time.Hour
)

// Service holds the relay workers and talks to Telegram.
type Service struct {
	repo      *pg.BotRepo
	token     string
	apiBase   string // https://api.telegram.org, a fake in tests
	admins    []int64
	client    *http.Client // to the relay: no redirects, Apps Script answers 302
	tgClient  *http.Client
	workers   int
	wake      chan struct{}
	features  featureSet
	testClock bool
	topic     string  // the group's ОТЧЁТЫ topic
	notify    []int64 // who gets the daily comparison
	publicURL string  // https://host of the server: the webhook it sets after the cutover
	fineSink  FineSink
	custdev   CustdevSink
	platform  string // the platform's address for the bot's buttons

	mu        sync.RWMutex
	relayURL  string
	startHook StartHook
	cbHook    CallbackHook
	claimHook CallbackHook // «Я резидент BS» (start_hook.go)
	teamCb    map[string]TeamCallbackHook
	files     sync.Map                    // Telegram file_id of what the server uploaded
	sysCheck  atomic.Pointer[SystemCheck] // R36: the admins' /status (syscheck_hook.go)
	shadowWG  sync.WaitGroup              // background reads of incoming updates (Receive)
	// R38c (outreach.go): anyone's buttons, a shared phone, WhatsApp residents
	pubCb       map[string]TeamCallbackHook
	contactHook ContactHook
	waRoute     WhatsAppRoute
	waSink      WhatsAppSink
}

type Options struct {
	Token     string
	APIBase   string
	Admins    []int64
	Workers   int
	Timeout   time.Duration
	Topic     string  // reports topic, default 9
	TestClock bool    // tests only: /tick may set the time
	Notify    []int64 // daily comparison recipients
	PublicURL string  // https://host the Telegram webhook points to (after the cutover the server sets it)
}

func New(repo *pg.BotRepo, o Options) *Service {
	if o.APIBase == "" {
		o.APIBase = "https://api.telegram.org"
	}
	if o.Workers <= 0 {
		o.Workers = 8
	}
	if o.Topic == "" {
		o.Topic = DefaultReportsTopic
	}
	if o.Timeout <= 0 {
		o.Timeout = 120 * time.Second
	}
	return &Service{
		repo:    repo,
		token:   strings.TrimSpace(o.Token),
		apiBase: strings.TrimRight(o.APIBase, "/"),
		admins:  o.Admins,
		client: &http.Client{
			Timeout:       o.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		tgClient:  &http.Client{Timeout: 30 * time.Second},
		workers:   o.Workers,
		wake:      make(chan struct{}, 1),
		topic:     o.Topic,
		testClock: o.TestClock,
		notify:    o.Notify,
		publicURL: strings.TrimRight(strings.TrimSpace(o.PublicURL), "/"),
	}
}

func (s *Service) Enabled() bool { return s.token != "" }

// Token is the bot token: the sheet signs its calls with it.
func (s *Service) Token() string { return s.token }

// ErrNotUpdate: the body is not a Telegram update.
var ErrNotUpdate = errors.New("not a telegram update")

// WebhookSecret is what Telegram sends back in X-Telegram-Bot-Api-Secret-Token.
// Derived from the bot token, so there is nothing extra to configure.
func (s *Service) WebhookSecret() string {
	m := hmac.New(sha256.New, []byte(s.token))
	m.Write([]byte("bs-webhook"))
	return hex.EncodeToString(m.Sum(nil))[:48]
}

func (s *Service) RelayURL() string {
	if !club.SheetLegacy() {
		return "" // after the cutover nothing goes to the script (club.SheetMode)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.relayURL
}

func (s *Service) SetRelayURL(ctx context.Context, u string) error {
	if err := s.repo.SetMeta(ctx, metaRelayURL, u); err != nil {
		return err
	}
	s.mu.Lock()
	s.relayURL = u
	s.mu.Unlock()
	s.Wake()
	_, _ = s.refreshFeatures(ctx)
	return nil
}

// NotifyIDs: who gets the owner's reports (the daily comparisons).
func (s *Service) NotifyIDs() []int64 { return s.notify }

// Master: who keeps the club's data, "sheet" or "server".
func (s *Service) Master(ctx context.Context) string {
	m, err := s.repo.Club().Master(ctx)
	if err != nil || m == "" {
		return "sheet"
	}
	return m
}

// NoteScript remembers which script version asked last, for diagnostics.
func (s *Service) NoteScript(ctx context.Context, version string) {
	if version == "" {
		return
	}
	if old, _ := s.repo.GetMeta(ctx, "script_version"); old != version {
		_ = s.repo.SetMeta(ctx, "script_version", version)
		_, _ = s.refreshFeatures(ctx)
	}
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start runs the relay workers and the hourly housekeeping until ctx ends.
func (s *Service) Start(ctx context.Context) {
	if u, err := s.repo.GetMeta(ctx, metaRelayURL); err == nil {
		s.mu.Lock()
		s.relayURL = u
		s.mu.Unlock()
	}
	s.loadFeatures(ctx)
	if !club.SheetLegacy() {
		go s.watchWebhook(ctx) // Telegram must call the server itself, not the script (webhook_watch.go)
	}
	for i := 0; i < s.workers; i++ {
		go s.worker(ctx)
	}
	go s.housekeeping(ctx)
}

// Parsed is what the server reads from an update before storing it.
type Parsed struct {
	UpdateID int64
	Kind     string
	ChatID   int64
	OrderKey string
}

// Parse reads the update id, its kind and the conversation it belongs to.
// In a group each member is a conversation of their own, so a slow report
// from one resident does not hold up the others.
func Parse(body []byte) (Parsed, error) {
	var u struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From *struct {
				ID int64 `json:"id"`
			} `json:"from"`
		} `json:"message"`
		Edited *struct {
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From *struct {
				ID int64 `json:"id"`
			} `json:"from"`
		} `json:"edited_message"`
		Callback *struct {
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
			Message *struct {
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
		Reaction *struct {
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			User *struct {
				ID int64 `json:"id"`
			} `json:"user"`
		} `json:"message_reaction"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return Parsed{}, err
	}
	if u.UpdateID == 0 {
		return Parsed{}, errors.New("no update_id")
	}
	p := Parsed{UpdateID: u.UpdateID, Kind: "other"}
	key := func(chat int64, typ string, from int64) {
		p.ChatID = chat
		if (typ == "group" || typ == "supergroup") && from != 0 {
			p.OrderKey = strconv.FormatInt(chat, 10) + ":" + strconv.FormatInt(from, 10)
		} else if chat != 0 {
			p.OrderKey = strconv.FormatInt(chat, 10)
		}
	}
	switch {
	case u.Message != nil:
		p.Kind = "message"
		var from int64
		if u.Message.From != nil {
			from = u.Message.From.ID
		}
		key(u.Message.Chat.ID, u.Message.Chat.Type, from)
	case u.Edited != nil:
		p.Kind = "edited_message"
		var from int64
		if u.Edited.From != nil {
			from = u.Edited.From.ID
		}
		key(u.Edited.Chat.ID, u.Edited.Chat.Type, from)
	case u.Callback != nil:
		p.Kind = "callback_query"
		if u.Callback.Message != nil {
			key(u.Callback.Message.Chat.ID, u.Callback.Message.Chat.Type, u.Callback.From.ID)
		} else {
			p.OrderKey = "u" + strconv.FormatInt(u.Callback.From.ID, 10)
		}
	case u.Reaction != nil:
		p.Kind = "message_reaction"
		var from int64
		if u.Reaction.User != nil {
			from = u.Reaction.User.ID
		}
		key(u.Reaction.Chat.ID, u.Reaction.Chat.Type, from)
	}
	return p, nil
}

// Receive stores an update. The caller answers Telegram right after.
func (s *Service) Receive(ctx context.Context, body []byte) (bool, error) {
	p, err := Parse(body)
	if err != nil {
		return false, ErrNotUpdate
	}
	fresh, err := s.repo.SaveUpdate(ctx, pg.BotUpdate{UpdateID: p.UpdateID, Kind: p.Kind, ChatID: p.ChatID, OrderKey: p.OrderKey, Body: body})
	if err == nil && fresh {
		s.Wake()
		// The server bot reads the message too, without answering anyone.
		s.shadowWG.Add(1)
		go func() {
			defer s.shadowWG.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.shadowUpdate(ctx, body)
			s.noteInbound(ctx, body) // R40b: a lead's message is on record before any answer (lead_inbound.go)
		}()
	}
	return fresh, err
}

// backoff: 5s, 15s, 30s, 1m, 2m, 4m, 8m, 15m, 15m…
func backoff(tries int) time.Duration {
	steps := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	if tries-1 < len(steps) && tries >= 1 {
		return steps[tries-1]
	}
	return 15 * time.Minute
}

func (s *Service) worker(ctx context.Context) {
	idle := time.NewTicker(5 * time.Second)
	defer idle.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		worked := false
		if s.RelayURL() != "" || !club.SheetLegacy() {
			u, err := s.repo.ClaimNext(ctx, s.client.Timeout+30*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("bot relay: claim: %v", err)
				}
			} else if u != nil {
				s.Wake() // more may be waiting: let another worker look
				s.relayOne(ctx, u)
				worked = true
			}
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-idle.C:
		}
	}
}

func (s *Service) relayOne(ctx context.Context, u *pg.BotUpdate) {
	if !club.SheetLegacy() {
		s.handleOwn(ctx, u) // the server is the bot (private.go)
		return
	}
	if u.Tries <= 1 && ((u.Kind == "message" && (s.takeStart(ctx, u.Body) || s.takeContact(ctx, u.Body))) || (u.Kind == "callback_query" && s.takeCallback(ctx, u.Body))) {
		if e := s.repo.MarkRelayed(ctx, u.UpdateID, 204, 0); e != nil {
			log.Printf("bot relay: mark %d: %v", u.UpdateID, e)
		}
		return
	}
	url := s.RelayURL()
	start := time.Now()
	status, err := s.post(ctx, url, u.Body)
	took := time.Since(start)
	if err == nil {
		if e := s.repo.MarkRelayed(ctx, u.UpdateID, status, took); e != nil {
			log.Printf("bot relay: mark %d: %v", u.UpdateID, e)
		}
		return
	}
	giveUp := u.Tries >= maxTries || time.Since(u.Received) > maxAge
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if e := s.repo.MarkFailed(ctx, u.UpdateID, status, msg, backoff(u.Tries), giveUp); e != nil {
		log.Printf("bot relay: mark failed %d: %v", u.UpdateID, e)
	}
	log.Printf("bot relay: update %d try %d: %v", u.UpdateID, u.Tries, err)
}

// post sends the update as Telegram would. Apps Script answers 200 (HtmlService)
// or 302 (ContentService) once doPost has run; both mean delivered.
func (s *Service) post(ctx context.Context, url string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("script answered %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (s *Service) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n++
		s.Tick(ctx, time.Now())
		if n%60 == 0 {
			if err := s.repo.Cleanup(ctx); err != nil {
				log.Printf("bot cleanup: %v", err)
			}
		}
	}
}

// TickResult is what one round of the timed jobs did.
type TickResult struct {
	Features []string          `json:"features"`
	Daily    *DailyOutcome     `json:"daily,omitempty"`
	Meetings []MeetingReminder `json:"meetings,omitempty"`
	Team     []TeamReminder    `json:"team,omitempty"`
	Shadow   *DayResult        `json:"shadow,omitempty"`
	Evening  []string          `json:"evening,omitempty"`
	// Onboarding: residents who got their onboarding message of the day
	Onboarding []string `json:"onboarding,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// Tick runs the timed jobs once: every minute from housekeeping, or on demand.
func (s *Service) Tick(ctx context.Context, now time.Time) TickResult {
	var r TickResult
	fail := func(what string, err error) {
		if err != nil {
			log.Printf("bot %s: %v", what, err)
			r.Errors = append(r.Errors, what+": "+err.Error())
		}
	}
	var err error
	r.Features, err = s.refreshFeatures(ctx)
	fail("features", err)
	s.checkAlert(ctx)
	r.Daily, err = s.maybeDailyCheck(ctx, now)
	fail("daily check", err)
	r.Meetings, err = s.maybeMeetingReminders(ctx, now)
	fail("meetings", err)
	r.Team, err = s.maybeTeamReminders(ctx, now)
	fail("team reminders", err)
	r.Shadow, err = s.maybeDailyShadow(ctx, now)
	fail("shadow", err)
	r.Evening, err = s.maybeEvening(ctx, now)
	fail("evening", err)
	if !club.SheetLegacy() {
		r.Onboarding, err = s.maybeOnboarding(ctx, now)
		fail("onboarding", err)
	}
	return r
}

func (s *Service) maybeEvening(ctx context.Context, now time.Time) ([]string, error) {
	return s.maybeEveningReminder(ctx, now)
}

// TestClock: the tick endpoint may be given a time (tests only).
func (s *Service) TestClock() bool { return s.testClock }

// checkAlert tells the team when updates pile up on the server: the script is
// down or refuses them. At most once in 3 hours.
func (s *Service) checkAlert(ctx context.Context) {
	if s.RelayURL() == "" {
		return
	}
	st, err := s.repo.Stats(ctx)
	if err != nil || (st.OldestWaitingS < 15*60 && st.GaveUp72h == 0) {
		return
	}
	last, _ := s.repo.GetMeta(ctx, metaAlertAt)
	if t, err := time.Parse(time.RFC3339, last); err == nil && time.Since(t) < 3*time.Hour {
		return
	}
	_ = s.repo.SetMeta(ctx, metaAlertAt, time.Now().UTC().Format(time.RFC3339))
	txt := fmt.Sprintf("⚠️ Сервер не может передать сообщения боту в таблице.\n\nЖдут отправки: %d (самое старое %d мин)\nНе доставлены после 10 попыток: %d\nОшибка: %s\n\nСообщения сохранены на сервере и уйдут, когда таблица ответит. Если это надолго: меню BS → «Переподключить бота напрямую».",
		st.Waiting, st.OldestWaitingS/60, st.GaveUp72h, orDash(st.LastError))
	s.sysNote(txt)
}

// sysNote: system events (outages, self-repair, rollout changes) go to the log,
// never to the team's Telegram. The bot only sends business messages.
func (s *Service) sysNote(text string) {
	log.Printf("bot system: %s", strings.ReplaceAll(text, "\n", " "))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ── Telegram API ──

type tgResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (s *Service) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	b, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+"/bot"+s.token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.tgClient.Do(req)
	if err != nil {
		// The error text carries the URL with the token: never pass it on.
		return nil, fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()
	var r tgResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("telegram %s: bad answer (%d)", method, resp.StatusCode)
	}
	if !r.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	return r.Result, nil
}

func (s *Service) SendMessage(ctx context.Context, chatID int64, text string) error {
	_, err := s.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	return err
}

// WebhookInfo is Telegram's view of where the bot's updates go.
type WebhookInfo struct {
	URL              string `json:"url"`
	Pending          int    `json:"pending_update_count"`
	LastErrorDate    int64  `json:"last_error_date,omitempty"`
	LastErrorMessage string `json:"last_error_message,omitempty"`
	MaxConnections   int    `json:"max_connections,omitempty"`
}

func (s *Service) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var w WebhookInfo
	raw, err := s.call(ctx, "getWebhookInfo", map[string]any{})
	if err != nil {
		return w, err
	}
	err = json.Unmarshal(raw, &w)
	return w, err
}

// SetWebhook points Telegram at the server. Updates already waiting in
// Telegram's queue are kept and come here.
func (s *Service) SetWebhook(ctx context.Context, url string) error {
	_, err := s.call(ctx, "setWebhook", map[string]any{
		"url":                  url,
		"secret_token":         s.WebhookSecret(),
		"allowed_updates":      AllowedUpdates,
		"drop_pending_updates": false,
		"max_connections":      40,
	})
	return err
}

// ScriptVersion asks the Apps Script which code version answers at url.
func (s *Service) ScriptVersion(ctx context.Context, url string) (string, error) {
	if !club.SheetLegacy() {
		return "", errors.New("таблица отключена")
	}
	c := &http.Client{Timeout: 30 * time.Second} // follows the redirect to googleusercontent
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"?action=bsVersion", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", errors.New("script unreachable")
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v); err != nil {
		return "", fmt.Errorf("script answered %d without a version", resp.StatusCode)
	}
	return v.Version, nil
}

func (s *Service) Stats(ctx context.Context) (pg.BotStats, error) { return s.repo.Stats(ctx) }

func (s *Service) Retry(ctx context.Context) (int64, error) {
	n, err := s.repo.Retry(ctx)
	if err == nil && n > 0 {
		s.Wake()
	}
	return n, err
}
