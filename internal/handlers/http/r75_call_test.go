package http

import (
	"strings"
	"testing"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/calllink"
	"github.com/bnursik/business_surgery_backend/internal/gcal"
)

// R75 call: команда входит сразу, резидент и гость ждут в лобби, впустить,
// отклонить, «автоматически» (без гостей), остановить показ гостя.
func TestR75CallLobby(t *testing.T) {
	h := newCallHub()
	host := callMember{ID: "tg:1", Key: "tg:1|a", Name: "Рустам", Team: true}
	bk := callMember{ID: "tg:2", Key: "tg:2|a", Name: "Береке", Team: true}
	res := callMember{ID: "tg:3", Key: "tg:3|a", Name: "Альтаир", Role: "resident"}
	guest := callMember{ID: "g:abc", Key: "g:abc|a", Name: "Гость", Guest: true}

	if v, err := h.OpWho("b", host, "join", ""); err != "" || !v.In {
		t.Fatalf("host joins directly: %v %+v", err, v)
	}
	if v, _ := h.OpWho("b", bk, "join", ""); !v.In {
		t.Fatal("second founder joins directly")
	}
	v, err := h.OpWho("b", res, "join", "")
	if err != "" || v.In || !v.Wait {
		t.Fatalf("resident waits: %v %+v", err, v)
	}
	if len(v.Lobby) != 0 {
		t.Fatal("the resident does not see the lobby list")
	}
	hv, _ := h.OpWho("b", host, "peek", "")
	if len(hv.Lobby) != 1 || hv.Lobby[0].Key != res.Key || len(hv.Members) != 2 {
		t.Fatalf("host sees the lobby: %+v", hv)
	}
	if !h.Send("b", host.Key, bk.Key, callMsg{Kind: "hello"}) || h.Send("b", res.Key, host.Key, callMsg{Kind: "hello"}) {
		t.Fatal("a waiting tab cannot signal")
	}
	if _, err := h.OpWho("b", res, "admit", res.Key); err != "team_only" {
		t.Fatalf("resident cannot admit: %q", err)
	}
	if _, err := h.OpWho("b", host, "admit", res.Key); err != "" {
		t.Fatal(err)
	}
	if v, _ := h.OpWho("b", res, "peek", ""); !v.In || v.Wait {
		t.Fatalf("admitted: %+v", v)
	}
	// перезагрузка вкладки: впущенный входит снова сразу
	h.OpWho("b", res, "leave", "")
	res2 := res
	res2.Key = "tg:3|b"
	if v, _ := h.OpWho("b", res2, "join", ""); !v.In {
		t.Fatal("an admitted person comes back without the lobby")
	}
	// гость: «автоматически» его не впускает
	h.OpWho("b", host, "auto", "")
	if v, _ := h.OpWho("b", guest, "join", ""); v.In || !v.Wait {
		t.Fatal("a guest waits even with auto")
	}
	if _, err := h.OpWho("b", host, "admit", guest.Key); err != "" {
		t.Fatal(err)
	}
	// показ гостя останавливает команда, гость получает stopshare
	h.OpWho("b", guest, "present", "")
	if v, err := h.OpWho("b", host, "stopshare", guest.Key); err != "" || v.Presenter != nil {
		t.Fatalf("stopshare: %q %+v", err, v)
	}
	_, msgs := h.take("b", guest.Key)
	if len(msgs) != 1 || msgs[0].Kind != "stopshare" {
		t.Fatalf("guest letter: %+v", msgs)
	}
	// пятый ждёт, впустить некуда; отклонить
	g2 := callMember{ID: "g:zzz", Key: "g:zzz|a", Name: "Пятый", Guest: true}
	h.OpWho("b", g2, "join", "")
	if _, err := h.OpWho("b", host, "admit", g2.Key); err != "room_full" {
		t.Fatalf("room full: %q", err)
	}
	h.OpWho("b", host, "deny", g2.Key)
	if v, _ := h.OpWho("b", g2, "peek", ""); !v.Denied || v.Wait {
		t.Fatalf("denied: %+v", v)
	}
	// лобби без потока уходит через 20 с
	h.OpWho("b", g2, "join", "")
	now := time.Now()
	h.now = func() time.Time { return now.Add(time.Minute) }
	if v, _ := h.OpWho("b", host, "peek", ""); len(v.Lobby) != 0 {
		t.Fatal("stale lobby expires")
	}
}

func TestR75GuestPass(t *testing.T) {
	l := &CallLinks{Secret: []byte("s1")}
	tok := l.sign(callGuestClaims{R: "abcdefghijklmnop", G: "g1234567", N: "Алия", E: time.Now().Add(time.Hour).Unix()})
	if cl, ok := l.parse(tok); !ok || cl.N != "Алия" {
		t.Fatal("pass")
	}
	if _, ok := (&CallLinks{Secret: []byte("s2")}).parse(tok); ok {
		t.Fatal("another secret")
	}
	if _, ok := l.parse(strings.Replace(tok, ".", "x.", 1)); ok {
		t.Fatal("tampered")
	}
	old := l.sign(callGuestClaims{R: "abcdefghijklmnop", G: "g1", N: "x", E: time.Now().Add(-time.Minute).Unix()})
	if _, ok := l.parse(old); ok {
		t.Fatal("expired")
	}
}

func TestR75CallLink(t *testing.T) {
	t.Setenv("JWT_SECRET", "x")
	t.Setenv("BS_CALL_BASE", "")
	t.Setenv("PUBLIC_URL", "")
	a, b := calllink.ID("Пётр  Иванов"), calllink.ID("петр иванов")
	if a != b || !calllink.IDRe.MatchString(a) {
		t.Fatalf("one id per person: %q %q", a, b)
	}
	if u := calllink.URL("Альтаир"); !strings.HasPrefix(u, "https://app.bxclub.kz/call/") {
		t.Fatal(u)
	}
	d := gcal.CallDesc("Встреча", "https://app.bxclub.kz/call/x")
	if !strings.HasPrefix(d, "🎥 Созвон на платформе") || gcal.CallDesc(d, "https://app.bxclub.kz/call/x") != d {
		t.Fatal(d)
	}
}
