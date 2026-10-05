package http

import "testing"

func TestR39NPS(t *testing.T) {
	// prod: Chat ID in the score column is not a score (it was averaged: 354788446)
	r := ComputeNPS([]map[string]any{{"date": "18.06.2026", "res": "Мади", "score": float64(354788446), "comment": "Честная обратная связь"}})
	if r.Count != 0 || r.Answers != 1 || r.NPS != 0 {
		t.Fatalf("%+v", r)
	}
	r = ComputeNPS([]map[string]any{
		{"date": "01.10.2026", "res": "A", "score": "10"},
		{"date": "02.10.2026", "res": "B", "score": "9/10"},
		{"date": "02.10.2026", "res": "C", "score": "5"},
		{"date": "03.10.2026", "res": "C", "score": "7"}, // the newest answer of C counts
		{"date": "03.10.2026", "res": "D", "comment": "оценка: 3, долго"},
		{"date": "01.06.2026", "res": "E", "score": float64(0)}, // an older survey
	})
	if r.NPS != 25 || r.Avg != 7.3 || r.Count != 4 || r.Promoters != 2 || r.Passives != 1 || r.Detractors != 1 || r.Answers != 4 {
		t.Fatalf("%+v", r)
	}
	// bounds: all detractors −100, all promoters 100
	if r := ComputeNPS([]map[string]any{{"date": "01.10.2026", "name": "x", "text": "2"}, {"date": "01.10.2026", "name": "y", "text": "0 из 10"}}); r.NPS != -100 {
		t.Fatalf("%+v", r)
	}
	if r := ComputeNPS([]map[string]any{{"date": "2026-10-01", "chatId": "1", "text": "9"}, {"date": "2026-10-01", "chatId": "2", "score": float64(10)}}); r.NPS != 100 || r.Avg != 9.5 {
		t.Fatalf("%+v", r)
	}
	for in, want := range map[string]int{"9": 9, " 10 ": 10, "8 из 10": 8, "7/10": 7, "Оценка 6": 6, "10. Всё супер": 10} {
		if v, ok := npsScore(in); !ok || v != want {
			t.Fatalf("%q → %d %v", in, v, ok)
		}
	}
	for _, in := range []any{"354788446", float64(11), "Пока не знаю", "", float64(-1)} {
		if v, ok := npsScore(in); ok {
			t.Fatalf("%v → %d", in, v)
		}
	}
}
