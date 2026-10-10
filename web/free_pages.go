package web

// R76: лид-магниты Threads. Владелец: «нужно, чтобы цепляло, вот как про 99
// бесплатных чек-листов: "бесплатно собрал для вас 1000+ бизнес-идей". Такое
// должно быть постоянно». Каждый магнит из постов открывается без входа:
//
//   /ideas            1 059 бизнес-идей: фильтры по направлению, бюджету,
//                     формату, «можно одному», «из дома», поиск
//   /ideas?pack=1m    подборка: старт до 1 млн ₸ (almaty: Алматы и область,
//                     home: из дома)
//   /ideas/<id>       одна идея: бюджет, окупаемость, команда, шаги, риски
//   /free/map         карта 278 диагнозов по органам бизнеса
//   /free/plan10      шаблон плана на 10 дней (печать и PDF из браузера)
//
// Gallup (34 таланта глазами собственника) живёт в handlers/http
// (magnet_gallup.go, там база талантов) и берёт отсюда оболочку PublicShell.
// Страницы лёгкие: первые 48 карточек отрисованы на сервере (без скрипта
// страница тоже читается), каталог для фильтра приходит отдельно
// (/ideas.json, сжатый, около 60 КБ) уже после показа страницы.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// MagnetBot: the bot link of a magnet («Забрать в Telegram» on its page).
func MagnetBot(id string) string { return bsProfile.Bot + "?start=mg_" + id }

// PublicShell: an open page in the club's look (the header, the footer, the
// fonts) with the page's own CSS and an optional inline script.
func PublicShell(title, desc, path, css, body, script string) string {
	var b strings.Builder
	url := SiteURL + path
	name := title
	if i := strings.Index(name, " | "); i > 0 {
		name = name[:i]
	}
	ld := ldScript(obj{"@context": "https://schema.org", "@graph": []any{
		websiteNode(),
		obj{"@type": "WebPage", "@id": url + "#page", "url": url, "name": name, "description": desc, "inLanguage": "ru",
			"isPartOf": obj{"@id": SiteURL + "/#website"}, "publisher": obj{"@id": orgID(), "@type": "Organization", "name": "Business Surgery", "url": bsProfile.MainSite},
			"dateModified": siteUpdated, "isAccessibleForFree": true},
		breadcrumbs([2]string{"Business Surgery", SiteURL + "/about"}, [2]string{name, url}),
	}})
	b.WriteString(pageHead(title, desc, url, "website", ld))
	if css != "" {
		b.WriteString("<style>" + freeCSS + css + "</style>\n")
	} else {
		b.WriteString("<style>" + freeCSS + "</style>\n")
	}
	b.WriteString("<main>" + body + "</main>\n")
	if script != "" {
		b.WriteString("<script>" + script + "</script>\n")
	}
	b.WriteString(pageFoot())
	return b.String()
}

// MagnetCTA: the closing box of a magnet page: the разбор and the bot.
func MagnetCTA(mg, head, text string) string {
	return `<section class="mgx"><div class="w"><div class="box"><h2 style="font-size:26px">` + hx(head) + `</h2><p class="mute" style="margin-top:8px">` + hx(text) + `</p>` +
		`<div class="cta"><a class="b p" href="` + bsProfile.WALink + `?text=` + waText + `">Экспресс-разбор с основателями · 50&nbsp;000&nbsp;₸</a>` +
		`<a class="b" href="` + MagnetBot(mg) + `">Ещё материалы в Telegram</a></div></div></div></section>` + "\n"
}

const freeCSS = `
.fh{padding:40px 0 26px}.fh h1{font-size:46px}.fh .lead{font-size:18px}
.chips{display:flex;flex-wrap:wrap;gap:8px;margin-top:16px}
.chip{display:inline-flex;align-items:center;gap:6px;min-height:36px;padding:0 14px;border:1px solid var(--line);border-radius:10px;background:transparent;color:var(--soft);font:inherit;font-size:14px;font-weight:600;cursor:pointer;text-decoration:none}
.chip.on,.chip:hover{border-color:#fff;color:#fff}.chip.on{background:#fff;color:#050505}
.chip i{font-style:normal;font-size:12px;color:var(--mute)}.chip.on i{color:#555}
.num{display:grid;grid-template-columns:repeat(4,1fr);gap:10px;margin-top:22px}
.num div{padding:14px 16px;border:1px solid var(--line);border-radius:10px;background:var(--card)}
.num b{display:block;font-size:26px;font-weight:800;letter-spacing:-.02em}.num span{font-size:13px;color:var(--mute)}
.mgx{border-top:1px solid var(--line)}
button.b{font:inherit;font-weight:700;font-size:15px;cursor:pointer;background:transparent;color:#fff}button.b.p{background:#fff;color:#050505}
@media(max-width:820px){.fh h1{font-size:32px}.num{grid-template-columns:1fr 1fr}}
@media print{header,footer,.noprint,.mgx{display:none!important}body,html{background:#fff;color:#000}.box,.pt td,.pt th{border-color:#999!important;color:#000}}
`

// ── Бизнес-идеи ──

type ideaLite struct {
	ID string `json:"i"`
	T  string `json:"t"`
	C  string `json:"c"`
	S  string `json:"s"`
	B  int64  `json:"b"`
	B2 int64  `json:"B"`
	P  string `json:"p"`
	PM int    `json:"m"` // payback, months from (sort)
	D  int    `json:"d"`
	F  string `json:"f"` // o offline, n online, g hybrid
	G  string `json:"g"` // flags: h из дома, 1 можно одному, x без опыта, a Алматы
	Ic string `json:"e"`
}

var paybackRe = regexp.MustCompile(`(\d+)`)

func paybackMonths(p string) int {
	m := paybackRe.FindString(p)
	n, err := strconv.Atoi(m)
	if err != nil {
		return 99
	}
	if strings.Contains(p, "год") || strings.Contains(p, "лет") {
		n *= 12
	}
	return n
}

// IdeaPacks: the ready selections of the catalogue the posts point to.
var IdeaPacks = map[string]struct{ Title, Lead string }{
	"1m":     {"Бизнес-идеи со стартом до 1 млн ₸", "Идеи из каталога, где стартовый бюджет укладывается в 1 млн ₸: с окупаемостью, командой, первыми шагами и рисками."},
	"almaty": {"Бизнес-идеи для Алматы и области", "Идеи, где город и область уже в расчёте: местные клиенты, сырьё, площадки и спрос."},
	"home":   {"Бизнес-идеи, которые можно начать из дома", "Идеи без помещения на старте: производство, услуги и онлайн из своей квартиры."},
}

var (
	ideasLiteOnce sync.Once
	ideasLite     []ideaLite
	ideasFull     map[string]*content.IdeaItem
	ideasCatName  map[string]string
)

func ideaFlags(it *content.IdeaItem) string {
	g := ""
	tags := map[string]bool{}
	for _, t := range it.Tags {
		tags[strings.ToLower(t)] = true
	}
	if tags["из дома"] {
		g += "h"
	}
	if tags["можно одному"] {
		g += "1"
	}
	if tags["без опыта"] {
		g += "x"
	}
	if strings.Contains(it.Title+it.Short+it.Desc+it.Sub, "Алмат") {
		g += "a"
	}
	return g
}

func ideasData() []ideaLite {
	ideasLiteOnce.Do(func() {
		plain, _, _, err := content.Ideas()
		if err != nil {
			return
		}
		var cat struct {
			Items []content.IdeaItem `json:"items"`
		}
		_ = json.Unmarshal(plain, &cat)
		ideasFull = map[string]*content.IdeaItem{}
		ideasCatName = map[string]string{}
		for _, c := range content.IdeaCats {
			ideasCatName[c.ID] = c.Short
		}
		for i := range cat.Items {
			it := &cat.Items[i]
			ideasFull[it.ID] = it
			f := ""
			for _, x := range it.Format {
				switch x {
				case "офлайн":
					f += "o"
				case "онлайн":
					f += "n"
				case "гибрид":
					f += "g"
				}
			}
			ideasLite = append(ideasLite, ideaLite{ID: it.ID, T: it.Title, C: it.Cat, S: cut(it.Short, 150), B: it.Budget[0], B2: it.Budget[1],
				P: it.Payback, PM: paybackMonths(it.Payback), D: it.Difficulty, F: f, G: ideaFlags(it), Ic: it.Cover.Icon})
		}
	})
	return ideasLite
}

// IdeaPackCount: how many ideas a pack holds (the post texts say it).
func IdeaPackCount(pack string) int {
	n := 0
	for _, it := range ideasData() {
		if inPack(it, pack) {
			n++
		}
	}
	return n
}

func inPack(it ideaLite, pack string) bool {
	switch pack {
	case "1m":
		return it.B > 0 && it.B <= 1_000_000
	case "almaty":
		return strings.Contains(it.G, "a")
	case "home":
		return strings.Contains(it.G, "h")
	}
	return true
}

// money: 1500000 → «1,5 млн ₸», 300000 → «300 тыс ₸».
func money(v int64) string {
	switch {
	case v >= 1_000_000:
		m := float64(v) / 1_000_000
		s := strconv.FormatFloat(m, 'f', 1, 64)
		s = strings.TrimSuffix(s, ".0")
		return strings.Replace(s, ".", ",", 1) + " млн ₸"
	case v >= 1000:
		return content.Num(int(v/1000)) + " тыс ₸"
	}
	return content.Num(int(v)) + " ₸"
}

func ideaCardHTML(it ideaLite) string {
	dots := strings.Repeat("●", it.D) + strings.Repeat("○", 5-it.D)
	return `<a class="ic" href="/ideas/` + hx(it.ID) + `"><span class="ie">` + hx(it.Ic) + `</span><b>` + hx(it.T) + `</b><span class="is">` + hx(it.S) + `</span>` +
		`<span class="im"><em>от ` + money(it.B) + `</em><em>окупаемость ` + hx(it.P) + `</em><em title="Сложность">` + dots + `</em></span><span class="ik">` + hx(ideasCatName[it.C]) + `</span></a>`
}

const ideasCSS = `
.ib{position:sticky;top:0;z-index:5;background:rgba(5,5,5,.94);backdrop-filter:blur(8px);border-bottom:1px solid var(--line);padding:12px 0}
.ib .row{display:flex;flex-wrap:wrap;gap:8px;align-items:center}
.ib input,.ib select{height:40px;border-radius:10px;border:1px solid var(--line);background:var(--card);color:#fff;font:inherit;font-size:14.5px;padding:0 12px}
.ib input{flex:1;min-width:200px}
.ib .cats{display:flex;gap:6px;overflow-x:auto;margin-top:10px;padding-bottom:2px;scrollbar-width:none}.ib .cats::-webkit-scrollbar{display:none}
.ib .cats .chip{white-space:nowrap;min-height:34px;font-size:13.5px}
.ig{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:18px}
.ic{display:flex;flex-direction:column;gap:6px;padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--card);text-decoration:none;min-width:0}
.ic:hover{border-color:rgba(255,255,255,.45)}
.ic .ie{font-size:24px;line-height:1}.ic b{font-size:16px;line-height:1.3}
.ic .is{font-size:13.5px;color:var(--soft);line-height:1.45}
.ic .im{display:flex;flex-wrap:wrap;gap:6px;margin-top:auto;padding-top:6px}
.ic .im em{font-style:normal;font-size:12px;font-weight:700;padding:3px 8px;border:1px solid var(--line);border-radius:6px;color:var(--soft)}
.ic .ik{font-size:11.5px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--mute)}
.icount{font-size:14px;color:var(--mute);margin-top:14px}
.more{display:flex;justify-content:center;margin:22px 0 10px}
@media(max-width:900px){.ig{grid-template-columns:1fr 1fr}}
@media(max-width:600px){.ig{grid-template-columns:1fr}.ib input{flex:1 1 100%;min-width:0;width:100%}.ib select{flex:1 1 40%;min-width:0}}
.id .num{grid-template-columns:repeat(3,1fr)}
.id h2{font-size:24px;margin-bottom:12px}
`

// ideasJS: the filter. D is the catalogue, P the pack of the page.
const ideasJS = `(function(){var D=null,C=JSON.parse(document.getElementById('ideasCats').textContent);
var u=new URLSearchParams(location.search),S={c:u.get('cat')||'',b:+(u.get('b')||0),f:u.get('f')||'',g:u.get('g')||'',q:u.get('q')||'',s:u.get('s')||'',n:48};
var pk=document.body.getAttribute('data-pack');if(pk==='1m'&&!S.b)S.b=1000000;if(pk==='almaty'&&!S.g)S.g='a';if(pk==='home'&&!S.g)S.g='h';
var $=function(i){return document.getElementById(i)};
function esc(s){return String(s==null?'':s).replace(/[&<>"]/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]})}
function money(v){if(v>=1e6){var m=(Math.round(v/1e5)/10).toString().replace('.',',');return m+' млн ₸'}if(v>=1e3){return String(Math.round(v/1e3)).replace(/\B(?=(\d{3})+(?!\d))/g,' ')+' тыс ₸'}return v+' ₸'}
function card(x){var d='';for(var i=1;i<=5;i++)d+=i<=x.d?'●':'○';return '<a class="ic" href="/ideas/'+esc(x.i)+'"><span class="ie">'+esc(x.e)+'</span><b>'+esc(x.t)+'</b><span class="is">'+esc(x.s)+'</span><span class="im"><em>от '+money(x.b)+'</em><em>окупаемость '+esc(x.p)+'</em><em title="Сложность">'+d+'</em></span><span class="ik">'+esc((C[x.c]||{}).s||'')+'</span></a>'}
function list(){var q=S.q.trim().toLowerCase(),r=D.filter(function(x){if(S.c&&x.c!==S.c)return false;if(S.b&&x.b>S.b)return false;if(S.f&&x.f.indexOf(S.f)<0)return false;if(S.g&&x.g.indexOf(S.g)<0)return false;if(q&&(x.t+' '+x.s).toLowerCase().indexOf(q)<0)return false;return true});
if(S.s==='b')r.sort(function(a,b){return a.b-b.b});else if(S.s==='m')r.sort(function(a,b){return a.m-b.m});else if(S.s==='d')r.sort(function(a,b){return a.d-b.d});return r}
function paint(){if(!D)return;var r=list();$('ideasList').innerHTML=r.slice(0,S.n).map(card).join('')||'<p class="mute">Под такие условия идей нет. Снимите один из фильтров.</p>';
$('ideasCount').textContent='Найдено: '+String(r.length).replace(/\B(?=(\d{3})+(?!\d))/g,' ')+' из '+String(D.length).replace(/\B(?=(\d{3})+(?!\d))/g,' ');
$('ideasMore').style.display=r.length>S.n?'':'none';
[].forEach.call(document.querySelectorAll('[data-cat]'),function(b){b.classList.toggle('on',b.getAttribute('data-cat')===S.c)});
var p=new URLSearchParams();if(pk)p.set('pack',pk);if(S.c)p.set('cat',S.c);if(S.b)p.set('b',S.b);if(S.f)p.set('f',S.f);if(S.g)p.set('g',S.g);if(S.q)p.set('q',S.q);if(S.s)p.set('s',S.s);
try{history.replaceState(null,'',location.pathname+(p.toString()?'?'+p:''))}catch(e){}}
window.ideaCat=function(c){S.c=c;S.n=48;paint()};window.ideaMore=function(){S.n+=48;paint()};
['b','f','g','s'].forEach(function(k){var el=$('ideasF'+k);if(!el)return;el.value=k==='b'?String(S.b||0):String(S[k]||'');el.onchange=function(){S[k]=k==='b'?+el.value:el.value;S.n=48;paint()}});
var qi=$('ideasQ'),t;qi.value=S.q;qi.oninput=function(){clearTimeout(t);t=setTimeout(function(){S.q=qi.value;S.n=48;paint()},200)};
fetch('/ideas.json').then(function(r){return r.json()}).then(function(j){D=j;paint()}).catch(function(){})})();`

func ideasPageHTML(pack string) string {
	items := ideasData()
	title, lead := "1 059 бизнес-идей для Казахстана", "Каталог Business Surgery: по каждой идее стартовый бюджет в ₸, окупаемость, маржа, команда, первые шаги и риски. Открыт бесплатно, без регистрации."
	total := content.Num(len(items))
	title = strings.Replace(title, "1 059", total, 1)
	if p, ok := IdeaPacks[pack]; ok {
		title, lead = p.Title, p.Lead
	} else {
		pack = ""
	}
	var shown []ideaLite
	cnt := map[string]int{}
	var under1m, solo, home int
	for _, it := range items {
		if inPack(it, pack) {
			shown = append(shown, it)
			cnt[it.C]++
		}
		if it.B <= 1_000_000 {
			under1m++
		}
		if strings.Contains(it.G, "1") {
			solo++
		}
		if strings.Contains(it.G, "h") {
			home++
		}
	}
	cats := map[string]map[string]string{}
	for _, c := range content.IdeaCats {
		cats[c.ID] = map[string]string{"s": c.Short, "n": c.N}
	}
	catsJS, _ := json.Marshal(cats)
	var b strings.Builder
	b.WriteString(`<div class="hero fh"><div class="w"><span class="pill">Бесплатно · без регистрации</span><h1>` + hx(title) + `</h1><p class="lead">` + hx(lead) + `</p>`)
	if pack == "" {
		b.WriteString(`<div class="num"><div><b>` + total + `</b><span>идей в 12 направлениях</span></div><div><b>` + content.Num(under1m) + `</b><span>со стартом до 1 млн ₸</span></div><div><b>` + content.Num(solo) + `</b><span>можно начать одному</span></div><div><b>` + content.Num(home) + `</b><span>можно начать из дома</span></div></div>`)
	} else {
		b.WriteString(`<div class="num"><div><b>` + content.Num(len(shown)) + `</b><span>идей в подборке</span></div><div><b>` + total + `</b><span>во всём каталоге</span></div></div>`)
	}
	b.WriteString(`<div class="chips">`)
	b.WriteString(`<a class="chip` + map[bool]string{true: " on"}[pack == ""] + `" href="/ideas">Весь каталог</a>`)
	for _, k := range [][2]string{{"1m", "Старт до 1 млн ₸"}, {"almaty", "Алматы и область"}, {"home", "Можно из дома"}} {
		b.WriteString(`<a class="chip` + map[bool]string{true: " on"}[pack == k[0]] + `" href="/ideas?pack=` + k[0] + `">` + hx(k[1]) + `</a>`)
	}
	b.WriteString(`</div></div></div>`)
	b.WriteString(`<div class="ib noprint"><div class="w"><div class="row"><input id="ideasQ" type="search" placeholder="Поиск: кофе, дети, Kaspi, ремонт…" aria-label="Поиск идеи">` +
		`<select id="ideasFb" aria-label="Бюджет"><option value="0">Любой бюджет</option><option value="300000">до 300 тыс ₸</option><option value="1000000">до 1 млн ₸</option><option value="3000000">до 3 млн ₸</option><option value="10000000">до 10 млн ₸</option></select>` +
		`<select id="ideasFf" aria-label="Формат"><option value="">Любой формат</option><option value="o">Офлайн</option><option value="n">Онлайн</option><option value="g">Гибрид</option></select>` +
		`<select id="ideasFg" aria-label="Условия"><option value="">Любые условия</option><option value="1">Можно одному</option><option value="h">Из дома</option><option value="x">Без опыта</option><option value="a">Алматы и область</option></select>` +
		`<select id="ideasFs" aria-label="Порядок"><option value="">По направлениям</option><option value="b">Сначала дешевле</option><option value="m">Быстрее окупаются</option><option value="d">Сначала проще</option></select></div>` +
		`<div class="cats"><button class="chip on" data-cat="" onclick="ideaCat('')">Все</button>`)
	for _, c := range content.IdeaCats {
		b.WriteString(`<button class="chip" data-cat="` + c.ID + `" onclick="ideaCat('` + c.ID + `')">` + c.Icon + ` ` + hx(c.Short) + `</button>`)
	}
	b.WriteString(`</div></div></div>`)
	b.WriteString(`<section style="border-top:0;padding-top:0"><div class="w"><p class="icount" id="ideasCount">Найдено: ` + content.Num(len(shown)) + `</p><div class="ig" id="ideasList">`)
	for i, it := range shown {
		if i >= 48 {
			break
		}
		b.WriteString(ideaCardHTML(it))
	}
	b.WriteString(`</div><div class="more" id="ideasMore"><button class="b" onclick="ideaMore()">Показать ещё</button></div>` +
		`<p class="note">Цифры в карточках: ориентиры для Казахстана на 2025-2026 год. Перед запуском проверьте их на своём рынке: цены поставщиков, аренду и спрос в своём районе.</p></div></section>`)
	b.WriteString(`<script type="application/json" id="ideasCats">` + string(catsJS) + `</script>`)
	b.WriteString(MagnetCTA("ideas", "Выбрали идею? Проверим её на цифрах", "На экспресс-разборе основатели клуба за час считают вашу идею или действующий бизнес: где деньги, где риски и что делать первые 10 дней."))
	page := PublicShell(title+" | Business Surgery", lead, "/ideas"+map[bool]string{true: "?pack=" + pack}[pack != ""], ideasCSS, b.String(), ideasJS)
	return strings.Replace(page, "<body>", `<body data-pack="`+pack+`">`, 1)
}

func ideaPageHTML(id string) (string, bool) {
	ideasData()
	it := ideasFull[id]
	if it == nil {
		return "", false
	}
	var b strings.Builder
	b.WriteString(`<div class="w"><p class="crumbs"><a href="/ideas">Бизнес-идеи</a> / <a href="/ideas?cat=` + hx(it.Cat) + `">` + hx(ideasCatName[it.Cat]) + `</a></p></div>`)
	b.WriteString(`<div class="hero fh id"><div class="w"><span class="tag">` + hx(it.Sub) + `</span><h1>` + hx(it.Cover.Icon) + ` ` + hx(it.Title) + `</h1><p class="lead">` + hx(it.Short) + `</p>`)
	diff := []string{"", "очень просто", "просто", "средне", "сложно", "очень сложно"}
	d := ""
	if it.Difficulty >= 1 && it.Difficulty <= 5 {
		d = diff[it.Difficulty]
	}
	b.WriteString(`<div class="num"><div><b>` + money(it.Budget[0]) + `</b><span>старт, до ` + money(it.Budget[1]) + `</span></div><div><b>` + hx(it.Payback) + `</b><span>окупаемость</span></div><div><b>` + hx(d) + `</b><span>сложность запуска</span></div></div></div></div>`)
	sec := func(h, body string) {
		if strings.TrimSpace(body) != "" {
			b.WriteString(`<section><div class="w"><h2>` + h + `</h2>` + body + `</div></section>`)
		}
	}
	sec("Кто платит и почему", "<p>"+hx(it.Desc)+"</p>")
	var facts []string
	if it.Margin != "" {
		facts = append(facts, "Маржа: "+it.Margin)
	}
	if it.Team != "" {
		facts = append(facts, "Команда: "+it.Team)
	}
	if len(it.Format) > 0 {
		facts = append(facts, "Формат: "+strings.Join(it.Format, ", "))
	}
	sec("Цифры и люди", list("ul", facts))
	sec("Что понадобится", list("ul", it.Need))
	sec("Первые шаги", list("ol", it.FirstSteps))
	sec("Риски", list("ul", it.Risks))
	// соседние идеи того же направления
	var near strings.Builder
	n := 0
	for _, x := range ideasLite {
		if x.ID != it.ID && x.C == it.Cat && n < 6 {
			near.WriteString(ideaCardHTML(x))
			n++
		}
	}
	if n > 0 {
		b.WriteString(`<section><div class="w"><h2>Ещё в направлении «` + hx(ideasCatName[it.Cat]) + `»</h2><div class="ig">` + near.String() + `</div><div class="cta"><a class="b" href="/ideas">Весь каталог идей</a></div></div></section>`)
	}
	b.WriteString(MagnetCTA("ideas", "Разобрать эту идею на ваших цифрах", "Экспресс-разбор: час с основателями клуба. Считаем бюджет, спрос и первые 10 дней запуска под ваш город и ваши деньги."))
	return PublicShell(it.Title+": бизнес-идея с расчётом | Business Surgery", cut(it.Short+" Старт от "+money(it.Budget[0])+", окупаемость "+it.Payback+".", 280), "/ideas/"+it.ID, ideasCSS, b.String(), ""), true
}

// ── Карта диагнозов ──

const mapCSS = `
.om{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin-top:14px}
.om a{display:block;padding:12px 14px;border:1px solid var(--line);border-radius:10px;background:var(--card);text-decoration:none}
.om a:hover{border-color:rgba(255,255,255,.45)}
.om b{display:block;font-size:15px;line-height:1.35}.om span{display:block;font-size:13px;color:var(--mute);line-height:1.45;margin-top:3px}
.oh{display:flex;align-items:baseline;justify-content:space-between;gap:10px}.oh em{font-style:normal;color:var(--mute);font-size:14px}
@media(max-width:700px){.om{grid-template-columns:1fr}}
`

func mapPageHTML() string {
	l := publicLib()
	nd := 0
	for _, it := range l.items {
		if it.Kind == "diag" {
			nd++
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="hero fh"><div class="w"><span class="pill">Бесплатно · без регистрации</span><h1>Карта диагнозов бизнеса <span>` + content.Num(nd) + ` проблем на одной странице</span></h1>` +
		`<p class="lead">По этой карте клуб ведёт разборы. Пройдите по органам бизнеса и отметьте, что узнали у себя. Каждая карточка открывает признаки, причины, цену проблемы в ₸, самопроверку и первые шаги.</p><div class="chips noprint">`)
	for _, o := range l.organs {
		k := 0
		for _, i := range l.byOrgan[o] {
			if l.items[i].Kind == "diag" {
				k++
			}
		}
		if k > 0 {
			b.WriteString(`<a class="chip" href="#` + slugify(o) + `">` + hx(o) + ` <i>` + strconv.Itoa(k) + `</i></a>`)
		}
	}
	b.WriteString(`</div></div></div>`)
	for _, o := range l.organs {
		var li strings.Builder
		k := 0
		for _, i := range l.byOrgan[o] {
			it := l.items[i]
			if it.Kind != "diag" {
				continue
			}
			k++
			li.WriteString(`<a href="/library/` + it.Slug + `"><b>` + hx(it.Title) + `</b><span>` + hx(cut(it.Subtitle, 90)) + `</span></a>`)
		}
		if k == 0 {
			continue
		}
		b.WriteString(`<section id="` + slugify(o) + `"><div class="w"><div class="oh"><h2>` + hx(o) + `</h2><em>` + strconv.Itoa(k) + ` ` + content.Plural(k, "диагноз", "диагноза", "диагнозов") + `</em></div><div class="om">` + li.String() + `</div></div></section>`)
	}
	b.WriteString(MagnetCTA("map", "Узнали у себя 3 и больше?", "Значит, болезни связаны между собой. На экспресс-разборе основатели клуба находят ту, что тянет остальные, и составляют план на 10 дней."))
	return PublicShell("Карта диагнозов бизнеса: "+content.Num(nd)+" проблем собственника | Business Surgery",
		"Карта диагнозов Business Surgery: проблемы малого бизнеса по органам (стратегия, маркетинг, продажи, команда, финансы, процессы) с признаками, причинами и первыми шагами.", "/free/map", mapCSS, b.String(), "")
}

// ── Шаблон плана на 10 дней ──

const planCSS = `
.pt{width:100%;border-collapse:collapse;margin-top:12px;font-size:14.5px}
.pt th,.pt td{border:1px solid var(--line);padding:10px 12px;text-align:left;vertical-align:top}
.pt th{font-size:12.5px;font-weight:700;letter-spacing:.04em;text-transform:uppercase;color:var(--mute)}
.pt td.e{height:38px}
.ex{color:var(--soft)}.ex td{color:var(--soft)}
.steps10{counter-reset:s;list-style:none;display:grid;gap:10px;margin-top:10px}
.steps10 li{counter-increment:s;position:relative;padding:14px 16px 14px 56px;border:1px solid var(--line);border-radius:10px;background:var(--card)}
.steps10 li::before{content:"0" counter(s);position:absolute;left:14px;top:12px;width:30px;height:30px;border:2px solid #fff;display:flex;align-items:center;justify-content:center;font-weight:700;font-size:13px}
.tw{overflow-x:auto}
@media print{.pt th,.pt td{color:#000}.steps10 li{background:#fff}.steps10 li::before{border-color:#000}}
`

func plan10PageHTML() string {
	var b strings.Builder
	b.WriteString(`<div class="hero fh"><div class="w"><span class="pill">Шаблон · бесплатно</span><h1>План на 10 дней <span>шаблон, по которому работают резиденты клуба</span></h1>` +
		`<p class="lead">Год слишком длинный срок, чтобы вовремя заметить ошибку. Цикл в 10 дней двигает одну цифру бизнеса: одна цель, 2-3 задачи, ежедневная отметка и разбор в конце.</p>` +
		`<div class="cta noprint"><button class="b p" onclick="window.print()">Распечатать или сохранить в PDF</button><a class="b" href="#example">Посмотреть пример</a></div></div></div>`)
	b.WriteString(`<section><div class="w"><h2>Как заполнять</h2><ol class="steps10">` +
		`<li><b>Одна цифра.</b> Выберите показатель, который важнее всего прямо сейчас: выручка недели, средний чек, конверсия заявок, остаток денег. Запишите, сколько сейчас и сколько должно стать через 10 дней.</li>` +
		`<li><b>2-3 задачи, которые двигают цифру.</b> Каждая с одним ответственным, сроком и проверкой: по какому факту станет видно, что задача сделана.</li>` +
		`<li><b>Отметка каждый день.</b> Две минуты вечером: что сделано и какая сейчас цифра. Пропуск дня тоже отметка.</li>` +
		`<li><b>Разбор на 10-й день.</b> Что сработало, что нет и почему. Одно решение на следующий цикл: продолжить, усилить или заменить задачу.</li></ol></div></section>`)
	b.WriteString(`<section><div class="w"><h2>Шаблон</h2><div class="box"><div class="tw"><table class="pt"><tr><th style="width:34%">Цель цикла</th><th>Сейчас</th><th>Через 10 дней</th><th>Даты цикла</th></tr><tr><td class="e"></td><td class="e"></td><td class="e"></td><td class="e">с ___ по ___</td></tr></table>` +
		`<table class="pt"><tr><th style="width:4%">№</th><th style="width:40%">Задача</th><th>Ответственный</th><th>Срок</th><th>Как проверить</th></tr>`)
	for i := 1; i <= 3; i++ {
		b.WriteString(`<tr><td>` + strconv.Itoa(i) + `</td><td class="e"></td><td class="e"></td><td class="e"></td><td class="e"></td></tr>`)
	}
	b.WriteString(`</table><table class="pt"><tr><th style="width:10%">День</th><th style="width:55%">Что сделано</th><th>Цифра на вечер</th></tr>`)
	for i := 1; i <= 10; i++ {
		b.WriteString(`<tr><td>` + strconv.Itoa(i) + `</td><td class="e"></td><td class="e"></td></tr>`)
	}
	b.WriteString(`</table><table class="pt"><tr><th>Разбор 10-го дня</th></tr><tr><td class="e">Что сработало:</td></tr><tr><td class="e">Что не сработало и почему:</td></tr><tr><td class="e">Решение на следующий цикл:</td></tr></table></div></div></div></section>`)
	b.WriteString(`<section id="example"><div class="w"><h2>Пример: кофейня у дома</h2><p class="mute">Цифры условные, чтобы показать логику заполнения.</p><div class="tw"><table class="pt ex">` +
		`<tr><th style="width:34%">Цель цикла</th><th>Сейчас</th><th>Через 10 дней</th></tr><tr><td>Средний чек</td><td>1 200 ₸</td><td>1 350 ₸</td></tr></table>` +
		`<table class="pt ex"><tr><th>№</th><th>Задача</th><th>Ответственный</th><th>Срок</th><th>Как проверить</th></tr>` +
		`<tr><td>1</td><td>Бариста предлагает десерт к каждому кофе одной фразой</td><td>Старший бариста</td><td>День 2</td><td>Доля чеков с десертом в кассе</td></tr>` +
		`<tr><td>2</td><td>Комбо «кофе и выпечка» на доске у кассы</td><td>Собственник</td><td>День 3</td><td>Продажи комбо в день</td></tr>` +
		`<tr><td>3</td><td>Объём 400 мл в меню первым</td><td>Управляющий</td><td>День 1</td><td>Доля больших стаканов</td></tr></table>` +
		`<table class="pt ex"><tr><th>Разбор 10-го дня</th></tr><tr><td>Чек 1 310 ₸: десерт сработал, комбо почти не берут. Следующий цикл: заменить комбо на десерт дня со скидкой к кофе.</td></tr></table></div></div></section>`)
	b.WriteString(MagnetCTA("plan10", "Хотите план на 10 дней под ваш бизнес?", "На экспресс-разборе основатели клуба за час находят главную цифру вашего бизнеса и вместе с вами заполняют первый цикл."))
	return PublicShell("Шаблон плана на 10 дней для собственника | Business Surgery",
		"Бесплатный шаблон плана на 10 дней Business Surgery: одна цель с цифрой, 2-3 задачи, ежедневная отметка и разбор. С примером заполнения.", "/free/plan10", planCSS, b.String(), "")
}

var (
	extraMu   sync.Mutex
	extraFree = map[string]func() string{}
)

// AddFreePage: a magnet page made outside this package (handlers/http
// registers /free/gallup in its init: the talents' base lives there).
func AddFreePage(path string, make func() string) {
	extraMu.Lock()
	extraFree[path] = make
	extraMu.Unlock()
}

// freePaths: the magnet pages besides /ideas (the sitemap).
func freePaths() []string {
	out := []string{"/free/map", "/free/plan10"}
	extraMu.Lock()
	for p := range extraFree {
		out = append(out, p)
	}
	extraMu.Unlock()
	sort.Strings(out)
	return out
}

// registerFree mounts the magnet pages (called from RegisterPublic).
func registerFree(r *gin.Engine) {
	html := "text/html; charset=utf-8"
	h := map[string]gin.HandlerFunc{
		"/ideas": func(c *gin.Context) {
			pack := c.Query("pack")
			if _, ok := IdeaPacks[pack]; !ok {
				pack = ""
			}
			servePublic(c, html, cachedPage("ideas/"+pack, func() string { return ideasPageHTML(pack) }))
		},
		"/ideas/:id": func(c *gin.Context) {
			id := c.Param("id")
			ideasData()
			if ideasFull[id] == nil {
				c.Header("X-Robots-Tag", "noindex")
				c.Data(http.StatusNotFound, html, []byte(notFoundHTML()))
				return
			}
			servePublic(c, html, cachedPage("idea/"+id, func() string { s, _ := ideaPageHTML(id); return s }))
		},
		"/ideas.json": func(c *gin.Context) {
			servePublic(c, "application/json; charset=utf-8", cachedPage("ideas.json", func() string { b, _ := json.Marshal(ideasData()); return string(b) }))
		},
		"/free/map":    func(c *gin.Context) { servePublic(c, html, cachedPage("free/map", mapPageHTML)) },
		"/free/plan10": func(c *gin.Context) { servePublic(c, html, cachedPage("free/plan10", plan10PageHTML)) },
	}
	extraMu.Lock()
	for p, mk := range extraFree {
		key, mk := "free"+p, mk
		h[p] = func(c *gin.Context) { servePublic(c, html, cachedPage(key, mk)) }
	}
	extraMu.Unlock()
	for p, fn := range h {
		r.GET(p, fn)
		r.HEAD(p, fn)
	}
}

// IdeaIDs: every idea id (the sitemap).
func ideaIDs() []string {
	var out []string
	for _, it := range ideasData() {
		out = append(out, it.ID)
	}
	sort.Strings(out)
	return out
}
