package http

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// signedGet signs a GET the way the script's bsSignedGet does:
// HMAC("GET " + path + "?" + query, bot token).
func (e *botEnv) signedGet(path string, q url.Values, sig string) map[string]any {
	e.t.Helper()
	raw := q.Encode()
	if sig == "" {
		sig = sign([]byte("GET " + path + "?" + raw))
	}
	w := e.do("GET", path+"?"+raw, nil, map[string]string{"X-BS-Signature": sig})
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{}
	}
	out["_code"] = w.Code
	return out
}

func TestScriptLatestIsSignedAndShipsTheEmbeddedCode(t *testing.T) {
	e := newBotEnv(t)
	e.useTestRelay()
	ctx := context.Background()
	if content.ScriptVersion() != bot.LatestScript {
		t.Fatalf("embedded Code.js is %q, bot.LatestScript %q", content.ScriptVersion(), bot.LatestScript)
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	q := url.Values{"ts": {now}, "version": {"2026-10-01-1"}}

	// Unsigned, wrongly signed, stale: refused
	if w := e.do("GET", "/api/v1/script/latest?"+q.Encode(), nil, nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
	if r := e.signedGet("/api/v1/script/latest", q, sign([]byte("GET /api/v1/script/latest?ts=1"))); r["_code"] != 401 {
		t.Fatalf("bad signature: %v", r["_code"])
	}
	old := url.Values{"ts": {strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)}, "version": {"x"}}
	if r := e.signedGet("/api/v1/script/latest", old, ""); r["_code"] != 401 || r["error"] != "stale_request" {
		t.Fatalf("stale: %v", r)
	}

	// An older script gets the code and the manifest
	r := e.signedGet("/api/v1/script/latest", q, "")
	if r["_code"] != 200 || r["version"] != bot.LatestScript || r["relay"] != e.scrSrv.URL {
		t.Fatalf("latest: %v %v %v", r["_code"], r["version"], r["relay"])
	}
	files, _ := r["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("files: %d", len(files))
	}
	byName := map[string]map[string]any{}
	for _, f := range files {
		m := f.(map[string]any)
		byName[m["name"].(string)] = m
	}
	code, _ := byName["Code"]["source"].(string)
	if byName["Code"]["type"] != "SERVER_JS" || !strings.Contains(code, `var BS_VERSION = "`+bot.LatestScript+`"`) ||
		!strings.Contains(code, "function bsSelfUpdate(") || !strings.Contains(code, `"`+content.ScriptTokenMark+`"`) {
		t.Fatal("Code.js is not the v32 script with the token mark")
	}
	if n := strings.Count(code, content.ScriptTokenMark); n != 2 {
		t.Fatalf("the token mark stands %d times, want 2 (both BOT_TOKEN lines)", n)
	}
	if regexp.MustCompile(`\d{8,10}:AA[A-Za-z0-9_-]{30,}`).MatchString(code) {
		t.Fatal("a bot token is embedded in Code.js")
	}
	var man struct {
		OAuthScopes []string       `json:"oauthScopes"`
		Webapp      map[string]any `json:"webapp"`
	}
	if byName["appsscript"]["type"] != "JSON" || json.Unmarshal([]byte(byName["appsscript"]["source"].(string)), &man) != nil {
		t.Fatal("manifest")
	}
	need := map[string]bool{"https://www.googleapis.com/auth/script.projects": true, "https://www.googleapis.com/auth/script.deployments": true,
		"https://www.googleapis.com/auth/script.external_request": true, "https://www.googleapis.com/auth/spreadsheets": true}
	for _, s := range man.OAuthScopes {
		delete(need, s)
	}
	if len(need) != 0 || man.Webapp["access"] != "ANYONE_ANONYMOUS" {
		t.Fatalf("manifest misses %v / webapp %v", need, man.Webapp)
	}
	if v, _ := pg.NewBotRepo(e.db).GetMeta(ctx, bot.MetaScriptVersion); v != "2026-10-01-1" {
		t.Fatalf("asking version not noted: %q", v)
	}

	// The latest script: nothing to download
	q.Set("version", bot.LatestScript)
	r = e.signedGet("/api/v1/script/latest", q, "")
	if files, _ := r["files"].([]any); r["_code"] != 200 || r["upToDate"] != true || len(files) != 0 {
		t.Fatalf("up to date: %v", r)
	}
}

func TestScriptUpdatedIsKeptAndShownInStatus(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	repo := pg.NewBotRepo(e.db)

	if w := e.do("POST", "/api/v1/script/updated", []byte(`{"version":"x"}`), nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
	w := e.signedPost("/api/v1/script/updated", map[string]any{"version": bot.LatestScript, "from": "2026-10-01-1",
		"deploymentId": "AKfycbwADG", "versionNumber": 41})
	if w.Code != 200 {
		t.Fatalf("updated: %d %s", w.Code, w.Body)
	}
	st := bot.ReadScriptUpdate(ctx, repo)
	if st.UpdatedTo != bot.LatestScript || st.UpdatedFrom != "2026-10-01-1" || st.DeploymentID != "AKfycbwADG" ||
		st.VersionNumber != "41" || st.UpdatedAt == "" || !st.UpToDate || st.Running != bot.LatestScript || st.LastError != "" {
		t.Fatalf("after update %+v", st)
	}

	// A failure is kept beside the last good update
	w = e.signedPost("/api/v1/script/updated", map[string]any{"version": bot.LatestScript, "error": "не включён Google Apps Script API"})
	if w.Code != 200 {
		t.Fatalf("error report: %d", w.Code)
	}
	st = bot.ReadScriptUpdate(ctx, repo)
	if st.LastError == "" || st.LastErrorAt == "" || st.UpdatedTo != bot.LatestScript {
		t.Fatalf("after failure %+v", st)
	}
	// The next good update clears it
	e.signedPost("/api/v1/script/updated", map[string]any{"version": bot.LatestScript, "from": bot.LatestScript, "deploymentId": "AKfycbwADG", "versionNumber": 42})
	if st = bot.ReadScriptUpdate(ctx, repo); st.LastError != "" || st.VersionNumber != "42" {
		t.Fatalf("error not cleared %+v", st)
	}
	// Nothing to keep
	if w := e.signedPost("/api/v1/script/updated", map[string]any{}); w.Code != 400 {
		t.Fatalf("empty: %d", w.Code)
	}
}
