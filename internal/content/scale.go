package content

import "strconv"

// R60: «на видео упор на 99 чек-листов, но это же мало: у нас 1000+
// бизнес-идей, много диагнозов, инструментов». The library's real scale for
// texts that sell the club: counted from the files, so it never overstates.

// Scale: diagnoses and tools of the rich library, ideas in the catalog.
func Scale() (diag, tools, ideas int) {
	t, d := RichTitles()
	return len(d), len(t), IdeasTotal()
}

// ScaleText: «173 диагноза, 259 инструментов с шаблонами и 1 059 бизнес-идей».
func ScaleText() string {
	d, t, i := Scale()
	return Num(d) + " " + Plural(d, "диагноз", "диагноза", "диагнозов") + ", " +
		Num(t) + " " + Plural(t, "инструмент", "инструмента", "инструментов") + " с шаблонами и " +
		Num(i) + " " + Plural(i, "бизнес-идея", "бизнес-идеи", "бизнес-идей")
}

// Num: 1059 → «1 059» (no-break space between thousands).
func Num(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + Num(-n)
	}
	out := ""
	for len(s) > 3 {
		out = " " + s[len(s)-3:] + out
		s = s[:len(s)-3]
	}
	return s + out
}

// Plural picks the Russian form for n: 1 диагноз, 2 диагноза, 5 диагнозов.
func Plural(n int, one, few, many string) string {
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}
