package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456:TEST-token-for-unit-tests"

func signWidget(fields map[string]string, token string) string {
	secret := sha256.Sum256([]byte(token))
	return hexHMAC(secret[:], dataCheck(fields))
}

func signInitData(fields map[string]string, token string) string {
	m := hmac.New(sha256.New, []byte("WebAppData"))
	m.Write([]byte(token))
	return hexHMAC(m.Sum(nil), dataCheck(fields))
}

func dataCheck(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + fields[k]
	}
	return strings.Join(lines, "\n")
}

func hexHMAC(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func TestVerifyTelegramWidget(t *testing.T) {
	now := time.Unix(1790000000, 0)
	base := map[string]string{
		"id": "453800951", "first_name": "Рустам", "username": "rustam",
		"auth_date": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
	}
	good := func() map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		m["hash"] = signWidget(base, testBotToken)
		return m
	}

	u, err := verifyTelegramWidget(good(), testBotToken, now)
	if err != nil || u.ID != 453800951 || u.FirstName != "Рустам" {
		t.Fatalf("valid widget refused: %v %+v", err, u)
	}

	// The widget passes id and auth_date as JSON numbers.
	num := good()
	num["id"] = float64(453800951)
	authDate, _ := strconv.ParseFloat(base["auth_date"], 64)
	num["auth_date"] = authDate
	if _, err := verifyTelegramWidget(num, testBotToken, now); err != nil {
		t.Fatalf("numeric fields refused: %v", err)
	}

	tampered := good()
	tampered["id"] = "1285596249"
	if _, err := verifyTelegramWidget(tampered, testBotToken, now); err == nil {
		t.Fatal("changed id accepted")
	}

	if _, err := verifyTelegramWidget(good(), "999:other-bot", now); err == nil {
		t.Fatal("signature from another bot accepted")
	}

	if _, err := verifyTelegramWidget(good(), testBotToken, now.Add(25*time.Hour)); err == nil {
		t.Fatal("expired login accepted")
	}

	noHash := good()
	delete(noHash, "hash")
	if _, err := verifyTelegramWidget(noHash, testBotToken, now); err == nil {
		t.Fatal("missing hash accepted")
	}
}

func TestVerifyTelegramInitData(t *testing.T) {
	now := time.Unix(1790000000, 0)
	user, _ := json.Marshal(map[string]any{"id": 1285596249, "first_name": "Береке"})
	fields := map[string]string{
		"user":      string(user),
		"auth_date": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
		"query_id":  "AAH-test",
	}
	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", signInitData(fields, testBotToken))

	u, err := verifyTelegramInitData(q.Encode(), testBotToken, now)
	if err != nil || u.ID != 1285596249 {
		t.Fatalf("valid initData refused: %v %+v", err, u)
	}

	// A Login Widget signature is not valid as initData (different key).
	q2 := url.Values{}
	for k, v := range fields {
		q2.Set(k, v)
	}
	q2.Set("hash", signWidget(fields, testBotToken))
	if _, err := verifyTelegramInitData(q2.Encode(), testBotToken, now); err == nil {
		t.Fatal("widget-signed initData accepted")
	}

	q.Set("auth_date", strconv.FormatInt(now.Unix(), 10))
	if _, err := verifyTelegramInitData(q.Encode(), testBotToken, now); err == nil {
		t.Fatal("changed auth_date accepted")
	}
}

func TestParsePlatformTeam(t *testing.T) {
	team := ParsePlatformTeam("")
	if team[453800951] != "Рустам" || team[1285596249] != "Береке" || len(team) != 2 {
		t.Fatalf("default team wrong: %v", team)
	}
	team = ParsePlatformTeam(" 1:А , bad, 2 ,3:В")
	if len(team) != 3 || team[1] != "А" || team[2] != "" || team[3] != "В" {
		t.Fatalf("custom team wrong: %v", team)
	}
}

func TestUnionJSON(t *testing.T) {
	cases := []struct {
		name, server, incoming, want string
		changed                      bool
	}{
		{"records by id: add missing, keep server copy of shared",
			`[{"id":1,"n":"server"},{"id":2,"n":"b"}]`,
			`[{"id":1,"n":"file"},{"id":3,"n":"c"}]`,
			`[{"id":1,"n":"server"},{"id":2,"n":"b"},{"id":3,"n":"c"}]`, true},
		{"nothing new",
			`[{"id":1,"n":"server"}]`, `[{"id":1,"n":"file"}]`, ``, false},
		{"empty server list takes the file",
			`[]`, `[{"id":1}]`, `[{"id":1}]`, true},
		{"objects: add keys, merge nested",
			`{"a":{"x":1},"b":2}`, `{"a":{"y":2},"c":3,"b":9}`, `{"a":{"x":1,"y":2},"b":2,"c":3}`, true},
		{"plain lists: union",
			`["a","b"]`, `["b","c"]`, `["a","b","c"]`, true},
		{"empty scalar takes the file",
			`""`, `"dark"`, `"dark"`, true},
		{"scalar kept",
			`"light"`, `"dark"`, ``, false},
		{"not JSON on server, empty",
			``, `plain text`, `plain text`, true},
	}
	for _, tc := range cases {
		got, ch := unionJSON(tc.server, tc.incoming)
		if ch != tc.changed {
			t.Errorf("%s: changed=%v want %v", tc.name, ch, tc.changed)
			continue
		}
		if !tc.changed {
			if got != tc.server {
				t.Errorf("%s: server value altered: %s", tc.name, got)
			}
			continue
		}
		if !jsonSame(got, tc.want) {
			t.Errorf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func jsonSame(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}
