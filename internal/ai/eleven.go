package ai

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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// R36: ElevenLabs, the premium voice of the tour.
//
// The owner picks a licensed voice himself (his own designed voice or one
// from the ElevenLabs voice library); the server reads every tour phrase
// with it once, keeps the files in tts_audio and the page plays them. No
// voice of a real person is copied: only voices ElevenLabs offers for use.
//
//	POST /v1/text-to-speech/{voice_id}?output_format=mp3_44100_128   speech (xi-api-key)
//	GET  /v1/voices                                                 the account's voices
//	GET  /v1/shared-voices?gender=male&language=ru&…                 the voice library
//	POST /v1/voices/add/{public_owner_id}/{voice_id} {new_name}      a library voice into the account
//	GET  /v1/user/subscription                                       the key check, characters left

// DefaultElevenModel reads Russian (ELEVENLABS_MODEL changes it).
const DefaultElevenModel = "eleven_multilingual_v2"

// ElevenFormat: the files the tour plays.
const ElevenFormat = "mp3_44100_128"

// ElevenBase: ELEVENLABS_API_BASE (tests) or the real API.
func ElevenBase() string {
	if v := strings.TrimSpace(os.Getenv("ELEVENLABS_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.elevenlabs.io"
}

// ElevenModel: ELEVENLABS_MODEL or eleven_multilingual_v2.
func ElevenModel() string {
	if v := strings.TrimSpace(os.Getenv("ELEVENLABS_MODEL")); v != "" {
		return v
	}
	return DefaultElevenModel
}

// ElevenSettings: how the voice reads. The defaults: a calm, even
// narrator (stability a bit above the middle, little extra style).
type ElevenSettings struct {
	Stability    float64 `json:"stability"`
	Similarity   float64 `json:"similarity_boost"`
	Style        float64 `json:"style"`
	SpeakerBoost bool    `json:"use_speaker_boost"`
}

var DefaultElevenSettings = ElevenSettings{Stability: 0.55, Similarity: 0.8, Style: 0.1, SpeakerBoost: true}

// Sig: the settings in the file key (other settings, other recordings).
func (s ElevenSettings) Sig() string {
	return fmt.Sprintf("%.2f/%.2f/%.2f/%t", s.Stability, s.Similarity, s.Style, s.SpeakerBoost)
}

// ElevenVoice: a voice offered in the settings.
type ElevenVoice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OwnerID     string `json:"ownerId,omitempty"` // a library voice: its public owner
	PreviewURL  string `json:"previewUrl,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Age         string `json:"age,omitempty"`
	Accent      string `json:"accent,omitempty"`
	Language    string `json:"language,omitempty"`
	UseCase     string `json:"useCase,omitempty"`
	Descriptive string `json:"descriptive,omitempty"`
	Category    string `json:"category,omitempty"`
	Mine        bool   `json:"mine,omitempty"` // in the account already
	RuPreview   bool   `json:"ruPreview,omitempty"`
	score       int
}

// ElevenError: an answer of the API that is not a success.
//
// The API answers {"detail": {"type", "code", "status", "message", ...}}:
// "code" is the current field, "status" the legacy one (older answers carry
// only it, e.g. {"status": "missing_permissions"}). A key without a needed
// permission comes as HTTP 401 too (missing_permissions), so the kind is
// told by the code first and by the HTTP status only when there is none (R38c).
type ElevenError struct {
	Status     int
	Code       string // detail.code, else detail.status: invalid_api_key, missing_permissions, voice_not_found…
	Legacy     string // detail.status when detail.code is there as well
	Type       string // detail.type: authentication_error, authorization_error…
	Message    string
	RetryAfter time.Duration
}

func (e *ElevenError) Error() string {
	s := fmt.Sprintf("elevenlabs %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// The kinds of a failed answer (ElevenKind).
const (
	ElevenKindKey   = "key"   // the key is wrong, not ElevenLabs' or revoked
	ElevenKindPerm  = "perm"  // the key is fine but lacks a permission
	ElevenKindVoice = "voice" // no such voice for this account
	ElevenKindQuota = "quota" // the plan's characters are used up
	ElevenKindPlan  = "plan"  // the plan does not allow it (a paid plan is needed)
	ElevenKindAbuse = "abuse" // the free plan is blocked for server use
	ElevenKindRate  = "rate"  // too many requests at once
	ElevenKindOther = "other"
)

func (e *ElevenError) codes() string { return strings.ToLower(e.Code + " " + e.Legacy) }

// Kind: what went wrong, by the provider's code first, the HTTP status last.
func (e *ElevenError) Kind() string {
	c, msg := e.codes(), strings.ToLower(e.Message)
	has := func(list ...string) bool {
		for _, x := range list {
			if strings.Contains(c, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("missing_permissions", "insufficient_permissions", "missing_permission"):
		return ElevenKindPerm
	case has("detected_unusual_activity", "unusual_activity"):
		return ElevenKindAbuse
	case has("quota_exceeded", "insufficient_credits", "credits_exhausted"):
		return ElevenKindQuota
	case has("voice_not_found", "invalid_voice_id"):
		return ElevenKindVoice
	case has("payment_required", "subscription_required", "feature_not_available", "voice_access_denied", "model_access_denied", "paid_plan_required", "can_not_use_instant_voice_cloning", "can_not_use_professional_voice_cloning"):
		return ElevenKindPlan
	case has("invalid_api_key", "missing_api_key", "invalid_authorization_header", "unauthorized", "api_key_revoked", "sign_in_required"):
		return ElevenKindKey
	case has("rate_limit_exceeded", "too_many_concurrent_requests", "concurrent_limit_exceeded", "system_busy"):
		return ElevenKindRate
	}
	// No code: the words of the message, then the HTTP status.
	switch {
	case strings.Contains(msg, "missing the permission") || strings.Contains(msg, "missing permission"):
		return ElevenKindPerm
	case strings.Contains(msg, "unusual activity"):
		return ElevenKindAbuse
	case strings.Contains(msg, "quota"):
		return ElevenKindQuota
	}
	switch e.Status {
	case 401:
		return ElevenKindKey
	case 402:
		return ElevenKindPlan
	case 403:
		return ElevenKindPerm
	case 404:
		return ElevenKindVoice
	case 429:
		return ElevenKindRate
	}
	return ElevenKindOther
}

var elevenPermRe = regexp.MustCompile(`(?i)permissions?\s+["'«]?([a-z][a-z0-9_]{2,40})`)

// Permission: the permission the key lacks, as the API names it
// (text_to_speech, voices_read, voices_write, user_read…), "" when unknown.
func (e *ElevenError) Permission() string {
	if m := elevenPermRe.FindStringSubmatch(e.Message); m != nil {
		p := strings.ToLower(m[1])
		if p != "to" && p != "for" && p != "the" {
			return p
		}
	}
	return ""
}

// ElevenPermLabel: the permission as the key's settings page names it.
func ElevenPermLabel(p string) string {
	switch p {
	case "text_to_speech":
		return "Text to Speech: Access"
	case "voices_read":
		return "Voices: Read"
	case "voices_write":
		return "Voices: Write"
	case "user_read":
		return "User: Read"
	case "models_read":
		return "Models: Read"
	case "":
		return ""
	}
	return p
}

var elevenSecretRe = regexp.MustCompile(`sk_[A-Za-z0-9_-]+|[A-Fa-f0-9]{32,}`)

// ProviderText: the provider's own words, safe to show: no key, one line, short.
func (e *ElevenError) ProviderText() string {
	t := elevenSecretRe.ReplaceAllString(e.Message, "…")
	t = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 32 || r == '«' || r == '»' || r == '"' || r == '`' || r == '<' || r == '>' {
			return ' '
		}
		return r
	}, t)), " ")
	if r := []rune(t); len(r) > 200 {
		t = string(r[:200]) + "…"
	}
	return t
}

// ErrNoElevenKey: no key in Railway and none saved in the settings.
var ErrNoElevenKey = errors.New("Нет ключа ElevenLabs: вставьте его в Настройках платформы («Голос ElevenLabs») или в переменную ELEVENLABS_API_KEY в Railway")

// ElevenMessage: the reason in words for the settings and /status, with the
// provider's own text at the end (never the key).
func ElevenMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrNoElevenKey) {
		return err.Error()
	}
	var e *ElevenError
	if !errors.As(err, &e) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "ElevenLabs не ответил вовремя, попробуйте ещё раз"
		}
		return "Нет связи с ElevenLabs, попробуйте позже"
	}
	var m string
	switch e.Kind() {
	case ElevenKindPerm:
		if l := ElevenPermLabel(e.Permission()); l != "" {
			m = "Ключ ElevenLabs принят, но у него нет права «" + l + "»: elevenlabs.io → Developers → API Keys → у ключа ⋯ → Edit → включите «" + l + "» и сохраните (или создайте ключ с этим правом)"
		} else {
			m = "Ключ ElevenLabs принят, но у него не хватает прав: elevenlabs.io → Developers → API Keys → у ключа ⋯ → Edit → включите «Text to Speech: Access» и «Voices: Read»"
		}
	case ElevenKindAbuse:
		m = "ElevenLabs заблокировал бесплатный тариф для запросов с сервера (detected_unusual_activity): нужен платный тариф (от Starter), ключ при этом менять не нужно"
	case ElevenKindQuota:
		m = "На счёте ElevenLabs закончились символы: пополните тариф, озвучка продолжится сама"
	case ElevenKindVoice:
		m = "Голос не найден в аккаунте ElevenLabs: проверьте id или выберите другой"
	case ElevenKindPlan:
		m = "Тариф ElevenLabs не даёт это через API: нужен платный тариф (от Starter)"
	case ElevenKindKey:
		m = "Ключ ElevenLabs не принят: скопируйте его заново из elevenlabs.io → Developers → API Keys"
	case ElevenKindRate:
		m = "ElevenLabs просит подождать: слишком много запросов"
	default:
		m = "ElevenLabs ответил ошибкой " + strconv.Itoa(e.Status)
	}
	if t := e.ProviderText(); t != "" {
		m += " (ответ ElevenLabs " + strconv.Itoa(e.Status)
		if e.Code != "" {
			m += " " + e.Code
		}
		m += ": «" + t + "»)"
	} else if e.Code != "" {
		m += " (ответ ElevenLabs " + strconv.Itoa(e.Status) + " " + e.Code + ")"
	}
	return m
}

// ElevenKind: the kind of err ("" when it is not an answer of the API).
func ElevenKind(err error) string {
	var e *ElevenError
	if errors.As(err, &e) {
		return e.Kind()
	}
	return ""
}

// IsElevenQuota: the characters of the plan are used up (no retry helps).
func IsElevenQuota(err error) bool { return ElevenKind(err) == ElevenKindQuota }

// IsElevenKey: the key is wrong or lacks a permission.
func IsElevenKey(err error) bool {
	k := ElevenKind(err)
	return k == ElevenKindKey || k == ElevenKindPerm
}

// IsElevenVoiceMissing: the voice is not there for this account.
func IsElevenVoiceMissing(err error) bool { return ElevenKind(err) == ElevenKindVoice }

// Eleven talks to the ElevenLabs API.
type Eleven struct {
	Base string
	HTTP *http.Client
	// Key: the key in use (ELEVENLABS_API_KEY, else the saved one).
	Key func() string
	// Wait: the pause before try n (from 1) after a 429 or a 5xx; the
	// server's Retry-After when it names one. Tests shorten it.
	Wait func(try int, retryAfter time.Duration) time.Duration
	// Tries: attempts of one speech call (429 / 5xx / network).
	Tries int
}

// NewEleven: the client with the real API (or ELEVENLABS_API_BASE).
func NewEleven(key func() string) *Eleven {
	return &Eleven{Base: ElevenBase(), HTTP: &http.Client{Timeout: 90 * time.Second}, Key: key}
}

func elevenWait(try int, ra time.Duration) time.Duration {
	if ra > 0 {
		if ra > 2*time.Minute {
			ra = 2 * time.Minute
		}
		return ra
	}
	d := time.Duration(1<<uint(try)) * time.Second // 2s, 4s, 8s, 16s…
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

func (e *Eleven) key(override string) string {
	if k := strings.TrimSpace(override); k != "" {
		return k
	}
	if e.Key != nil {
		return strings.TrimSpace(e.Key())
	}
	return ""
}

func (e *Eleven) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

// call: one request. A failure is an *ElevenError (never the key).
func (e *Eleven) call(ctx context.Context, key, method, path string, body any) ([]byte, error) {
	if key == "" {
		return nil, ErrNoElevenKey
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	base := e.Base
	if base == "" {
		base = ElevenBase()
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("elevenlabs: request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, errors.New("elevenlabs: answer cut off")
	}
	if resp.StatusCode >= 300 {
		ee := &ElevenError{Status: resp.StatusCode}
		var d struct {
			Detail json.RawMessage `json:"detail"`
		}
		if json.Unmarshal(b, &d) == nil && len(d.Detail) > 0 {
			var obj struct {
				Code    string `json:"code"`
				Status  string `json:"status"`
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			var s string
			if json.Unmarshal(d.Detail, &obj) == nil {
				ee.Code, ee.Type, ee.Message = obj.Code, obj.Type, obj.Message
				if ee.Code == "" {
					ee.Code = obj.Status
				} else if obj.Status != obj.Code {
					ee.Legacy = obj.Status
				}
			} else if json.Unmarshal(d.Detail, &s) == nil {
				ee.Message = s
			}
		}
		if len([]rune(ee.Message)) > 300 {
			ee.Message = string([]rune(ee.Message)[:300])
		}
		if v := resp.Header.Get("Retry-After"); v != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				ee.RetryAfter = time.Duration(n) * time.Second
			}
		}
		return nil, ee
	}
	return b, nil
}

// Subscription: the key check: the plan and the characters used / allowed.
type ElevenPlan struct {
	Tier  string `json:"tier"`
	Used  int    `json:"used"`
	Limit int    `json:"limit"`
}

func (e *Eleven) Subscription(ctx context.Context, key string) (ElevenPlan, error) {
	b, err := e.call(ctx, e.key(key), "GET", "/v1/user/subscription", nil)
	if err != nil {
		return ElevenPlan{}, err
	}
	var s struct {
		Tier  string `json:"tier"`
		Used  int    `json:"character_count"`
		Limit int    `json:"character_limit"`
	}
	_ = json.Unmarshal(b, &s)
	return ElevenPlan{Tier: s.Tier, Used: s.Used, Limit: s.Limit}, nil
}

type elevenVerified struct {
	Language   string `json:"language"`
	PreviewURL string `json:"preview_url"`
}

func ruPreview(def string, vl []elevenVerified) (string, bool) {
	for _, v := range vl {
		if strings.EqualFold(v.Language, "ru") && v.PreviewURL != "" {
			return v.PreviewURL, true
		}
	}
	return def, false
}

// MyVoices: the voices in the account (designed, cloned by the owner, added
// from the library).
func (e *Eleven) MyVoices(ctx context.Context) ([]ElevenVoice, error) {
	b, err := e.call(ctx, e.key(""), "GET", "/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Voices []struct {
			ID         string            `json:"voice_id"`
			Name       string            `json:"name"`
			Category   string            `json:"category"`
			PreviewURL string            `json:"preview_url"`
			Labels     map[string]string `json:"labels"`
			Verified   []elevenVerified  `json:"verified_languages"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("elevenlabs: voices unreadable")
	}
	var out []ElevenVoice
	for _, v := range r.Voices {
		if v.ID == "" {
			continue
		}
		p, ru := ruPreview(v.PreviewURL, v.Verified)
		out = append(out, ElevenVoice{ID: v.ID, Name: v.Name, PreviewURL: p, RuPreview: ru, Category: v.Category, Mine: true,
			Gender: v.Labels["gender"], Age: v.Labels["age"], Accent: v.Labels["accent"],
			UseCase: v.Labels["use_case"], Descriptive: firstNonEmpty(v.Labels["descriptive"], v.Labels["description"])})
	}
	return out, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// SharedVoices: one page of the voice library.
func (e *Eleven) SharedVoices(ctx context.Context, q url.Values) ([]ElevenVoice, error) {
	b, err := e.call(ctx, e.key(""), "GET", "/v1/shared-voices?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Voices []struct {
			ID          string           `json:"voice_id"`
			OwnerID     string           `json:"public_owner_id"`
			Name        string           `json:"name"`
			PreviewURL  string           `json:"preview_url"`
			Gender      string           `json:"gender"`
			Age         string           `json:"age"`
			Accent      string           `json:"accent"`
			Language    string           `json:"language"`
			UseCase     string           `json:"use_case"`
			Descriptive string           `json:"descriptive"`
			Category    string           `json:"category"`
			Verified    []elevenVerified `json:"verified_languages"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("elevenlabs: library unreadable")
	}
	var out []ElevenVoice
	for _, v := range r.Voices {
		if v.ID == "" {
			continue
		}
		p, ru := ruPreview(v.PreviewURL, v.Verified)
		out = append(out, ElevenVoice{ID: v.ID, OwnerID: v.OwnerID, Name: v.Name, PreviewURL: p, RuPreview: ru,
			Gender: v.Gender, Age: v.Age, Accent: v.Accent, Language: v.Language, UseCase: v.UseCase,
			Descriptive: v.Descriptive, Category: v.Category})
	}
	return out, nil
}

// ButlerQueries: the library searches behind «Подобрать голос»: male voices
// that speak Russian, deep or calm, narrators and assistants.
func ButlerQueries(search string) []url.Values {
	base := func() url.Values {
		q := url.Values{}
		q.Set("gender", "male")
		q.Set("language", "ru")
		q.Set("page_size", "30")
		return q
	}
	if s := strings.TrimSpace(search); s != "" {
		q := base()
		q.Set("search", s)
		q.Set("page_size", "40")
		return []url.Values{q}
	}
	var out []url.Values
	for _, kv := range [][2]string{{"descriptives", "deep"}, {"descriptives", "calm"}, {"use_cases", "narrative_story"}, {"use_cases", "informative_educational"}} {
		q := base()
		q.Set(kv[0], kv[1])
		out = append(out, q)
	}
	return out
}

func butlerScore(v ElevenVoice) int {
	d := strings.ToLower(v.Descriptive + " " + v.Name)
	s := 0
	for w, p := range map[string]int{"deep": 4, "calm": 3, "confident": 2, "formal": 2, "professional": 2, "mature": 1, "smooth": 1, "warm": 1, "british": 1, "butler": 3, "assistant": 2, "низк": 3, "спокойн": 3, "глубок": 3} {
		if strings.Contains(d, w) {
			s += p
		}
	}
	if strings.Contains(v.UseCase, "narrative") || strings.Contains(v.UseCase, "informative") {
		s += 2
	}
	if v.Age == "middle_aged" || v.Age == "old" {
		s++
	}
	if v.RuPreview || strings.EqualFold(v.Language, "ru") {
		s += 2
	}
	if v.Gender != "" && v.Gender != "male" {
		s -= 10
	}
	return s
}

// FindButlerVoices: «Подобрать голос»: the account's voices first, then the
// library's male Russian-speaking deep / calm narrators, best first.
func (e *Eleven) FindButlerVoices(ctx context.Context, search string, limit int) (mine, library []ElevenVoice, err error) {
	if limit <= 0 {
		limit = 24
	}
	mine, err = e.MyVoices(ctx)
	if err != nil {
		return nil, nil, err
	}
	have := map[string]bool{}
	for _, v := range mine {
		have[v.ID] = true
	}
	seen := map[string]bool{}
	var firstErr error
	for _, q := range ButlerQueries(search) {
		vs, err := e.SharedVoices(ctx, q)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, v := range vs {
			if seen[v.ID] || have[v.ID] {
				continue
			}
			seen[v.ID] = true
			v.score = butlerScore(v)
			library = append(library, v)
		}
	}
	if len(library) == 0 && firstErr != nil {
		return mine, nil, firstErr
	}
	sort.SliceStable(library, func(i, j int) bool { return library[i].score > library[j].score })
	if len(library) > limit {
		library = library[:limit]
	}
	return mine, library, nil
}

// Voice: one voice of the account by its id (ELEVENLABS_VOICE_ID). A 404
// means it is not in the account: FindShared looks for it in the library.
func (e *Eleven) Voice(ctx context.Context, id string) (ElevenVoice, error) {
	b, err := e.call(ctx, e.key(""), "GET", "/v1/voices/"+url.PathEscape(id), nil)
	if err != nil {
		return ElevenVoice{}, err
	}
	var v struct {
		ID         string `json:"voice_id"`
		Name       string `json:"name"`
		Category   string `json:"category"`
		PreviewURL string `json:"preview_url"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.ID == "" {
		return ElevenVoice{}, errors.New("elevenlabs: voice unreadable")
	}
	return ElevenVoice{ID: v.ID, Name: v.Name, Category: v.Category, PreviewURL: v.PreviewURL, Mine: true}, nil
}

// FindShared: a library voice by its id (to add it to the account).
func (e *Eleven) FindShared(ctx context.Context, id string) (ElevenVoice, bool, error) {
	for _, q := range []url.Values{{"voice_id": {id}, "page_size": {"10"}}, {"search": {id}, "page_size": {"30"}}} {
		list, err := e.SharedVoices(ctx, q)
		if err != nil {
			return ElevenVoice{}, false, err
		}
		for _, v := range list {
			if v.ID == id {
				return v, true, nil
			}
		}
	}
	return ElevenVoice{}, false, nil
}

// AddShared puts a library voice into the account (what the API needs to
// speak with it) and returns the id to speak with. A voice already there
// answers with its own id.
func (e *Eleven) AddShared(ctx context.Context, ownerID, voiceID, name string) (string, error) {
	if ownerID == "" {
		return voiceID, nil
	}
	b, err := e.call(ctx, e.key(""), "POST", "/v1/voices/add/"+url.PathEscape(ownerID)+"/"+url.PathEscape(voiceID), map[string]any{"new_name": name})
	if err != nil {
		var ee *ElevenError
		if errors.As(err, &ee) && ee.Status == 400 && (strings.Contains(strings.ToLower(ee.Code+ee.Message), "already")) {
			return voiceID, nil
		}
		return "", err
	}
	var r struct {
		ID string `json:"voice_id"`
	}
	if json.Unmarshal(b, &r) == nil && r.ID != "" {
		return r.ID, nil
	}
	return voiceID, nil
}

// ProbeText: what «Проверить» reads (4 characters of the plan).
const ProbeText = "Тест"

// Probe reads ProbeText with the voice once, with key (or the key in use):
// the one check that proves the key, its Text to Speech permission and the
// voice together. It needs no other permission. Returns the audio size.
func (e *Eleven) Probe(ctx context.Context, key, voiceID, model string) (int, error) {
	if model == "" {
		model = ElevenModel()
	}
	body := map[string]any{"text": ProbeText, "model_id": model}
	wait := e.Wait
	if wait == nil {
		wait = elevenWait
	}
	var b []byte
	var err error
	for try := 1; try <= 3; try++ {
		b, err = e.call(ctx, e.key(key), "POST", "/v1/text-to-speech/"+url.PathEscape(voiceID)+"?output_format="+ElevenFormat, body)
		var ee *ElevenError
		if err == nil || try == 3 || !errors.As(err, &ee) || (ee.Status != 429 && ee.Status < 500) || IsElevenQuota(err) {
			break
		}
		d := wait(try, ee.RetryAfter)
		if d > 5*time.Second {
			d = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(d):
		}
	}
	if err != nil {
		return 0, err
	}
	if len(b) < 64 {
		return 0, errors.New("elevenlabs: empty audio")
	}
	return len(b), nil
}

// Speak reads text with the voice: MP3 bytes. A 429, a 5xx or a broken
// connection is retried after a pause (the server's Retry-After first);
// a wrong key, used-up characters or a missing voice stop at once.
func (e *Eleven) Speak(ctx context.Context, voiceID, model, text string, s ElevenSettings) ([]byte, error) {
	if model == "" {
		model = ElevenModel()
	}
	tries := e.Tries
	if tries <= 0 {
		tries = 6
	}
	wait := e.Wait
	if wait == nil {
		wait = elevenWait
	}
	body := map[string]any{"text": text, "model_id": model, "voice_settings": s}
	path := "/v1/text-to-speech/" + url.PathEscape(voiceID) + "?output_format=" + ElevenFormat
	var last error
	for try := 1; try <= tries; try++ {
		b, err := e.call(ctx, e.key(""), "POST", path, body)
		if err == nil {
			if len(b) < 64 {
				return nil, errors.New("elevenlabs: empty audio")
			}
			return b, nil
		}
		last = err
		var ee *ElevenError
		retry := ctx.Err() == nil && !errors.Is(err, ErrNoElevenKey)
		if errors.As(err, &ee) {
			retry = retry && (ee.Status == 429 || ee.Status >= 500) && !IsElevenQuota(err)
		}
		if !retry || try == tries {
			break
		}
		ra := time.Duration(0)
		if ee != nil {
			ra = ee.RetryAfter
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait(try, ra)):
		}
	}
	return nil, last
}
