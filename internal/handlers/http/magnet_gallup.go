package http

// R76: магнит «34 таланта Gallup глазами собственника»: открытая страница
// /free/gallup из той же базы, по которой платформа делает разбор Gallup
// резидентам (platform_gallup_kb.go): как талант проявляется в продажах,
// команде, деньгах и найме, слепая зона, что выжигает, эксперимент на 10
// дней; по каждому домену роль в компании и кого ставить рядом.

import (
	"html"
	"strings"

	"github.com/bnursik/business_surgery_backend/web"
)

func init() { web.AddFreePage("/free/gallup", gallupFreeHTML) }

const gallupFreeCSS = `
.gd{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin-top:14px;align-items:start}
.gd details{border:1px solid var(--line);border-radius:10px;background:var(--card);padding:0}
.gd summary{cursor:pointer;list-style:none;padding:14px 16px;font-weight:700;font-size:16px;display:flex;justify-content:space-between;gap:10px}
.gd summary::-webkit-details-marker{display:none}.gd summary em{font-style:normal;font-size:13px;color:var(--mute);font-weight:600}
.gd details[open] summary{border-bottom:1px solid var(--line)}
.gd dl{padding:6px 16px 14px}.gd dt{font-size:12px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--mute);margin-top:10px}
.gd dd{font-size:14.5px;color:var(--soft);line-height:1.5;margin-top:2px}
.dm{padding:16px 18px;border:1px solid var(--line);border-radius:10px;margin-top:10px}
.dm p{font-size:14.5px;color:var(--soft)}.dm b{color:#fff}
@media(max-width:760px){.gd{grid-template-columns:1fr}}
@media print{.gd details{break-inside:avoid}}
`

func gallupFreeHTML() string {
	hx := html.EscapeString
	var b strings.Builder
	b.WriteString(`<div class="hero fh"><div class="w"><span class="pill">Справочник · бесплатно</span><h1>34 таланта Gallup <span>глазами собственника бизнеса</span></h1>` +
		`<p class="lead">Как каждый талант CliftonStrengths проявляется в продажах, команде, деньгах и найме, в чём его слепая зона, что выжигает и какой эксперимент на 10 дней попробовать. Найдите свою пятёрку из отчёта Gallup.</p><div class="chips">`)
	for _, d := range []string{"executing", "influencing", "relationship", "strategic"} {
		b.WriteString(`<a class="chip" href="#` + d + `">` + hx(gallupDomainRu[d]) + `</a>`)
	}
	b.WriteString(`</div></div></div>`)
	for _, d := range []string{"executing", "influencing", "relationship", "strategic"} {
		dk := gallupDomainKB[d]
		b.WriteString(`<section id="` + d + `"><div class="w"><h2>` + hx(gallupDomainRu[d]) + `</h2>`)
		b.WriteString(`<div class="dm"><p><b>Роль в компании.</b> ` + hx(dk.Role) + `.</p><p><b>Если это ваш ведущий домен:</b> вы ` + hx(dk.Own) + `.</p>` +
			`<p><b>Если это ваш слабый домен:</b> ` + hx(dk.Weak) + `. Рядом нужен ` + hx(dk.Seek) + `.</p></div><div class="gd">`)
		for _, th := range gallupThemes {
			if th[3] != d {
				continue
			}
			k, ok := gallupKB[th[0]]
			if !ok {
				continue
			}
			b.WriteString(`<details><summary>` + hx(th[2]) + `<em>` + hx(th[1]) + `</em></summary><dl>`)
			for _, f := range [][2]string{{"Продажи", k.Sales}, {"Команда", k.Team}, {"Деньги", k.Money}, {"Найм", k.Hire}, {"Слепая зона", k.Blind}, {"Что выжигает", k.Burnout}, {"Эксперимент на 10 дней", k.Exp}} {
				if f[1] != "" {
					b.WriteString(`<dt>` + f[0] + `</dt><dd>` + hx(f[1]) + `</dd>`)
				}
			}
			b.WriteString(`</dl></details>`)
		}
		b.WriteString(`</div></div></section>`)
	}
	b.WriteString(web.MagnetCTA("gallup", "Полный разбор вашей пятёрки", "Резиденты клуба получают разбор Gallup под свой бизнес: сильные стороны, слепые зоны, кого нанять рядом и как вести цикл на 10 дней. Вход в клуб начинается с экспресс-разбора."))
	return web.PublicShell("34 таланта Gallup для собственника бизнеса | Business Surgery",
		"Справочник Business Surgery: 34 таланта Gallup CliftonStrengths в продажах, команде, деньгах и найме собственника, слепые зоны и эксперимент на 10 дней.", "/free/gallup", gallupFreeCSS, b.String(), "")
}
