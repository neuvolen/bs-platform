package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R47: a photo in the group gets 👍 like a video note; an album gets one 👍;
// a photo with a long caption is not taken for a report; the feature switch
// governs photos as it governs video notes.
func TestPhotoReaction(t *testing.T) {
	e := newFeatEnv(t)
	ctx := context.Background()
	evening := time.Date(club.Today().Year(), club.Today().Month(), club.Today().Day(), 21, 0, 0, 0, club.Almaty)
	photo := `,"photo":[{"file_id":"p1","width":90,"height":90},{"file_id":"p2","width":800,"height":800}]`

	// Off: nothing, as for video notes.
	e.group(t, 1001, 2, "", evening, photo)
	if len(e.got) != 0 {
		t.Fatalf("feature off, calls %v", e.got)
	}
	if _, err := e.s.SetFeatures(ctx, []string{FeatureReportFeedback}); err != nil {
		t.Fatal(err)
	}

	thumbs := func() int {
		n := 0
		for _, c := range e.calls("setMessageReaction") {
			if strings.Contains(fmt.Sprint(c.P["reaction"]), "👍") {
				n++
			}
		}
		return n
	}

	e.group(t, 1001, 2, "", evening, photo) // «Общение»
	if r := e.calls("setMessageReaction"); len(r) != 1 || thumbs() != 1 || r[0].P["chat_id"].(float64) != -1002494126345 {
		t.Fatalf("photo reaction %v", e.got)
	}
	e.reset()

	// Same as a video note: any topic, any sender, no private notes.
	e.group(t, 555, 9, "", evening, photo+`,"caption":"`+strings.Repeat("Сделал три встречи. ", 8)+`"`)
	e.group(t, 1002, 2, "", evening, `,"document":{"file_id":"d","mime_type":"image/jpeg"}`)
	e.group(t, 1002, 2, "", evening, `,"video_note":{"file_id":"v"}`)
	if thumbs() != 3 || len(e.calls("sendMessage")) != 0 {
		t.Fatalf("photo with caption / image file / video note: %v", e.got)
	}
	e.reset()

	// A PDF is not a photo: no reaction.
	e.group(t, 1002, 2, "", evening, `,"document":{"file_id":"d","mime_type":"application/pdf"}`)
	if len(e.got) != 0 {
		t.Fatalf("pdf reacted: %v", e.got)
	}

	// An album of 3 photos: one 👍 (the first), the next album gets its own.
	for i := 0; i < 3; i++ {
		e.group(t, 1001, 2, "", evening, photo+`,"media_group_id":"alb1"`)
	}
	if thumbs() != 1 {
		t.Fatalf("album: want one reaction, got %v", e.got)
	}
	e.group(t, 1001, 2, "", evening, photo+`,"media_group_id":"alb2"`)
	if thumbs() != 2 {
		t.Fatalf("second album: %v", e.got)
	}
	e.reset()

	// A photo is never a report: nothing recorded for the sender.
	var n int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM bot_reports WHERE tg_user_id = 555`).Scan(&n); err == nil && n != 0 {
		t.Fatalf("photo caption recorded as a report: %d", n)
	}

	// Telegram refuses: the log, never the chat.
	e.fail["setMessageReaction"] = "Bad Request: REACTION_INVALID"
	e.group(t, 1001, 2, "", evening, photo)
	if len(e.calls("sendMessage")) != 0 {
		t.Fatalf("refused reaction reached the chat: %v", e.got)
	}
}

func TestReadGroupMessagePhoto(t *testing.T) {
	b := []byte(`{"update_id":1,"message":{"message_id":7,"date":1,"chat":{"id":-1,"type":"supergroup"},"from":{"id":5},"photo":[{"file_id":"x"}],"caption":"подпись","media_group_id":"g"}}`)
	m, ok := ReadGroupMessage(b)
	if !ok || !m.Media || m.Album != "g" || m.Text != "" {
		t.Fatalf("%+v", m)
	}
	if d := Decide(m, "9", func(int64, string) (string, bool) { return "X", true }); d.Record {
		t.Fatal("photo recorded")
	}
	m, _ = ReadGroupMessage([]byte(`{"update_id":1,"message":{"message_id":7,"date":1,"chat":{"id":-1,"type":"supergroup"},"from":{"id":5},"document":{"mime_type":"application/pdf"}}}`))
	if m.Media {
		t.Fatal("pdf is media")
	}
}
