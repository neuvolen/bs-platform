package ai

import (
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R51: a provider that refuses the key itself («API key not valid»,
// API_KEY_INVALID, an AI Studio auth key the endpoint does not take). Before,
// such a refusal was not remembered: /status still said «Gemini: работает»
// (the live ping's error was dropped), the chain kept calling it and the
// owner saw Google's raw text in three lines. Now the key rests
// BadKeyHold (a freshly made key may need a few minutes at Google), the
// chain goes on without it and the check says «ключ не принят» with one
// plain action. Only the key's shape is ever logged (start, length), never
// the key.

// BadKeyHold: how long a refused key is not called again.
var BadKeyHold = 20 * time.Minute

// KeyError: the provider refused the key.
type KeyError struct {
	Service string
	Status  int
	Hint    string // what is wrong with the key's shape, "" when it looks fine
	Err     error
}

func (e *KeyError) Error() string {
	msg := "Ключ " + ProviderLabel(e.Service) + " не принят"
	if a := e.Action(); a != "" {
		msg += ": " + strings.ToLower(a[:2]) + a[2:]
	}
	if e.Hint != "" {
		msg += " (" + e.Hint + ")"
	}
	return msg
}

// Problem: «ключ Gemini не принят» with the shape hint, for one line.
func (e *KeyError) Problem() string {
	p := "ключ " + ProviderLabel(e.Service) + " не принят"
	if e.Hint != "" {
		p += " (" + e.Hint + ")"
	}
	return p
}

// Action: the one thing the owner does about it.
func (e *KeyError) Action() string {
	if e.Service == "gemini" {
		return "Создайте новый ключ на aistudio.google.com/api-keys и вставьте его в GEMINI_API_KEY в Railway"
	}
	if env := providerEnv[e.Service]; env != "" {
		return "Проверьте " + env + " в переменных Railway"
	}
	return ""
}

func (e *KeyError) Unwrap() error { return e.Err }

// IsKeyRejected: err is (or wraps) a refused key.
func IsKeyRejected(err error) bool {
	var ke *KeyError
	return errors.As(err, &ke)
}

type badKeyState struct {
	mu    sync.Mutex
	until map[string]time.Time
	errs  map[string]*KeyError
}

var badKeys sync.Map // *Client → *badKeyState

func (c *Client) bk() *badKeyState {
	v, _ := badKeys.LoadOrStore(c, &badKeyState{until: map[string]time.Time{}, errs: map[string]*KeyError{}})
	return v.(*badKeyState)
}

// keyRefusal: Google's (or an OpenAI-style) answer that the key itself is bad.
func keyRefusal(err error) (*HTTPError, bool) {
	var he *HTTPError
	if !errors.As(err, &he) || he.Status < 400 || he.Status >= 500 || he.Status == 429 {
		return nil, false
	}
	low := strings.ToLower(he.Body)
	for _, t := range []string{"api key not valid", "api_key_invalid", "invalid api key", "invalid_api_key",
		"access_token_type_unsupported", "api key expired", "api_key_expired"} {
		if strings.Contains(low, t) {
			return he, true
		}
	}
	return nil, false
}

// noteBadKey remembers a refused key; it returns the *KeyError to pass on
// (nil when err is something else).
func (c *Client) noteBadKey(service string, err error) *KeyError {
	he, ok := keyRefusal(err)
	if !ok {
		return nil
	}
	ke := &KeyError{Service: service, Status: he.Status, Err: err}
	if service == "gemini" {
		ke.Hint = geminiShapeHint(c.Gemini)
		if ke.Hint == "" && strings.HasPrefix(c.Gemini, "AIza") {
			// prod 06.10.2026: a whole AIza… key, refused: Google moves the
			// Gemini API to auth keys (AQ.…, made in AI Studio) and rejects
			// the old standard keys
			ke.Hint = "это ключ старого формата AIza…: Google их больше не принимает, нужен новый ключ AQ.… из AI Studio"
		}
	}
	b := c.bk()
	b.mu.Lock()
	wasOpen := !quotaNow().Before(b.until[service])
	b.until[service] = quotaNow().Add(BadKeyHold)
	b.errs[service] = ke
	b.mu.Unlock()
	if wasOpen {
		log.Printf("ai: %s key rejected (%d %s); key shape: %s; not called for %s",
			service, he.Status, apiMessage(he.Body), keyShape(c.providerKey(service)), BadKeyHold)
	}
	return ke
}

// keyClosed: the provider's key was refused a moment ago (nil: try it).
func (c *Client) keyClosed(service string) *KeyError {
	if service == "gemini-lite" {
		service = "gemini"
	}
	b := c.bk()
	b.mu.Lock()
	defer b.mu.Unlock()
	if u := b.until[service]; !u.IsZero() && quotaNow().Before(u) {
		return b.errs[service]
	}
	return nil
}

// ClearBadKey forgets a refusal (tests, a key saved again).
func (c *Client) ClearBadKey(service string) {
	b := c.bk()
	b.mu.Lock()
	delete(b.until, service)
	delete(b.errs, service)
	b.mu.Unlock()
}

// keyShape: «AQ.…, 53 символа» for the logs: the start and the length only.
func keyShape(k string) string {
	if k == "" {
		return "empty"
	}
	start := "other"
	for _, p := range []string{"AIza", "AQ.", "gsk_", "sk-or-", "sk-ant-", "sk-"} {
		if strings.HasPrefix(k, p) {
			start = p + "…"
			break
		}
	}
	s := start + ", " + strconv.Itoa(len(k)) + " chars"
	if strings.IndexFunc(k, func(r rune) bool { return r <= ' ' || r == '"' || r == '\'' || r == '=' }) >= 0 {
		s += ", has spaces, quotes or ="
	}
	return s
}

// geminiShapeHint: what is wrong with the shape of a Gemini key, in Russian,
// without revealing it. "" when it looks like a key.
func geminiShapeHint(k string) string {
	n := len(k)
	switch {
	case k == "":
		return ""
	case strings.IndexFunc(k, func(r rune) bool { return r <= ' ' || r == '"' || r == '\'' || r == '=' }) >= 0:
		return "в значении есть пробел, кавычка или «=»: вставьте только сам ключ"
	case strings.HasPrefix(k, "AIza") && n != 39:
		return "ключ скопирован не полностью: в нём " + strconv.Itoa(n) + " символов, а ключ AIza… состоит из 39"
	case strings.HasPrefix(k, "AQ.") && n < 40:
		return "ключ скопирован не полностью: в нём " + strconv.Itoa(n) + " символов"
	case !strings.HasPrefix(k, "AIza") && !strings.HasPrefix(k, "AQ."):
		return "ключ Google начинается с AIza или AQ., а этот начинается иначе (" + strconv.Itoa(n) + " символов)"
	}
	return ""
}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*[=:]\s*`)

// cleanAPIKey: a provider key as pasted into Railway: CleanKey, then without
// a «GEMINI_API_KEY=» in front and up to the first space or line break (a
// key has none; a comment or a second line after it is dropped).
func cleanAPIKey(v string) string {
	k, _ := CleanKey(v)
	if m := envAssign.FindString(k); m != "" {
		k, _ = CleanKey(k[len(m):])
	}
	if i := strings.IndexFunc(k, func(r rune) bool { return r <= ' ' || r == '"' || r == '\'' || r == '`' }); i > 0 {
		k = k[:i]
	}
	return k
}

// ShortAPIMessage: the API's own error message (no keys inside), for the
// system check's details.
func ShortAPIMessage(body string) string {
	if m := apiMessage(body); m != "" {
		return m
	}
	return "без текста"
}
