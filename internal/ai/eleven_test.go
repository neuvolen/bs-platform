package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// R36: the ElevenLabs client against a fake API: the key header, the
// library search, adding a library voice, speech with a 429 backoff, and
// the errors that must not be retried.
func TestR36ElevenClient(t *testing.T) {
	const key = "el-test-key-0123456789abcdef"
	var mu sync.Mutex
	hits := map[string]int{}
	var queries []string
	var speakBodies []map[string]any
	n429 := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		if r.Header.Get("xi-api-key") != key {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`))
			return
		}
		switch {
		case r.URL.Path == "/v1/user/subscription":
			_, _ = w.Write([]byte(`{"tier":"starter","character_count":1200,"character_limit":30000}`))
		case r.URL.Path == "/v1/voices":
			_, _ = w.Write([]byte(`{"voices":[{"voice_id":"mine1","name":"Мой дворецкий","category":"generated","preview_url":"https://x/m.mp3","labels":{"gender":"male","descriptive":"calm"}}]}`))
		case r.URL.Path == "/v1/shared-voices":
			queries = append(queries, r.URL.RawQuery)
			q := r.URL.Query()
			if q.Get("gender") != "male" || q.Get("language") != "ru" {
				w.WriteHeader(400)
				return
			}
			vs := []map[string]any{
				{"voice_id": "v_calm", "public_owner_id": "own1", "name": "Calm Narrator", "preview_url": "https://x/en.mp3", "gender": "male", "age": "middle_aged", "use_case": "narrative_story", "descriptive": "calm",
					"verified_languages": []any{map[string]any{"language": "ru", "preview_url": "https://x/ru.mp3"}}},
				{"voice_id": "v_deep", "public_owner_id": "own2", "name": "Deep Butler", "preview_url": "https://x/d.mp3", "gender": "male", "age": "middle_aged", "use_case": "narrative_story", "descriptive": "deep"},
				{"voice_id": "mine1", "public_owner_id": "me", "name": "dup of mine", "gender": "male"},
				{"voice_id": "v_young", "public_owner_id": "own3", "name": "Energetic", "gender": "male", "age": "young", "use_case": "social_media", "descriptive": "upbeat"},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"voices": vs, "has_more": false})
		case strings.HasPrefix(r.URL.Path, "/v1/voices/add/"):
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "new_name") {
				w.WriteHeader(422)
				return
			}
			_, _ = w.Write([]byte(`{"voice_id":"added_` + strings.TrimPrefix(r.URL.Path, "/v1/voices/add/own2/") + `"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			speakBodies = append(speakBodies, b)
			if r.URL.Query().Get("output_format") != ElevenFormat {
				w.WriteHeader(400)
				return
			}
			switch strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/") {
			case "busy":
				n429++
				if n429 <= 2 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(429)
					_, _ = w.Write([]byte(`{"detail":{"status":"too_many_concurrent_requests","message":"busy"}}`))
					return
				}
			case "broke":
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"detail":{"status":"quota_exceeded","message":"This request exceeds your quota"}}`))
				return
			case "gone":
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"detail":{"status":"voice_not_found","message":"no voice"}}`))
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3"), make([]byte, 400)...))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	var waits []time.Duration
	e := &Eleven{Base: srv.URL, HTTP: srv.Client(), Key: func() string { return key },
		Wait: func(try int, ra time.Duration) time.Duration { waits = append(waits, ra); return time.Millisecond }}

	// the key check
	p, err := e.Subscription(ctx, "")
	if err != nil || p.Tier != "starter" || p.Limit != 30000 {
		t.Fatalf("subscription: %+v %v", p, err)
	}
	if _, err := e.Subscription(ctx, "wrong-key-000000000000"); !IsElevenKey(err) || !strings.Contains(ElevenMessage(err), "не принят") {
		t.Fatalf("wrong key: %v / %s", err, ElevenMessage(err))
	}
	if strings.Contains(ElevenMessage(err), key) {
		t.Fatal("key in message")
	}
	none := &Eleven{Base: srv.URL, HTTP: srv.Client(), Key: func() string { return "" }}
	if _, err := none.Subscription(ctx, ""); err != ErrNoElevenKey {
		t.Fatalf("no key: %v", err)
	}

	// «Подобрать голос»: mine first, library male + ru, deep / calm on top, no duplicate of mine
	mine, lib, err := e.FindButlerVoices(ctx, "", 10)
	if err != nil || len(mine) != 1 || !mine[0].Mine {
		t.Fatalf("mine: %+v %v", mine, err)
	}
	if len(lib) != 3 || lib[0].ID != "v_deep" && lib[0].ID != "v_calm" || lib[len(lib)-1].ID != "v_young" {
		t.Fatalf("library order: %+v", lib)
	}
	for _, v := range lib {
		if v.ID == "mine1" {
			t.Fatal("account voice repeated in the library list")
		}
		if v.ID == "v_calm" && (!v.RuPreview || v.PreviewURL != "https://x/ru.mp3") {
			t.Fatalf("russian preview not preferred: %+v", v)
		}
	}
	if len(queries) != 4 || !strings.Contains(strings.Join(queries, "&"), "descriptives=deep") || !strings.Contains(strings.Join(queries, "&"), "use_cases=narrative_story") {
		t.Fatalf("queries: %v", queries)
	}
	queries = nil
	if _, _, err := e.FindButlerVoices(ctx, "jarvis butler", 10); err != nil || len(queries) != 1 || !strings.Contains(queries[0], "search=jarvis+butler") {
		t.Fatalf("search: %v %v", queries, err)
	}

	// a library voice is added to the account; an account voice is not
	if id, err := e.AddShared(ctx, "own2", "v_deep", "BS гид"); err != nil || id != "added_v_deep" {
		t.Fatalf("add: %s %v", id, err)
	}
	if id, err := e.AddShared(ctx, "", "mine1", "x"); err != nil || id != "mine1" || hits["/v1/voices/add//mine1"] != 0 {
		t.Fatalf("add own: %s %v", id, err)
	}

	// speech: model, settings, mp3
	b, err := e.Speak(ctx, "v_calm", "", "Добрый день.", DefaultElevenSettings)
	if err != nil || string(b[:3]) != "ID3" {
		t.Fatalf("speak: %v", err)
	}
	last := speakBodies[len(speakBodies)-1]
	vs, _ := last["voice_settings"].(map[string]any)
	if last["model_id"] != DefaultElevenModel || last["text"] != "Добрый день." || vs["stability"] != 0.55 || vs["use_speaker_boost"] != true {
		t.Fatalf("speak body: %+v", last)
	}
	// 429 twice: waited as Retry-After says, then the audio
	waits = nil
	if b, err := e.Speak(ctx, "busy", "", "Раз.", DefaultElevenSettings); err != nil || len(b) < 64 {
		t.Fatalf("429: %v", err)
	}
	if len(waits) != 2 || waits[0] != time.Second || hits["/v1/text-to-speech/busy"] != 3 {
		t.Fatalf("backoff: waits %v hits %d", waits, hits["/v1/text-to-speech/busy"])
	}
	// used-up characters and a missing voice: no retry
	if _, err := e.Speak(ctx, "broke", "", "Два.", DefaultElevenSettings); !IsElevenQuota(err) || hits["/v1/text-to-speech/broke"] != 1 || !strings.Contains(ElevenMessage(err), "символы") {
		t.Fatalf("quota: %v hits %d", err, hits["/v1/text-to-speech/broke"])
	}
	if _, err := e.Speak(ctx, "gone", "", "Три.", DefaultElevenSettings); err == nil || hits["/v1/text-to-speech/gone"] != 1 || !strings.Contains(ElevenMessage(err), "не найден") {
		t.Fatalf("gone: %v", err)
	}
	// 429 that never ends: gives up after Tries
	e2 := &Eleven{Base: srv.URL, HTTP: srv.Client(), Key: func() string { return key }, Tries: 2, Wait: func(int, time.Duration) time.Duration { return time.Millisecond }}
	n429 = -100
	if _, err := e2.Speak(ctx, "busy", "", "Четыре.", DefaultElevenSettings); err == nil {
		t.Fatal("endless 429 succeeded")
	}
}

func TestR36ElevenWait(t *testing.T) {
	if elevenWait(1, 0) != 2*time.Second || elevenWait(3, 0) != 8*time.Second || elevenWait(10, 0) != time.Minute {
		t.Fatal("exponential")
	}
	if elevenWait(1, 7*time.Second) != 7*time.Second || elevenWait(1, time.Hour) != 2*time.Minute {
		t.Fatal("retry-after")
	}
}
