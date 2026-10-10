package http

import "testing"

// R74: камера у каждого (отметка cam видна всем) и созвон до четырёх вкладок.
func TestR74CallCamAndMesh(t *testing.T) {
	h := newCallHub()
	b := "bAlt"
	mk := func(id, name string, cam bool) callMember {
		return callMember{ID: id, Key: id + "|t1", Name: name, Role: "admin", Cam: cam, Team: true}
	}
	tr, rs, pa, fo, extra := mk("1", "Рустам", true), mk("2", "Альтаир", true), mk("3", "Партнёр", false), mk("4", "Береке", true), mk("5", "Пятый", true)
	for _, m := range []callMember{tr, rs, pa, fo} {
		if _, ok := h.Op(b, m, "join"); !ok {
			t.Fatalf("%s не вошёл", m.Name)
		}
	}
	v, ok := h.Op(b, extra, "join")
	if ok || len(v.Members) != 4 || v.In {
		t.Fatalf("пятый вошёл: ok=%v members=%d", ok, len(v.Members))
	}
	cams := map[string]bool{}
	for _, m := range v.Members {
		cams[m.Name] = m.Cam
	}
	if !cams["Рустам"] || !cams["Альтаир"] || cams["Партнёр"] || !cams["Береке"] {
		t.Fatalf("камеры: %v", cams)
	}
	if v, _ = h.Op(b, tr, "nocam"); v.Members[0].Cam {
		t.Fatal("камера трекера не выключилась")
	}
	if v, _ = h.Op(b, pa, "cam"); !v.Members[2].Cam {
		t.Fatal("камера партнёра не включилась")
	}
	if _, ok := h.Op(b, extra, "cam"); ok {
		t.Fatal("камера не участника принята")
	}
	// повторный вход (сервер перезапустился, вкладка вернулась) не упирается в предел
	if _, ok := h.Op(b, rs, "join"); !ok {
		t.Fatal("повторный вход участника отклонён")
	}
	h.Op(b, fo, "leave")
	if _, ok := h.Op(b, extra, "join"); !ok {
		t.Fatal("после выхода четвёртого место не освободилось")
	}
}
