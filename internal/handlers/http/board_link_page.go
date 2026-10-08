package http

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bnursik/business_surgery_backend/internal/bot"

	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/web"
)

// R57: what the client sees of the board. Only these fields leave the
// server: notes, fines, stickers, history, tests, health, authors and
// everything else of the board stay inside.

type cbItem struct {
	Title   string   `json:"title"`
	Desc    string   `json:"desc,omitempty"`
	Organ   string   `json:"organ,omitempty"`
	Effects []string `json:"effects,omitempty"`
}

type cbTask struct {
	Title string `json:"title"`
	Due   string `json:"due,omitempty"`
	Done  bool   `json:"done,omitempty"`
}

type clientBoard struct {
	Name      string   `json:"name"`
	Updated   string   `json:"updated,omitempty"`
	PointA    string   `json:"pointA,omitempty"`
	PointB    string   `json:"pointB,omitempty"`
	Goals     []cbItem `json:"goals"`
	Strategy  string   `json:"strategy,omitempty"`
	Diagnoses []cbItem `json:"diagnoses"`
	Causes    []cbItem `json:"causes"`
	Tasks     []cbTask `json:"tasks"`
	Tools     []cbItem `json:"tools"`
}

func clientBoardOf(b *pg.PlatformBoard) clientBoard {
	var d struct {
		Name    string            `json:"name"`
		Updated string            `json:"updated"`
		Date    string            `json:"date"`
		Info    map[string]string `json:"info"`
		Nodes   []struct {
			Type  string `json:"type"`
			Role  string `json:"role"`
			Title string `json:"title"`
			Desc  string `json:"desc"`
			Organ string `json:"organ"`
			Task  *struct {
				Completed bool   `json:"completed"`
				Date      string `json:"date"`
			} `json:"task"`
			Done bool `json:"done"`
			DNA  *struct {
				Effects []string `json:"effects"`
			} `json:"dna"`
		} `json:"nodes"`
	}
	_ = json.Unmarshal(b.Data, &d)
	cl := func(s string, n int) string { return cbNbsp(clip(noLongDash(clean(s)), n)) }
	out := clientBoard{Goals: []cbItem{}, Diagnoses: []cbItem{}, Causes: []cbItem{}, Tasks: []cbTask{}, Tools: []cbItem{}}
	out.Name = cl(d.Info["name"], 80)
	if out.Name == "" {
		out.Name = cl(b.Resident, 80)
	}
	out.PointA, out.PointB = cl(d.Info["a"], 300), cl(d.Info["b"], 300)
	upd := b.UpdatedAt
	if !upd.IsZero() {
		out.Updated = upd.In(almaty).Format("02.01.2006")
	}
	join := func(t, s string) string {
		switch {
		case t != "" && s != "":
			return t + ". " + s
		case t != "":
			return t
		}
		return s
	}
	for _, n := range d.Nodes {
		t, ds := cl(n.Title, 200), cl(n.Desc, 600)
		switch n.Type {
		case "diag":
			if t != "" && len(out.Diagnoses) < 12 {
				out.Diagnoses = append(out.Diagnoses, cbItem{Title: t, Desc: ds, Organ: cl(n.Organ, 40)})
			}
		case "dna":
			if t != "" && len(out.Causes) < 12 {
				it := cbItem{Title: t, Desc: ds}
				if n.DNA != nil {
					for _, e := range n.DNA.Effects {
						if e = cl(e, 200); e != "" && len(it.Effects) < 6 {
							it.Effects = append(it.Effects, e)
						}
					}
				}
				out.Causes = append(out.Causes, it)
			}
		case "goal":
			if t != "" && len(out.Goals) < 6 {
				out.Goals = append(out.Goals, cbItem{Title: t, Desc: ds})
			}
		case "tool":
			if t != "" && len(out.Tools) < 16 {
				out.Tools = append(out.Tools, cbItem{Title: t, Desc: ds})
			}
		case "task":
			if t != "" && len(out.Tasks) < 30 {
				tk := cbTask{Title: t, Done: n.Done || (n.Task != nil && n.Task.Completed)}
				if n.Task != nil {
					if dt, err := time.Parse("2006-01-02", strings.TrimSpace(n.Task.Date)); err == nil {
						tk.Due = dt.Format("02.01")
					}
				}
				out.Tasks = append(out.Tasks, tk)
			}
		case "strat":
			if n.Role != "exp" && out.Strategy == "" {
				out.Strategy = join(t, ds)
			}
		case "point":
			if n.Role == "pointA" && out.PointA == "" {
				out.PointA = join(t, ds)
			}
			if n.Role == "pointB" && out.PointB == "" {
				out.PointB = join(t, ds)
			}
		}
	}
	return out
}

var cbNumRe = regexp.MustCompile(`(\d) (\d{3})`)

// cbNbsp keeps «1 200 000 ₸» on one line.
func cbNbsp(s string) string {
	for i := 0; i < 4; i++ {
		s = cbNumRe.ReplaceAllString(s, "$1\u00a0$2")
	}
	return strings.ReplaceAll(s, " ₸", "\u00a0₸")
}

// ── the page ──

type cbPrice struct {
	Label, Sum, Note string
}

type cbCase struct {
	Title, Who string
	Metrics    []string
}

type cbPage struct {
	Nonce, Fonts, Token string
	B                   clientBoard
	First               string
	ClubLines           []string
	Prices              []cbPrice
	Bonus               string
	Kaspi               string
	WALink, WAPhone, TG string
	Cases               []cbCase
	Year                int
}

func cspNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

func setPageCSP(c *gin.Context, nonce string) {
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; upgrade-insecure-requests")
}

// Page: GET /b/:token.
func (h *BoardLinks) Page(c *gin.Context) {
	ctx := c.Request.Context()
	nonce := cspNonce()
	setPageCSP(c, nonce)
	l, b, why := h.live(ctx, c.Param("token"))
	if l == nil {
		var buf bytes.Buffer
		msg := "Ссылка не найдена. Проверьте, что она скопирована целиком, или попросите новую."
		switch why {
		case "revoked":
			msg = "Эта ссылка больше не действует. Напишите нам, и мы пришлём новую."
		case "expired":
			msg = "Срок ссылки закончился. Напишите нам, и мы пришлём новую."
		}
		wp, wl, bot := web.Contacts()
		_ = cbGoneTpl.Execute(&buf, map[string]string{"Fonts": web.FontHead(), "Msg": msg, "WA": wl, "WAPhone": wp, "TG": bot})
		c.Data(http.StatusNotFound, "text/html; charset=utf-8", buf.Bytes())
		return
	}
	cb := clientBoardOf(b)
	p := cbPage{Nonce: nonce, Fonts: web.FontHead(), Token: l.ID, B: cb, First: firstName(cb.Name), Year: h.now().In(almaty).Year()}
	var plans []diagPlan
	for i, d := range cb.Diagnoses {
		if i >= 3 {
			break
		}
		plans = append(plans, planForDiag(d.Title, d.Desc))
	}
	for _, ln := range strings.Split(clubForDiagnoses(plans), "\n") {
		if ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "•")); ln != "" {
			p.ClubLines = append(p.ClubLines, ln)
		}
	}
	cfg := defaultSalesCfg()
	if h.S != nil {
		cfg = h.S.Settings(ctx)
		for i, x := range h.S.published(ctx) {
			if i >= 3 {
				break
			}
			sc := x["case"].(salesCase)
			v := cbCase{Title: sc.Title, Who: sc.who("anon")}
			for _, m := range sc.Metrics {
				v.Metrics = append(v.Metrics, metricLine(m))
			}
			p.Cases = append(p.Cases, v)
		}
	}
	p.Prices = append(p.Prices, cbPrice{"12 месяцев", tenge(cfg.YearPrice), "полный год: разбор каждые 10 дней"})
	if cfg.Q3Price > 0 {
		p.Prices = append(p.Prices, cbPrice{"3 месяца", tenge(cfg.Q3Price), "чтобы начать: разбор каждые 10 дней"})
	}
	if cfg.BonusText != "" {
		p.Bonus = cfg.BonusText
		if cfg.BonusDays > 0 {
			p.Bonus += " (до " + l.CreatedAt.AddDate(0, 0, cfg.BonusDays).In(almaty).Format("02.01") + ")"
		}
	}
	p.Kaspi = cfg.KaspiClub
	if p.Kaspi == "" && cfg.KaspiSeeded == "" { // not seeded yet (the first minute after a start)
		p.Kaspi = bot.KaspiLink
	}
	wp, wl, bot := web.Contacts()
	hello := "Здравствуйте! Посмотрел свою доску после разбора"
	if p.First != "" {
		hello = "Здравствуйте! Это " + p.First + ", посмотрел свою доску после разбора"
	}
	p.WAPhone, p.WALink, p.TG = wp, wl+"?text="+url.QueryEscape(hello+". Хочу обсудить клуб."), bot
	var buf bytes.Buffer
	if err := cbPageTpl.Execute(&buf, p); err != nil {
		c.String(http.StatusInternalServerError, "Ошибка страницы")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}

var cbFuncs = template.FuncMap{
	"safe": func(s string) template.HTML { return template.HTML(s) },
	"inc":  func(i int) int { return i + 1 },
}

const cbCSS = `
:root{--bg:#0A0A0A;--card:#131313;--card2:#181818;--line:#262626;--tx:#F5F5F5;--mut:#9A9A9A;--dim:#6B6B6B;--red:#FF4D4D;--redbg:rgba(255,77,77,.08);--redln:rgba(255,77,77,.35)}
*{box-sizing:border-box;margin:0;padding:0}
html{-webkit-text-size-adjust:100%;scroll-behavior:smooth}
body{background:var(--bg);color:var(--tx);font-family:Manrope,system-ui,-apple-system,Segoe UI,Roboto,sans-serif;font-size:16px;line-height:1.55;-webkit-font-smoothing:antialiased}
a{color:inherit}
.w{max-width:1160px;margin:0 auto;padding:0 20px}
.top{display:flex;align-items:center;justify-content:space-between;padding:18px 0;border-bottom:1px solid var(--line)}
.top img{display:block;height:30px;width:auto}
.top .tag{font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--mut)}
.hero{padding:44px 0 28px}
.eyebrow{font-size:12px;letter-spacing:.16em;text-transform:uppercase;color:var(--mut);margin-bottom:14px}
h1{font-size:clamp(30px,6vw,52px);line-height:1.08;font-weight:800;letter-spacing:-.02em}
h1 span{color:var(--mut);font-weight:600}
.lead{margin-top:16px;color:#CFCFCF;max-width:640px;font-size:17px}
.ab{display:grid;grid-template-columns:1fr auto 1fr;gap:14px;align-items:stretch;margin-top:30px}
.pt{background:var(--card);border:1px solid var(--line);border-radius:18px;padding:18px 20px}
.pt b{display:block;font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--mut);font-weight:700;margin-bottom:8px}
.pt p{font-size:17px;font-weight:600}
.pt.b{background:#F5F5F5;color:#0A0A0A;border-color:#F5F5F5}.pt.b b{color:#555}
.arr{display:flex;align-items:center;color:var(--dim);font-size:22px}
.grid{display:grid;grid-template-columns:minmax(0,1fr) 360px;gap:36px;align-items:start;padding-bottom:60px}
section.s{padding:30px 0;border-top:1px solid var(--line)}
section.s:first-child{border-top:0;padding-top:8px}
h2{font-size:24px;font-weight:800;letter-spacing:-.01em;display:flex;align-items:baseline;gap:12px;margin-bottom:6px}
h2 .n{font-size:13px;color:var(--dim);font-weight:700}
.sub{color:var(--mut);font-size:15px;margin-bottom:18px}
.cards{display:grid;gap:12px}
.c{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:16px 18px}
.c h3{font-size:17px;font-weight:700;line-height:1.35}
.c p{color:#BDBDBD;font-size:15px;margin-top:6px}
.c.dx{background:var(--redbg);border-color:var(--redln)}
.c.dx h3{color:#FFE9E9}
.c.dx .og{color:var(--red)}
.og{display:inline-block;font-size:11px;letter-spacing:.12em;text-transform:uppercase;font-weight:800;color:var(--mut);margin-bottom:6px}
.fx{list-style:none;margin-top:10px;display:grid;gap:6px}
.fx li{font-size:14px;color:#BDBDBD;padding-left:16px;position:relative}
.fx li:before{content:"";position:absolute;left:0;top:9px;width:6px;height:6px;border-radius:50%;background:var(--dim)}
.strat{font-size:19px;font-weight:600;line-height:1.45}
.tasks{list-style:none;display:grid;gap:8px}
.tasks li{display:flex;gap:14px;align-items:flex-start;background:var(--card);border:1px solid var(--line);border-radius:14px;padding:14px 16px}
.ck{flex:0 0 22px;height:22px;border-radius:7px;border:1.5px solid #4A4A4A;display:flex;align-items:center;justify-content:center;font-size:13px;margin-top:1px}
.tasks li.done .ck{background:#F5F5F5;border-color:#F5F5F5;color:#0A0A0A}
.tasks li.done .tt{color:var(--mut);text-decoration:line-through}
.tt{flex:1;font-weight:600}
.due{font-size:13px;color:var(--mut);white-space:nowrap;margin-top:2px}
.tools{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:10px}
.tools .c h3{font-size:15px}
.empty{color:var(--dim);font-size:15px}
aside.side{position:sticky;top:20px}
.offer{background:#F5F5F5;color:#0A0A0A;border-radius:22px;padding:24px}
.offer .k{font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:#666;font-weight:800}
.offer h3{font-size:22px;line-height:1.2;font-weight:800;margin:10px 0 8px;letter-spacing:-.01em}
.offer p{color:#3A3A3A;font-size:15px}
.pr{display:grid;gap:8px;margin:16px 0}
.pr div{display:flex;justify-content:space-between;align-items:baseline;border-top:1px solid #DDD;padding-top:8px}
.pr span{color:#555;font-size:14px}.pr b{font-size:17px;font-weight:800;white-space:nowrap}
.bonus{background:#0A0A0A;color:#F5F5F5;border-radius:12px;padding:10px 12px;font-size:14px;font-weight:600;margin:4px 0 14px}
.btn{display:flex;align-items:center;justify-content:center;gap:8px;width:100%;border:0;border-radius:14px;padding:15px 18px;font:inherit;font-weight:800;font-size:16px;cursor:pointer;text-decoration:none;transition:transform .12s,opacity .12s}
.btn:active{transform:scale(.98)}
.btn.pri{background:#0A0A0A;color:#fff}
.btn.sec{background:transparent;color:#0A0A0A;border:1.5px solid #0A0A0A}
.btn.ghost{background:transparent;color:#666;font-weight:700;padding:10px}
.dark .btn.pri{background:#F5F5F5;color:#0A0A0A}
.dark .btn.sec{color:#F5F5F5;border-color:#3A3A3A}
.dark .btn.ghost{color:var(--mut)}
.dark .pay .btn.pri{background:#0A0A0A;color:#fff}
.dark .pay .btn.sec{color:#0A0A0A;border-color:#0A0A0A}
.bt{display:grid;gap:8px;margin-top:6px}
.club{background:var(--card);border:1px solid var(--line);border-radius:24px;padding:28px;margin-top:10px}
.club h2{font-size:28px;line-height:1.15;display:block}
.club .lead{margin-top:10px}
.why{list-style:none;display:grid;gap:10px;margin:22px 0}
.why li{background:var(--card2);border:1px solid var(--line);border-radius:14px;padding:14px 16px;font-size:15px}
.steps{display:grid;grid-template-columns:repeat(4,1fr);gap:10px;margin:18px 0 6px}
.steps div{background:var(--card2);border:1px solid var(--line);border-radius:14px;padding:14px}
.steps b{display:block;font-size:26px;font-weight:800;line-height:1}
.steps span{display:block;color:#BDBDBD;font-size:14px;margin-top:8px}
.cases{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:10px;margin-top:10px}
.cases .c h3{font-size:15px}.cases .who{font-size:12px;color:var(--mut);text-transform:uppercase;letter-spacing:.1em;font-weight:700;margin-bottom:6px}
.cases ul{list-style:none;margin-top:8px}.cases li{font-size:14px;color:#DADADA;padding:3px 0}
.fin{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:22px}
.fin .pcard{background:var(--card2);border:1px solid var(--line);border-radius:16px;padding:16px}
.pcard span{color:var(--mut);font-size:13px;font-weight:700;text-transform:uppercase;letter-spacing:.1em}
.pcard b{display:block;font-size:26px;font-weight:800;margin:6px 0 2px;white-space:nowrap}
.pcard p{color:var(--mut);font-size:14px}
.cta{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-top:18px}
.cta .ghost{grid-column:1/-1}
.pay{display:none;margin-top:18px;background:#F5F5F5;color:#0A0A0A;border-radius:18px;padding:20px}
.pay.on{display:block;animation:up .3s ease}
.pay h3{font-size:19px;font-weight:800}
.pay p{color:#3A3A3A;font-size:15px;margin-top:6px}
.pay .amt{display:grid;gap:6px;margin:14px 0}
.pay .amt div{display:flex;justify-content:space-between;background:#fff;border-radius:12px;padding:10px 12px;font-size:15px}
.pay .amt b{font-weight:800}
.pay .row{display:grid;grid-template-columns:1fr 1fr;gap:8px;margin-top:8px}
.note{display:none;margin-top:14px;color:#CFCFCF;font-size:15px;background:var(--card2);border:1px solid var(--line);border-radius:14px;padding:14px 16px}
.note.on{display:block}
.contact{display:none;margin-top:12px}.contact.on{display:grid;grid-template-columns:1fr 1fr;gap:8px}
footer{border-top:1px solid var(--line);padding:26px 0 120px;color:var(--dim);font-size:13px}
.sticky{position:fixed;left:0;right:0;bottom:0;z-index:20;padding:10px 14px calc(10px + env(safe-area-inset-bottom));background:rgba(10,10,10,.92);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border-top:1px solid var(--line);display:none;transform:translateY(110%);transition:transform .25s}
.sticky.on{transform:none}
.sticky .in{display:grid;grid-template-columns:1fr auto;gap:8px;max-width:640px;margin:0 auto;align-items:center}
.sticky .t{font-size:13px;color:var(--mut);line-height:1.3}.sticky .t b{color:#fff;display:block;font-size:14px}
.sticky .btn{padding:12px 16px;font-size:15px;width:auto}
.toast{position:fixed;left:50%;bottom:90px;transform:translateX(-50%);background:#F5F5F5;color:#0A0A0A;padding:10px 16px;border-radius:12px;font-weight:700;font-size:14px;opacity:0;transition:opacity .2s;pointer-events:none;z-index:30}
.toast.on{opacity:1}
@keyframes up{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
@media (max-width:980px){.grid{grid-template-columns:1fr}aside.side{display:none}.sticky{display:block}.steps{grid-template-columns:1fr 1fr}}
@media (max-width:560px){.w{padding:0 16px}.hero{padding:30px 0 18px}.ab{grid-template-columns:1fr}.arr{justify-content:center;transform:rotate(90deg);height:18px}
 h2{font-size:21px}.club{padding:20px;border-radius:20px}.club h2{font-size:23px}.fin,.cta,.pay .row,.contact.on{grid-template-columns:1fr}.tasks li{padding:13px 14px}.lead{font-size:16px}}
`

var cbPageTpl = template.Must(template.New("cb").Funcs(cbFuncs).Parse(`<!doctype html>
<html lang="ru"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="robots" content="noindex,nofollow,noarchive">
<meta name="referrer" content="no-referrer">
<meta name="theme-color" content="#0A0A0A">
<title>Ваш разбор · Business Surgery</title>
{{safe .Fonts}}<style>` + cbCSS + `</style>
</head><body>
<div class="w"><header class="top"><img src="/site/logo-white.png" alt="Business Surgery" width="63" height="30"><span class="tag">Ваш разбор{{if .B.Updated}} · {{.B.Updated}}{{end}}</span></header></div>
<div class="w">
<div class="hero" data-sec="top">
  <div class="eyebrow">Business Surgery · доска разбора</div>
  <h1>{{if .B.Name}}{{.B.Name}}<br><span>ваш разбор и план на 10 дней</span>{{else}}Ваш разбор<br><span>и план на 10 дней</span>{{end}}</h1>
  <p class="lead">Здесь всё, что мы нашли на разборе: диагнозы, причины, стратегия и конкретные задачи на ближайшие 10 дней.</p>
  {{if or .B.PointA .B.PointB}}<div class="ab">
    <div class="pt"><b>Точка А · сейчас</b><p>{{if .B.PointA}}{{.B.PointA}}{{else}}·{{end}}</p></div>
    <div class="arr">→</div>
    <div class="pt b"><b>Точка Б · цель</b><p>{{if .B.PointB}}{{.B.PointB}}{{else}}·{{end}}</p></div>
  </div>{{end}}
</div>
<div class="grid">
<main>
  <section class="s" data-sec="diag"><h2>Диагнозы <span class="n">{{len .B.Diagnoses}}</span></h2><p class="sub">Что сейчас мешает бизнесу расти</p>
    {{if .B.Diagnoses}}<div class="cards">{{range .B.Diagnoses}}<div class="c dx">{{if .Organ}}<span class="og">{{.Organ}}</span>{{end}}<h3>{{.Title}}</h3>{{if .Desc}}<p>{{.Desc}}</p>{{end}}</div>{{end}}</div>{{else}}<p class="empty">Диагнозы появятся здесь после разбора.</p>{{end}}
  </section>
  {{if .B.Causes}}<section class="s" data-sec="cause"><h2>Причины <span class="n">{{len .B.Causes}}</span></h2><p class="sub">Корень проблем: то, что даёт симптомы</p>
    <div class="cards">{{range .B.Causes}}<div class="c"><span class="og">ДНК причина</span><h3>{{.Title}}</h3>{{if .Desc}}<p>{{.Desc}}</p>{{end}}{{if .Effects}}<ul class="fx">{{range .Effects}}<li>{{.}}</li>{{end}}</ul>{{end}}</div>{{end}}</div>
  </section>{{end}}
  {{if or .B.Strategy .B.Goals}}<section class="s" data-sec="strat"><h2>Стратегия и цель</h2><p class="sub">Куда идём и за счёт чего</p>
    {{if .B.Strategy}}<p class="strat">{{.B.Strategy}}</p>{{end}}
    {{if .B.Goals}}<div class="cards" style="margin-top:14px">{{range .B.Goals}}<div class="c"><span class="og">Цель</span><h3>{{.Title}}</h3>{{if .Desc}}<p>{{.Desc}}</p>{{end}}</div>{{end}}</div>{{end}}
  </section>{{end}}
  <section class="s" data-sec="plan"><h2>План на 10 дней <span class="n">{{len .B.Tasks}}</span></h2><p class="sub">Конкретные шаги со сроками</p>
    {{if .B.Tasks}}<ul class="tasks">{{range .B.Tasks}}<li{{if .Done}} class="done"{{end}}><span class="ck">{{if .Done}}✓{{end}}</span><span class="tt">{{.Title}}</span>{{if .Due}}<span class="due">до {{.Due}}</span>{{end}}</li>{{end}}</ul>{{else}}<p class="empty">Задачи появятся здесь, когда план будет готов.</p>{{end}}
  </section>
  {{if .B.Tools}}<section class="s" data-sec="tools"><h2>Инструменты <span class="n">{{len .B.Tools}}</span></h2><p class="sub">Чем закрываем диагнозы</p>
    <div class="tools">{{range .B.Tools}}<div class="c"><h3>{{.Title}}</h3>{{if .Desc}}<p>{{.Desc}}</p>{{end}}</div>{{end}}</div>
  </section>{{end}}

  <section class="club dark" id="club" data-sec="club">
    <div class="eyebrow">Следующий шаг</div>
    <h2>{{if .First}}{{.First}}, ваш{{else}}Ваш{{end}} план на 10 дней готов. Дальше всё решает выполнение.</h2>
    <p class="lead">Знать диагноз полезно. Результат в цифрах появляется, когда план выполняется день за днём и кто-то держит вас в ритме. Для этого и существует клуб.</p>
    {{if .ClubLines}}<ul class="why">{{range .ClubLines}}<li>{{.}}</li>{{end}}</ul>{{end}}
    <div class="steps">
      <div><b>10</b><span>дней один цикл: разбор, план, отчёты, новый разбор</span></div>
      <div><b>2</b><span>основателя на каждом разборе: Рустам и Береке</span></div>
      <div><b>1</b><span>отчёт в день: трекер видит, что сделано</span></div>
      <div><b>5</b><span>собственников в группе: опыт и честная обратная связь</span></div>
    </div>
    {{if .Cases}}<div data-sec="cases" style="margin-top:22px"><h2 style="font-size:19px">Результаты резидентов</h2><div class="cases">{{range .Cases}}<div class="c"><div class="who">{{.Who}}</div><h3>{{.Title}}</h3>{{if .Metrics}}<ul>{{range .Metrics}}<li>{{.}}</li>{{end}}</ul>{{end}}</div>{{end}}</div></div>{{end}}
    <div class="fin" data-sec="price">{{range .Prices}}<div class="pcard"><span>{{.Label}}</span><b>{{.Sum}}</b><p>{{.Note}}</p></div>{{end}}</div>
    {{if .Bonus}}<div class="bonus" style="margin-top:12px;background:#F5F5F5;color:#0A0A0A">🎁 {{.Bonus}}</div>{{end}}
    <div class="cta">
      <button class="btn pri" type="button" data-act="join">Хочу в клуб</button>
      <button class="btn sec" type="button" data-act="ask">Есть вопрос</button>
      <button class="btn ghost" type="button" data-act="think">Пока подумаю</button>
    </div>
    <div class="pay" id="pay" data-sec="pay">
      <h3>Отлично! Команда уже знает и свяжется с вами</h3>
      <p>Можно не ждать: оплатите участие через Kaspi или напишите Рустаму, он ответит лично.</p>
      <div class="amt">{{range .Prices}}<div><span>{{.Label}}</span><b>{{.Sum}}</b></div>{{end}}</div>
      {{if .Kaspi}}<a class="btn pri" href="{{.Kaspi}}" target="_blank" rel="noopener noreferrer" data-click="kaspi">Оплатить в Kaspi</a>
      <p style="font-size:13px;margin-top:8px">В Kaspi введите сумму выбранного пакета из списка выше.</p>{{end}}
      <div class="row"><a class="btn sec" href="{{.WALink}}" target="_blank" rel="noopener noreferrer" data-click="wa">Написать в WhatsApp</a><a class="btn sec" href="{{.TG}}" target="_blank" rel="noopener noreferrer" data-click="tg">Написать в Telegram</a></div>
    </div>
    <div class="note" id="askNote">Рустам ответит лично. Напишите вопрос туда, где вам удобнее:</div>
    <div class="contact" id="askC"><a class="btn pri" href="{{.WALink}}" target="_blank" rel="noopener noreferrer" data-click="wa">WhatsApp {{.WAPhone}}</a><a class="btn sec" href="{{.TG}}" target="_blank" rel="noopener noreferrer" data-click="tg">Telegram</a></div>
    <div class="note" id="thinkNote">Хорошо, без спешки. Доска остаётся доступна по этой ссылке: возвращайтесь к плану, когда будет удобно. Если появится вопрос, напишите нам.</div>
  </section>
</main>
<aside class="side">
  <div class="offer">
    <div class="k">Клуб Business Surgery</div>
    <h3>Разбор каждые 10 дней и трекинг плана</h3>
    <p>Оба основателя, план задач на цикл, ежедневные отчёты и ваша доска на платформе.</p>
    <div class="pr">{{range .Prices}}<div><span>{{.Label}}</span><b>{{.Sum}}</b></div>{{end}}</div>
    {{if .Bonus}}<div class="bonus">🎁 {{.Bonus}}</div>{{end}}
    <div class="bt"><button class="btn pri" type="button" data-act="join">Хочу в клуб</button><button class="btn sec" type="button" data-act="ask">Есть вопрос</button></div>
  </div>
</aside>
</div>
</div>
<footer><div class="w">Business Surgery · Алматы · {{.Year}}. Ссылка личная: она открывает только вашу доску.</div></footer>
<div class="sticky dark" id="sticky"><div class="in"><div class="t"><b>Клуб Business Surgery</b>разбор каждые 10 дней</div><button class="btn pri" type="button" data-act="join" data-sticky="1">Хочу в клуб</button></div></div>
<div class="toast" id="toast"></div>
<script nonce="{{.Nonce}}">
(function(){
  var T = {{.Token}}, base = '/b/' + T, t0 = Date.now(), last = t0, seen = {}, qs = [], qc = [], hid = false;
  function post(p, body, beacon){
    var s = JSON.stringify(body);
    try{ if(beacon && navigator.sendBeacon){ navigator.sendBeacon(base + p, new Blob([s], {type:'application/json'})); return Promise.resolve(); } }catch(e){}
    return fetch(base + p, {method:'POST', headers:{'Content-Type':'application/json'}, body:s, keepalive:true, credentials:'same-origin'}).catch(function(){});
  }
  function flush(beacon){
    var now = Date.now(), s = hid ? 0 : Math.round((now - last) / 1000); last = now;
    if(s > 120) s = 120;
    if(!s && !qs.length && !qc.length) return;
    post('/ev', {s:s, sec:qs.splice(0), click:qc.splice(0)}, beacon);
  }
  post('/ev', {open:true});
  setInterval(function(){ flush(false); }, 15000);
  document.addEventListener('visibilitychange', function(){ if(document.visibilityState === 'hidden'){ flush(true); hid = true; } else { hid = false; last = Date.now(); } });
  window.addEventListener('pagehide', function(){ flush(true); });
  if('IntersectionObserver' in window){
    var io = new IntersectionObserver(function(es){ es.forEach(function(e){ var k = e.target.getAttribute('data-sec'); if(e.isIntersecting && !seen[k]){ seen[k] = 1; qs.push(k); } }); }, {threshold:.35});
    document.querySelectorAll('[data-sec]').forEach(function(el){ io.observe(el); });
    var club = document.getElementById('club'), st = document.getElementById('sticky'), hero = document.querySelector('.hero');
    var inClub = false, pastHero = false;
    function upd(){ st.classList.toggle('on', pastHero && !inClub); }
    new IntersectionObserver(function(es){ inClub = es[0].isIntersecting; upd(); }, {threshold:0}).observe(club);
    new IntersectionObserver(function(es){ pastHero = !es[0].isIntersecting; upd(); }, {threshold:0}).observe(hero);
  }
  function toast(m){ var t = document.getElementById('toast'); t.textContent = m; t.classList.add('on'); setTimeout(function(){ t.classList.remove('on'); }, 2200); }
  function show(id){ ['pay','askNote','askC','thinkNote'].forEach(function(x){ document.getElementById(x).classList.remove('on'); }); (Array.isArray(id) ? id : [id]).forEach(function(x){ document.getElementById(x).classList.add('on'); }); }
  document.addEventListener('click', function(ev){
    var a = ev.target.closest('[data-click]'); if(a){ qc.push(a.getAttribute('data-click')); flush(true); return; }
    var b = ev.target.closest('[data-act]'); if(!b) return;
    var act = b.getAttribute('data-act');
    if(b.getAttribute('data-sticky')) qc.push('sticky_' + act);
    post('/intent', {intent:act});
    if(act === 'join'){ show('pay'); toast('Передали команде'); }
    if(act === 'ask') show(['askNote','askC']);
    if(act === 'think') show('thinkNote');
    var tgt = document.getElementById(act === 'join' ? 'pay' : act === 'ask' ? 'askNote' : 'thinkNote');
    setTimeout(function(){ tgt.scrollIntoView({behavior:'smooth', block:'center'}); }, 60);
  });
})();
</script>
</body></html>`))

var cbGoneTpl = template.Must(template.New("gone").Funcs(cbFuncs).Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow"><meta name="referrer" content="no-referrer"><title>Ваш разбор · Business Surgery</title>
{{safe .Fonts}}<style>` + cbCSS + `
.gone{min-height:80vh;display:flex;flex-direction:column;justify-content:center;max-width:520px;margin:0 auto;padding:40px 20px}
.gone h1{font-size:30px}.gone p{color:#CFCFCF;margin:14px 0 24px}.gone .row{display:grid;gap:10px}</style></head>
<body class="dark"><div class="gone"><img src="/site/logo-white.png" alt="Business Surgery" width="63" height="30" style="margin-bottom:28px">
<h1>Ваш разбор</h1><p>{{.Msg}}</p>
<div class="row"><a class="btn pri" href="{{.WA}}" rel="noopener noreferrer">WhatsApp {{.WAPhone}}</a><a class="btn sec" href="{{.TG}}" rel="noopener noreferrer">Telegram</a></div></div></body></html>`))
