package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R38c: a restricted ElevenLabs key comes back as HTTP 401 with
// detail.status "missing_permissions": that is not a wrong key. The kind is
// told by the provider's code first; the message names the permission and
// quotes ElevenLabs (never the key).
func TestR38cElevenKinds(t *testing.T) {
	const key = "sk_r38c_0123456789abcdef0123456789abcdef0123456789ab"
	answers := map[string]struct {
		status int
		body   string
	}{
		"perm_tts":  {401, `{"detail":{"status":"missing_permissions","message":"The API key you used is missing the permission text_to_speech to execute this operation."}}`},
		"perm_read": {401, `{"detail":{"status":"missing_permissions","message":"The API key you used is missing the permission voices_read to execute this operation."}}`},
		"perm_new":  {403, `{"detail":{"type":"authorization_error","code":"insufficient_permissions","status":"missing_permissions","message":"The API key you used is missing the permission voices_write to execute this operation."}}`},
		"badkey":    {401, `{"detail":{"status":"invalid_api_key","message":"Invalid API key: ` + key + `"}}`},
		"quota":     {401, `{"detail":{"status":"quota_exceeded","message":"This request exceeds your quota of 10000."}}`},
		"credits":   {402, `{"detail":{"type":"payment_required","code":"insufficient_credits","message":"Not enough credits"}}`},
		"payment":   {402, `{"detail":{"status":"payment_required","message":"Upgrade your subscription"}}`},
		"abuse":     {401, `{"detail":{"status":"detected_unusual_activity","message":"Unusual activity detected. Free Tier usage disabled."}}`},
		"novoice":   {404, `{"detail":{"type":"not_found","code":"voice_not_found","message":"A voice with voice_id x was not found."}}`},
		"bare401":   {401, `{"detail":"Unauthorized"}`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
		a, ok := answers[id]
		if !ok {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(make([]byte, 400))
			return
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	defer srv.Close()
	e := &Eleven{Base: srv.URL, HTTP: srv.Client(), Key: func() string { return key }, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
	ctx := context.Background()
	for id, want := range map[string]struct {
		kind  string
		words []string
	}{
		"perm_tts":  {ElevenKindPerm, []string{"Ключ ElevenLabs принят", "«Text to Speech: Access»", "missing the permission text_to_speech", "401 missing_permissions"}},
		"perm_read": {ElevenKindPerm, []string{"«Voices: Read»"}},
		"perm_new":  {ElevenKindPerm, []string{"«Voices: Write»", "insufficient_permissions"}},
		"badkey":    {ElevenKindKey, []string{"не принят", "Invalid API key"}},
		"quota":     {ElevenKindQuota, []string{"закончились символы"}},
		"credits":   {ElevenKindQuota, []string{"закончились символы"}},
		"payment":   {ElevenKindPlan, []string{"платный тариф"}},
		"abuse":     {ElevenKindAbuse, []string{"бесплатный тариф", "detected_unusual_activity", "Free Tier usage disabled"}},
		"novoice":   {ElevenKindVoice, []string{"Голос не найден"}},
		"bare401":   {ElevenKindKey, []string{"не принят", "Unauthorized"}},
	} {
		_, err := e.Probe(ctx, "", id, "")
		if err == nil || ElevenKind(err) != want.kind {
			t.Fatalf("%s: kind %q err %v", id, ElevenKind(err), err)
		}
		m := ElevenMessage(err)
		for _, w := range want.words {
			if !strings.Contains(m, w) {
				t.Fatalf("%s: %q lacks %q", id, m, w)
			}
		}
		if strings.Contains(m, key) || strings.Contains(m, "sk_r38c") || strings.Contains(m, "—") {
			t.Fatalf("%s: key or em dash in %q", id, m)
		}
	}
	if !IsElevenKey(mustErr(e.Probe(ctx, "", "perm_tts", ""))) || IsElevenKey(mustErr(e.Probe(ctx, "", "quota", ""))) || !IsElevenQuota(mustErr(e.Probe(ctx, "", "quota", ""))) {
		t.Fatal("helpers")
	}
	if n, err := e.Probe(ctx, "", "goodVoice", ""); err != nil || n != 400 {
		t.Fatalf("probe ok: %d %v", n, err)
	}
}

func mustErr(_ int, err error) error { return err }
