package http

import (
	"strings"
	"testing"
	"time"
)

// R50: the real export format of bxclub.kz (synthetic people): semicolons,
// quoted fields, formid only, the phone in Phone or in Input, the tariff
// form, quiz columns, «Ваш бизнес», Status ID / Stage.
const tildaRealCSV = "Email;Name;Phone;Date;Input;Hidden;Договор_офферты;tranid;formid;Checkbox;Что_сегодня_сильнее_всего_тормозит_ваш_бизнес;Какая_у_вас_сейчас_стадия_бизнеса;Что_вы_хотите_получить_от_разбора;Ваш_бизнес;\"Status ID\";Stage\n" +
	// January 2025 form: the phone in Input
	";Синтетик Один;;\"2025-01-08 10:00:00\";87010000101;;;1000001:1;form848255043;;;;;;1;Входящие\n" +
	";Синтетик Два;;\"2025-01-09 11:00:00\";\"+7 701 000 01 02\";;;1000002:2;form848255043;;;;;;1;Входящие\n" +
	// the tariff form: Input phone, Hidden tariff, offer accepted
	";Синтетик Тариф;;\"2025-03-01 12:00:00\";\"8 (701) 000-01-03\";\"1-ый тариф\";yes;1000003:3;form846585592;;;;;;1;Входящие\n" +
	";Синтетик Кыргыз;;\"2025-03-02 12:00:00\";0555000104;\"1-ый тариф\";yes;1000004:4;form846585592;;;;;;1;Входящие\n" +
	";Синтетик Битый;;\"2025-03-03 12:00:00\";00;\"1-ый тариф\";yes;1000005:5;form846585592;;;;;;1;Входящие\n" +
	// the review form: Phone, goal in Input, business with a semicolon inside quotes
	";Синтетик Разбор;\"+7 (705) 000-01-06\";\"2025-11-01 09:00:00\";\"Рост выручки вдвое\";;;1000006:6;form1447358181;;;;;\"Кафе; доставка\";1;Входящие\n" +
	// the same person later in the quiz (8 705 …): one card, the latest request
	";Синтетик Разбор;\"8 705 000 01 06\";\"2026-02-01 09:00:00\";;;;1000007:7;form1447374631;;\"⏳ Нет системы\";\"🚀 Только запускаюсь\";\"🎯 Понять, где теряется прибыль\";;1;Входящие\n" +
	// a site form with e-mail
	"syn@example.kz;Синтетик Почта;\"+7 (707) 000-01-08\";\"2025-08-21 10:00:00\";Консультация;;;1000008:8;form1234981126;;;;;;1;Входящие\n" +
	// tests
	";Test;\"+7 (777) 777-77-77\";\"2025-08-21 10:05:00\";;;;1000009:9;form1234981126;;;;;;1;Входящие\n" +
	";Hhh;7777777777777;\"2025-08-21 10:06:00\";;;;1000010:10;form1234985931;;;;;;1;Входящие\n" +
	";Test;\"+7 (701) 500-00-00\";\"2026-10-05 13:06:04\";-;;;1000011:11;form1768964711;;;;;-;1;Входящие\n" +
	";тест;\"+7 (701) 000-01-12\";\"2025-09-04 10:00:00\";;;;1000012:12;form1269547391;;;;;;1;Входящие\n" +
	";Gggg;\"+7 (701) 000-01-13\";\"2025-09-04 10:00:00\";;;;1000013:13;form1269547391;;;;;;1;Входящие\n" +
	";Синтетик Бизнес;\"+7 (701) 000-01-14\";\"2025-10-22 10:00:00\";;;;1000014:14;form1447358181;;;;;тест;1;Входящие\n"

func TestR50TildaPhoneNorm(t *testing.T) {
	for _, c := range []struct{ in, phone, flag string }{
		{"87011234567", "+77011234567", ""},
		{"+7 (701) 123-45-67", "+77011234567", ""},
		{"77011234567", "+77011234567", ""},
		{"7011234567", "+77011234567", ""},
		{"8 701 123 45 67", "+77011234567", ""},
		{"0555000104", "+996555000104", "Кыргызстан"},
		{"+996 555 000 104", "+996555000104", "Кыргызстан"},
		{"00", "", "некорректный"},
		{"+7 (916) 123-45-67", "+79161234567", "Россия"},
		{"12345", "", "некорректный"},
	} {
		p, k, f := TildaPhoneNorm(c.in)
		if p != c.phone || k == "" || (c.flag == "") != (f == "") || !strings.Contains(f, c.flag) {
			t.Fatalf("%q → %q %q %q", c.in, p, k, f)
		}
	}
}

func TestR50TildaRealFormat(t *testing.T) {
	rows, err := ParseTildaCSV(tildaRealCSV)
	if err != nil || len(rows) != 15 || !LooksTilda(rows[0]) || len(rows[6]) != 16 || rows[6][13] != "Кафе; доставка" {
		t.Fatalf("parse: %v %d", err, len(rows))
	}
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, almaty)
	existing := map[string]any{"id": "x1", "name": "Есть в CRM", "phone": "+77010000103", "source": "WhatsApp", "col": "work"}
	crm := map[string]any{"leads": []any{existing}}
	dry := ImportTildaWith(crm, rows, "tilda.csv", now, true, map[string]string{"form1234981126": "Старый лендинг"})
	if dry.Rows != 14 || dry.Tests != 6 || dry.Skip != 0 || dry.DupFile != 1 || dry.People != 7 || dry.Fresh != 6 || dry.Dup != 1 ||
		dry.Updated != 1 || dry.Flagged != 2 || dry.Done || len(crm["leads"].([]any)) != 1 {
		t.Fatalf("dry %+v", dry)
	}
	if dry.Segs[TildaSegTariff] != 3 || dry.Segs[TildaSegSite] != 4 || dry.Segs[TildaSegTests] != 6 || len(dry.TestList) != 6 || len(dry.FlagList) != 2 {
		t.Fatalf("segs %v", dry.Segs)
	}
	names := map[string]TildaForm{}
	for _, f := range dry.FormList {
		names[f.ID] = f
	}
	if names["form846585592"].Name != "Покупка тарифа" || names["form848255043"].Name != "Заявка январь 2025 (лид-магнит?)" ||
		names["form1447358181"].Name != "Заявка на разбор (бизнес и цель)" || names["form1447374631"].Name != "Квиз: стадия и боли" ||
		names["form1768964711"].Name != "Заявка с сайта" || names["form1234981126"].Name != "Старый лендинг" || names["form1234981126"].Def ||
		names["form1234981126"].Tests != 1 || names["form846585592"].Rows != 3 || names["form1447374631"].Leads != 1 || names["form1447358181"].Leads != 0 {
		t.Fatalf("forms %+v", dry.FormList)
	}
	if dry.Forms["Покупка тарифа"] != 3 || dry.From != "08.01.2025" || dry.To != "01.02.2026" {
		t.Fatalf("forms/range %v %s %s", dry.Forms, dry.From, dry.To)
	}
	rep := ImportTildaWith(crm, rows, "tilda.csv", now, false, map[string]string{"form1234981126": "Старый лендинг"})
	if rep.Fresh != 6 || !rep.Done {
		t.Fatalf("import %+v", rep)
	}
	if m, _ := crm["tildaForms"].(map[string]any); m["form1234981126"] != "Старый лендинг" {
		t.Fatalf("names not kept %v", crm["tildaForms"])
	}
	by := map[string]map[string]any{}
	for _, x := range crm["leads"].([]any) {
		l := x.(map[string]any)
		by[pStr(l, "name")] = l
		if strings.Contains(strings.ToLower(pStr(l, "name")), "test") || pStr(l, "name") == "тест" || pStr(l, "name") == "Gggg" {
			t.Fatalf("a test became a lead %v", l)
		}
	}
	one := by["Синтетик Один"]
	if one["phone"] != "+77010000101" || one["source"] != "Сайт (Tilda): Заявка январь 2025 (лид-магнит?)" || one["date"] != "08.01.2025" || one["goal"] != nil {
		t.Fatalf("january %v", one)
	}
	if by["Синтетик Два"]["phone"] != "+77010000102" {
		t.Fatalf("input phone %v", by["Синтетик Два"])
	}
	kg := by["Синтетик Кыргыз"]
	if kg["phone"] != "+996555000104" || !strings.Contains(pStr(kg, "phoneFlag"), "Кыргызстан") || kg["phoneRaw"] != "0555000104" ||
		kg["intent"] != "tariff" || kg["hot"] != true || kg["prio"] != "high" || kg["tariff"] != "1-ый тариф" || !strings.Contains(pStr(kg, "note"), "договор оферты") {
		t.Fatalf("kyrgyz tariff %v", kg)
	}
	bad := by["Синтетик Битый"]
	if bad["phone"] != "" || bad["phoneRaw"] != "00" || !strings.Contains(pStr(bad, "phoneFlag"), "некорректный") || bad["intent"] != "tariff" {
		t.Fatalf("broken phone kept %v", bad)
	}
	rz := by["Синтетик Разбор"]
	if rz["phone"] != "+77050000106" || rz["niche"] != "Кафе; доставка" || rz["goal"] != "Рост выручки вдвое" || rz["date"] != "01.02.2026" ||
		rz["form"] != "Квиз: стадия и боли" || rz["tildaN"] != 2 || rz["id"] != "site10000077" || rz["firstAt"] == nil ||
		!strings.Contains(pStr(rz, "note"), "Понять, где теряется прибыль") || !strings.Contains(pStr(rz, "note"), "Что сегодня сильнее всего тормозит ваш бизнес? ⏳ Нет системы") {
		t.Fatalf("merged person %v", rz)
	}
	lg := asList(rz["log"])
	if len(lg) != 3 || !strings.Contains(pStr(lg[0].(map[string]any), "text"), "цель: Рост выручки вдвое") ||
		pStr(lg[0].(map[string]any), "at") != time.Date(2025, 11, 1, 9, 0, 0, 0, almaty).UTC().Format(time.RFC3339) {
		t.Fatalf("history %v", lg)
	}
	ml := by["Синтетик Почта"]
	if ml["email"] != "syn@example.kz" || ml["goal"] != "Консультация" || ml["source"] != "Сайт (Tilda): Старый лендинг" {
		t.Fatalf("mail %v", ml)
	}
	// the card already in the CRM: marked as a site lead who wanted the tariff, stage and source kept
	if existing["intent"] != "tariff" || existing["hot"] != true || existing["source"] != "WhatsApp" || existing["col"] != "work" || existing["tildaAt"] == nil {
		t.Fatalf("existing %v", existing)
	}
	// segments: the tariff people get their own segment
	SegmentCRM(crm, SegPeople{}, now)
	if kg["seg"] != SegTariff || bad["seg"] != SegTariff || rz["seg"] != SegSite || existing["seg"] != SegTariff || kg["segForm"] != "Покупка тарифа" {
		t.Fatalf("segments %v %v %v", kg["seg"], rz["seg"], existing["seg"])
	}
	// the same file again: nothing new, nothing marked twice
	if again := ImportTilda(crm, rows, "tilda.csv", now, false); again.Fresh != 0 || again.Updated != 0 || again.Dup != 7 {
		t.Fatalf("again %+v", again)
	}
}
