package tplpdf

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func printSample() *PrintDoc {
	return &PrintDoc{Resident: "Асет Нуров", Business: "Кофейня «Зерно»", Date: "09.10.2026", Cycle: 2, Tracker: "Рустам",
		PointA: "Выручка 9 000 000 ₸, прибыль 600 000 ₸, собственник в операционке 60 часов в неделю",
		PointB: "Выручка 14 000 000 ₸, прибыль 2 000 000 ₸, собственник 20 часов в неделю",
		Diags: []PrintDiag{{Title: "Кассовые разрывы", Symptoms: []string{"Платит поставщикам из выручки дня", "Нет резерва на налоги"}},
			{Title: "Все решения через собственника — даже закуп", Symptoms: []string{"Управляющая ждёт согласования"}},
			{Title: "Нет системы продаж"}},
		Tasks: []PrintTask{{Text: "Собрать платёжный календарь на 30 дней", Owner: "Асет", Due: "15.10"},
			{Text: "Передать закуп управляющей", Owner: "Асет", Due: "постоянно", Done: true}},
		Tools: []PrintTool{{Title: "Платёжный календарь", How: "Выпишите все платежи с датами и посчитайте остаток на каждый день", Diag: "Кассовые разрывы"},
			{Title: "Скрипт продаж"}},
		Next:  "19.10.2026 · 10:00 · Достык 44",
		Notes: []string{"Клиент готов к отдельному счёту под налоги"}}
}

func TestRenderRazborPrint(t *testing.T) {
	b, err := RenderRazborPrint(printSample())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF")) || len(b) < 20000 {
		t.Fatalf("not a pdf: %d bytes", len(b))
	}
	if out := os.Getenv("R69_PDF"); out != "" {
		_ = os.WriteFile(out, b, 0o644)
	}
	// many tasks: more pages, no error
	d := printSample()
	for i := 0; i < 50; i++ {
		d.Tasks = append(d.Tasks, PrintTask{Text: strings.Repeat("Длинная задача ", 6), Owner: "Асет", Due: "20.10"})
	}
	if _, err := RenderRazborPrint(d); err != nil {
		t.Fatal(err)
	}
}

func TestRazborPrintCleanAndName(t *testing.T) {
	d := printSample()
	d.Clean()
	if strings.Contains(d.Diags[1].Title, "—") {
		t.Fatal("long dash kept")
	}
	if got := d.FileName(); got != "Разбор Асет Нуров 09.10.2026.pdf" {
		t.Fatalf("name %q", got)
	}
	if _, err := RenderRazborPrint(&PrintDoc{Resident: "Пусто"}); err == nil {
		t.Fatal("empty board rendered")
	}
}
