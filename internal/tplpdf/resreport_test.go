package tplpdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderReport(t *testing.T) {
	if _, err := RenderReport(&ReportDoc{}); err == nil {
		t.Fatal("empty report rendered")
	}
	r := &ReportDoc{Resident: "Айдана Ким", Niche: "Сеть кофеен", City: "Алматы", Months: 3, From: "01.07.2026", To: "01.10.2026", Draft: true,
		Metrics: []ReportMetric{{Label: "Выручка в месяц", Unit: "₸", Before: 6500000, After: 9800000}, {Label: "Часы собственника в неделю", Unit: "ч", Before: 50, After: 28, Lower: true},
			{Label: "Пусто", Unit: "₸"}},
		Organs:    []ReportOrgan{{"Стратегия", 5, 7}, {"Маркетинг", 4, 6}, {"Продажи", 4, 8}, {"Команда", 3, 6}, {"Финансы", 3, 8}, {"Процессы", 4, 7}, {"Аналитика", 3, 7}},
		Diagnoses: []ReportDiag{{Title: "Кассовые разрывы", Organ: "Финансы", Closed: true}, {Title: "Нет системы продаж — воронка", Organ: "Продажи"}},
		TasksDone: 9, TasksTotal: 12, TasksList: []string{"Платёжный календарь"}, Tools: []string{"Платёжный календарь", "Воронка продаж"},
		Meetings: 8, MeetPlan: 9, ReportDays: 80, PlanDays: 92, Fines: 1, Gallup: []string{"Стратегия", "Достигатор"},
		Plan: []string{"Нанять управляющего"}, Note: "Итог хороший", Q3Price: 500000, YearPrice: 1500000}
	pdf, err := RenderReport(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) || len(pdf) < 20000 {
		t.Fatalf("pdf %d bytes", len(pdf))
	}
	if len(r.Metrics) != 2 || strings.Contains(r.Diagnoses[1].Title, "—") {
		t.Fatalf("clean: %+v", r)
	}
	if got := MetricValue(1500000, "₸"); got != "1 500 000 ₸" {
		t.Fatal(got)
	}
	if got := MetricDelta(ReportMetric{Before: 6500000, After: 9800000, Unit: "₸"}); got != "+51%" {
		t.Fatal(got)
	}
	if got := MetricDelta(ReportMetric{Before: 0, After: 3, Unit: ""}); got != "+3" {
		t.Fatal(got)
	}
}
