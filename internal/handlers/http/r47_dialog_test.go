package http

import (
	"context"
	"strings"
	"testing"
	"time"
)

// R47: a Threads lead's whole conversation is on the CRM card: what the lead
// sent (/start, a button, free text) and what the bot answered, in order,
// with the buttons; the team's notes are not in it.
func TestR47LeadDialog(t *testing.T) {
	e := newR40Env(t)
	e.f.RecordDialog()
	e.f.RecordDialog() // twice is once
	ctx := context.Background()

	e.msg(7002, "/start th_99", time.Now())
	got := e.wait(7002, 2)
	e.press(7002, 2, "lm_pain_fin")
	e.wait(7002, 3)
	e.msg(7002, "Сколько стоит участие в клубе?", time.Now())
	e.wait(7002, 4)
	time.Sleep(300 * time.Millisecond)

	d := LeadDialog(ctx, e.repo, 7002)
	var lines []string
	for _, m := range d {
		lines = append(lines, m.D+": "+m.T)
	}
	all := strings.Join(lines, "\n")
	if len(d) < 6 {
		t.Fatalf("dialog too short:\n%s", all)
	}
	if d[0].D != "in" || !strings.HasPrefix(d[0].T, "/start (Threads") {
		t.Fatalf("first is the lead's start:\n%s", all)
	}
	if d[1].D != "out" || !strings.Contains(d[1].T, "Business Surgery") || strings.Contains(d[1].T, "<b>") || len(d[1].B) == 0 {
		t.Fatalf("welcome with buttons, no markup:\n%s\n%+v", all, d[1])
	}
	pain := ""
	for _, b := range d[2].B {
		if b.D == "lm_pain_fin" {
			pain = b.T
		}
	}
	if pain == "" || got[1].text() == "" {
		t.Fatalf("pain ask buttons: %+v", d[2])
	}
	if !strings.Contains(all, "in: ▸ "+pain) {
		t.Fatalf("the press is named by its button %q:\n%s", pain, all)
	}
	if !strings.Contains(all, "in: Сколько стоит участие в клубе?") || !strings.Contains(all, "out: Спасибо, сообщение получил") {
		t.Fatalf("free text and the answer:\n%s", all)
	}
	for _, m := range d {
		if m.At <= 0 || (m.D != "in" && m.D != "out") || len([]rune(m.T)) > dlgTextMax {
			t.Fatalf("bad entry %+v", m)
		}
	}
	for i := 1; i < len(d); i++ {
		if d[i].At < d[i-1].At {
			t.Fatalf("not in time order:\n%s", all)
		}
	}
	// the team's notes stay out of any dialog
	if len(LeadDialog(ctx, e.repo, 111)) != 0 {
		t.Fatal("admin chat recorded")
	}
	// the card still has the reply marks for the kanban line
	if l := e.lead(7002); l == nil || l["botReplyAt"] == nil {
		t.Fatalf("card: %v", l)
	}
}

func TestR47DialogCaps(t *testing.T) {
	docs := &r38Docs{}
	f := NewLeadFunnel(docs, func(context.Context, int64, string, map[string]any) error { return nil }, []int64{1})
	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	tick := int64(0)
	f.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) }
	f.RecordDialog()
	ctx := context.Background()
	for i := 0; i < dlgPerChat+5; i++ {
		_ = f.send(ctx, 42, "<b>Сообщение</b> &amp; "+strings.Repeat("я", 400), nil)
	}
	d := LeadDialog(ctx, docs, 42)
	if len(d) != dlgPerChat || strings.Contains(d[0].T, "<b>") || !strings.Contains(d[0].T, "Сообщение & ") || !strings.HasSuffix(d[0].T, "…") {
		t.Fatalf("cap/clean: %d %q", len(d), d[0].T)
	}
	for c := int64(100); c < 100+dlgChats+10; c++ {
		_ = f.send(ctx, c, "привет", nil)
	}
	if len(LeadDialog(ctx, docs, 42)) != 0 || len(LeadDialog(ctx, docs, 100+dlgChats+9)) != 1 {
		t.Fatal("oldest chats must go first")
	}
	_ = f.send(ctx, 1, "команде", nil)
	if len(LeadDialog(ctx, docs, 1)) != 0 {
		t.Fatal("admin recorded")
	}
	// a lead's dated message lands before the bot's answer of the same second
	f.now = func() time.Time { return base.Add(time.Hour) }
	_ = f.send(ctx, 500, "ответ", nil)
	f.noteDialogAt(ctx, 500, base.Add(time.Hour).Unix(), "in", "вопрос", nil)
	d = LeadDialog(ctx, docs, 500)
	if len(d) != 2 || d[0].T != "вопрос" || d[1].T != "ответ" {
		t.Fatalf("order: %+v", d)
	}
}
