package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// R67: «БХ Экспресс разбор» under Рустам's (or anyone's) name is statistics,
// not membership: it never pays a resident's debt and is never reported as
// «не учтено».
func TestR67ExpressReviewIsNotMembership(t *testing.T) {
	e := newOffEnv(t, "sheet", nil)
	ctx := context.Background()
	if _, err := e.db.Pool.Exec(ctx, `UPDATE club_residents SET renew_debt = 50000, rest_entry = 0 WHERE name = 'Альтаир'`); err != nil {
		t.Fatal(err)
	}
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "50000", "src", "БХ Экспресс разбор", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment %v", r)
	}
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 50000 {
		t.Fatalf("an express review paid the debt: %d left", n)
	}
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "50000", "src", "БХ Экспресс разбор", "resident", "Рустам", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment 2 %v", r)
	}
	a := time.Now().In(club.Almaty)
	done, left, err := e.repo.AutoCountPayments(ctx, time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1))
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 || len(left) != 0 {
		t.Fatalf("express reviews counted %v or reported «не учтено» %v", done, left)
	}
	// a membership payment still pays the debt
	if r := e.call(offOwner, "addPayment", "type", "income", "amount", "50000", "src", "БХ Трекинг продление", "resident", "Альтаир", "isCash", "true"); r["ok"] != true {
		t.Fatalf("payment 3 %v", r)
	}
	if n := e.n(`SELECT renew_debt FROM club_residents WHERE name = 'Альтаир'`); n != 0 {
		t.Fatalf("renewal not paid: %d", n)
	}
	for _, c := range []string{"БХ Экспресс разбор", "Экспресс-разбор", "БХ МК", "МК"} {
		if !club.NonMembershipCat(c) {
			t.Fatalf("%q is membership", c)
		}
	}
	for _, c := range []string{"БХ Трекинг продление", "Вход резидент", "Мкр. Самал"} {
		if club.NonMembershipCat(c) {
			t.Fatalf("%q is not membership", c)
		}
	}
}

// R67: a meeting marked in the app is never asked about; an unmarked one is,
// with a button straight to it.
func TestR67MeetAskRespectsAppMark(t *testing.T) {
	e := newOffEnv(t, "sheet", nil)
	ctx := context.Background()
	a := time.Now().In(club.Almaty)
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	day := today.Format("02.01.2006")
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM club_meetings`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"Альтаир", "Асет"} {
		if x := e.call(offOwner, "addSchedule", "res", r, "date", day, "time", "12:00"); x["ok"] != true {
			t.Fatalf("schedule %s %v", r, x)
		}
	}
	if x := e.call(offOwner, "confirmMeeting", "res", "Альтаир", "date", day, "time", "12:00"); x["ok"] != true {
		t.Fatalf("confirm %v", x)
	}
	res := e.svc.Tick(ctx, today.Add(13*time.Hour+40*time.Minute))
	if len(res.MeetAsks) != 1 || strings.Join(res.MeetAsks[0].Names, ",") != "Асет" {
		t.Fatalf("asks %+v (errors %v)", res.MeetAsks, res.Errors)
	}
	var asked []map[string]any
	e.tg.mu.Lock()
	for _, c := range e.tg.calls {
		if s, _ := c["text"].(string); strings.Contains(s, "прошла?") {
			asked = append(asked, c)
		}
	}
	e.tg.mu.Unlock()
	if len(asked) != 1 || !strings.Contains(asked[0]["text"].(string), "Асет") {
		t.Fatalf("messages %v", asked)
	}
	b, _ := json.Marshal(asked[0]["reply_markup"])
	if !strings.Contains(string(b), "p=meet_"+today.Format("20060102")+"_1200") {
		t.Fatalf("button %s", b)
	}
	// once
	if again := e.svc.Tick(ctx, today.Add(13*time.Hour+50*time.Minute)); len(again.MeetAsks) != 0 {
		t.Fatalf("asked twice %+v", again.MeetAsks)
	}
}
