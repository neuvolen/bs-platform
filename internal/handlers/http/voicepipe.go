package http

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// R57: voicepipe: voiceovers for the videos made by GitHub Actions with the
// platform's ElevenLabs key, without the key ever leaving Railway.
//
// The workflow .github/workflows/voice.yml (branch voice-jobs) asks GitHub
// for an OIDC token (audience bs-voicepipe) and sends allowlisted ElevenLabs
// calls here; the server checks the token against GitHub's keys (issuer,
// audience, repository, branch, event) and forwards the call with its key.
//
//	POST /api/v1/platform/voicepipe/eleven
//	Authorization: Bearer <GitHub Actions OIDC token>
//	{"method":"GET|POST","path":"/v1/…","query":{"k":"v"},"body":{…}}
//
// The answer is ElevenLabs' own status and JSON body (X-Voicepipe: eleven).
// VOICEPIPE=off turns it off; VOICEPIPE_REPO / VOICEPIPE_REF change the repo
// and the branch whose workflows may call it.

const (
	ghOIDCIssuer  = "https://token.actions.githubusercontent.com"
	voicepipeAud  = "bs-voicepipe"
	voicepipeRepo = "neuvolen/bs-platform"
	voicepipeRef  = "refs/heads/voice-jobs"
)

var voicepipeAllow = []struct {
	method string
	re     *regexp.Regexp
}{
	{"GET", regexp.MustCompile(`^/v1/voices$`)},
	{"GET", regexp.MustCompile(`^/v1/voices/[A-Za-z0-9]{10,40}$`)},
	{"GET", regexp.MustCompile(`^/v1/shared-voices$`)},
	{"GET", regexp.MustCompile(`^/v1/user/subscription$`)},
	{"GET", regexp.MustCompile(`^/v1/models$`)},
	{"POST", regexp.MustCompile(`^/v1/voices/add/[A-Za-z0-9]{6,80}/[A-Za-z0-9]{10,40}$`)},
	{"POST", regexp.MustCompile(`^/v1/text-to-speech/[A-Za-z0-9]{10,40}/with-timestamps$`)},
}

var voicepipeQuery = map[string]bool{
	"search": true, "page_size": true, "page": true, "output_format": true, "language": true,
	"gender": true, "age": true, "accent": true, "category": true, "sort": true, "use_cases": true,
	"featured": true, "show_legacy": true, "include_settings": true,
}

// VoicePipe forwards the allowlisted ElevenLabs calls of the voice workflow.
type VoicePipe struct {
	Key     func() string // the platform's ElevenLabs key
	Base    string        // ElevenLabs API base ("" → ai.ElevenBase())
	JWKSURL string        // GitHub's OIDC keys ("" → the real one)
	HTTP    *http.Client

	mu     sync.Mutex
	keys   map[string]*rsa.PublicKey
	keysAt time.Time
	sem    chan struct{}
}

func NewVoicePipe(key func() string) *VoicePipe {
	return &VoicePipe{Key: key, HTTP: &http.Client{Timeout: 3 * time.Minute}, sem: make(chan struct{}, 2)}
}

func voicepipeEnv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// jwks: GitHub's signing keys, fetched again at most every 5 minutes for an
// unknown kid (GitHub rotates them).
func (v *VoicePipe) pubKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k := v.keys[kid]; k != nil {
		return k, nil
	}
	if time.Since(v.keysAt) < 5*time.Minute && v.keys != nil {
		return nil, errors.New("unknown kid")
	}
	u := v.JWKSURL
	if u == "" {
		u = ghOIDCIssuer + "/.well-known/jwks"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("jwks unreachable")
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct {
			Kty, Kid, N, E string
		} `json:"keys"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set) != nil {
		return nil, errors.New("jwks unreadable")
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(eb) == 0 || len(eb) > 4 {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
	}
	v.keys, v.keysAt = keys, time.Now()
	if k := keys[kid]; k != nil {
		return k, nil
	}
	return nil, errors.New("unknown kid")
}

type voicepipeClaims struct {
	Repository      string `json:"repository"`
	RepositoryOwner string `json:"repository_owner"`
	Ref             string `json:"ref"`
	EventName       string `json:"event_name"`
	Actor           string `json:"actor"`
	RunID           string `json:"run_id"`
	jwt.RegisteredClaims
}

// verify: the token is GitHub's, for this audience, from a workflow of our
// repository run on the voice branch by a push or by hand.
func (v *VoicePipe) verify(ctx context.Context, raw string) (*voicepipeClaims, error) {
	cl := &voicepipeClaims{}
	_, err := jwt.ParseWithClaims(raw, cl, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.pubKey(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(ghOIDCIssuer), jwt.WithAudience(voicepipeAud),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		return nil, err
	}
	repo := voicepipeEnv("VOICEPIPE_REPO", voicepipeRepo)
	if !strings.EqualFold(cl.Repository, repo) || !strings.EqualFold(cl.RepositoryOwner, strings.SplitN(repo, "/", 2)[0]) {
		return nil, fmt.Errorf("repository %q not allowed", cl.Repository)
	}
	if cl.Ref != voicepipeEnv("VOICEPIPE_REF", voicepipeRef) {
		return nil, fmt.Errorf("ref %q not allowed", cl.Ref)
	}
	if cl.EventName != "push" && cl.EventName != "workflow_dispatch" {
		return nil, fmt.Errorf("event %q not allowed", cl.EventName)
	}
	return cl, nil
}

type voicepipeReq struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
}

func voicepipeAllowed(method, path string) bool {
	for _, a := range voicepipeAllow {
		if a.method == method && a.re.MatchString(path) {
			return true
		}
	}
	return false
}

// Eleven: POST /voicepipe/eleven.
func (v *VoicePipe) Eleven(c *gin.Context) {
	if strings.EqualFold(os.Getenv("VOICEPIPE"), "off") {
		c.JSON(http.StatusNotFound, gin.H{"error": "voicepipe off"})
		return
	}
	raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok || raw == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "GitHub Actions OIDC token required"})
		return
	}
	cl, err := v.verify(c.Request.Context(), strings.TrimSpace(raw))
	if err != nil {
		log.Printf("voicepipe: token refused: %v", err)
		c.JSON(http.StatusForbidden, gin.H{"error": "token refused"})
		return
	}
	var r voicepipeReq
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 64<<10)).Decode(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	if !voicepipeAllowed(r.Method, r.Path) {
		c.JSON(http.StatusForbidden, gin.H{"error": "call not allowed: " + r.Method + " " + r.Path})
		return
	}
	key := ""
	if v.Key != nil {
		key = strings.TrimSpace(v.Key())
	}
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no ElevenLabs key on the server"})
		return
	}
	q := url.Values{}
	for k, val := range r.Query {
		if voicepipeQuery[k] {
			q.Set(k, val)
		}
	}
	base := v.Base
	if base == "" {
		base = ai.ElevenBase()
	}
	u := strings.TrimRight(base, "/") + r.Path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if r.Method == "POST" {
		b := []byte(r.Body)
		if len(b) == 0 {
			b = []byte("{}")
		}
		body = bytes.NewReader(b)
	}
	select {
	case v.sem <- struct{}{}:
		defer func() { <-v.sem }()
	case <-c.Request.Context().Done():
		return
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), r.Method, u, body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	req.Header.Set("xi-api-key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		log.Printf("voicepipe: run %s %s %s: request failed", cl.RunID, r.Method, r.Path)
		c.JSON(http.StatusBadGateway, gin.H{"error": "elevenlabs unreachable"})
		return
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 60<<20))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "elevenlabs answer cut off"})
		return
	}
	chars := ""
	if strings.HasPrefix(r.Path, "/v1/text-to-speech/") {
		var t struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(r.Body, &t)
		chars = fmt.Sprintf(", %d chars", len([]rune(t.Text)))
	}
	log.Printf("voicepipe: run %s (%s) %s %s → %d%s", cl.RunID, cl.Actor, r.Method, r.Path, resp.StatusCode, chars)
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Header("X-Voicepipe", "eleven")
	c.Data(resp.StatusCode, ct, out)
}
