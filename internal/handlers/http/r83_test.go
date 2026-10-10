package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
)

// R83: the WhatsApp links are read from the text and the anchors of a page,
// the channel and group pages give the name, only live links about business
// are taken.
func TestWhatsAppParse(t *testing.T) {
	txt := waFromText("Вступайте в чат предпринимателей Алматы: chat.whatsapp.com/AbCdEfGhIjKlMnOpQrStUv и канал https://www.whatsapp.com/channel/0029VaAbCdEfGhIjKlMnOp12 !")
	if len(txt) != 2 || !strings.Contains(txt["https://chat.whatsapp.com/AbCdEfGhIjKlMnOpQrStUv"], "предпринимателей") || txt["https://www.whatsapp.com/channel/0029VaAbCdEfGhIjKlMnOp12"] == "" {
		t.Fatalf("text %v", txt)
	}
	page := `<a href="https://chat.whatsapp.com/invite/ZzZzZzZzZzZzZzZzZzZzZz">Бизнес-клуб Шымкент</a>
<a href="/groups/biznes-almaty">Бизнес Алматы</a><a href="/groups/cats">Котики</a><a href="https://other.kz/biznes">Бизнес</a>
<a href="https://wa.me/77010000000">Написать</a>`
	found, deeper := waFromHTML("https://dir.kz/list", page)
	if len(found) != 1 || found["https://chat.whatsapp.com/ZzZzZzZzZzZzZzZzZzZzZz"] != "Бизнес-клуб Шымкент" {
		t.Fatalf("found %v", found)
	}
	if len(deeper) != 1 || deeper[0] != "https://dir.kz/groups/biznes-almaty" {
		t.Fatalf("deeper %v", deeper)
	}
	ch := parseWAPage(`<meta property="og:title" content="Kapital.kz | WhatsApp Channel"><meta property="og:description" content="Деловые новости Казахстана"><div>12,4K followers</div>`, "канал")
	if ch.Name != "Kapital.kz" || ch.Followers != 12400 || ch.Desc == "" {
		t.Fatalf("channel %+v", ch)
	}
	gr := parseWAPage(`<meta property="og:title" content="WhatsApp Group Invite"><h3 class="_9vd5">Предприниматели Астаны</h3>`, "группа")
	if gr.Name != "Предприниматели Астаны" {
		t.Fatalf("group %+v", gr)
	}
	dead := parseWAPage(`<meta property="og:title" content="WhatsApp Group Invite"><title>WhatsApp</title>`, "группа")
	if dead.Name != "" {
		t.Fatalf("dead %+v", dead)
	}
	if ok, _ := waTake(waInfo{Name: "Котики Алматы"}, &waCand{}); ok {
		t.Fatal("off topic from a directory must not be taken")
	}
	if ok, _ := waTake(waInfo{Name: "Kapital.kz"}, &waCand{Biz: true}); !ok {
		t.Fatal("a channel on a business page is taken")
	}
	if ok, _ := waTake(waInfo{Name: "Женский бизнес-клуб"}, &waCand{}); !ok {
		t.Fatal("a business club is taken")
	}
	if ok, _ := waTake(waInfo{Name: "Бизнес с Атоми"}, &waCand{Biz: true}); ok {
		t.Fatal("network marketing is not taken")
	}
	if ok, why := waTake(waInfo{}, &waCand{Biz: true}); ok || why == "" {
		t.Fatal("a dead link is not taken")
	}
}

// R83: one run finds links in the catalogue's Telegram posts, on its source
// pages and one level deeper in a directory, opens them and adds the live
// ones about business with the page they were seen on; the next run does
// not open them again.
func TestRefreshWhatsApp(t *testing.T) {
	repo, ctx := testPlatformDB(t, commCatKey, waCandKey)
	post := func(ch, text string) string {
		return `<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="` + ch + `/9">` +
			`<div class="tgme_widget_message_text js-message_text" dir="auto">` + text + `</div>` +
			`<a class="tgme_widget_message_date"><time datetime="2026-10-09T08:00:00+00:00"></time></a></div></div>`
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s/biz_kz":
			if r.URL.Query().Get("q") == "whatsapp" {
				_, _ = w.Write([]byte(`<div class="tgme_channel_info"></div>` + post("biz_kz", `Наш чат собственников: <a href="https://chat.whatsapp.com/GroupBizOwnersAlmaty01">вступить</a>`)))
				return
			}
			_, _ = w.Write([]byte(`<div class="tgme_channel_info"></div>` + post("biz_kz", `Канал: whatsapp.com/channel/0029ChannelBizNews000001`)))
		case "/s/whatsapp_group_ru":
			_, _ = w.Write([]byte(`<div class="tgme_channel_info"></div>` + post("whatsapp_group_ru", `Котики chat.whatsapp.com/GroupCatsCatsCats00001`)))
		case "/org":
			_, _ = w.Write([]byte(`<html><a href="/clubs/biznes">Бизнес-клубы</a></html>`))
		case "/clubs/biznes":
			_, _ = w.Write([]byte(`<a href="https://chat.whatsapp.com/GroupDeadLinkDead000001">Клуб предпринимателей</a>`))
		case "/wa/GroupBizOwnersAlmaty01":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Собственники Алматы"><h3>Собственники Алматы</h3>`))
		case "/wa/channel/0029ChannelBizNews000001":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Бизнес новости KZ | WhatsApp Channel"><p>3 500 followers</p>`))
		case "/wa/GroupCatsCatsCats00001":
			_, _ = w.Write([]byte(`<meta property="og:title" content="Котики и собачки">`))
		case "/wa/GroupDeadLinkDead000001":
			_, _ = w.Write([]byte(`<meta property="og:title" content="WhatsApp Group Invite">`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	// WhatsApp links go to the test server
	tr := &rewrite{to: srv.URL, base: http.DefaultTransport}
	ob, op, oh, opg, oagg := tgevents.BaseURL, waPause, waHTTP, waPages, waAggTG
	tgevents.BaseURL, waPause, waHTTP = srv.URL+"/s/", 0, &http.Client{Transport: tr}
	waPages, waAggTG = []waSrc{{srv.URL + "/org", true}}, []string{"whatsapp_group_ru"}
	defer func() { tgevents.BaseURL, waPause, waHTTP, waPages, waAggTG = ob, op, oh, opg, oagg }()

	h := NewPlatformAI(repo, &ai.Client{})
	cat := commCat{Items: []commItem{{ID: "tg-biz_kz", Name: "Biz KZ", URL: "https://t.me/biz_kz", Platform: "telegram", Origin: "seed"}}}
	val, _ := json.Marshal(cat)
	if _, err := repo.PutDoc(ctx, "club", commCatKey, 0, string(val), false, "test"); err != nil {
		t.Fatal(err)
	}
	res, err := h.refreshWhatsApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Links != 4 || res.Opened != 4 || res.Added != 2 || res.Total != 2 {
		t.Fatalf("run %+v", res)
	}
	got, _ := h.loadComm(ctx)
	byURL := map[string]commItem{}
	for _, it := range got.Items {
		byURL[it.URL] = it
	}
	g := byURL["https://chat.whatsapp.com/GroupBizOwnersAlmaty01"]
	if g.Name != "Собственники Алматы" || g.Platform != "whatsapp" || g.Kind != "чат" || g.Src != "https://t.me/biz_kz/9" || g.City != "Алматы" || g.Origin != "found" {
		t.Fatalf("group %+v", g)
	}
	c := byURL["https://www.whatsapp.com/channel/0029ChannelBizNews000001"]
	if c.Name != "Бизнес новости KZ" || c.Kind != "канал" || c.Size != 3500 {
		t.Fatalf("channel %+v", c)
	}
	if got.WARun == nil || got.WARun.Added != 2 {
		t.Fatalf("waRun %+v", got.WARun)
	}
	cands, _ := h.loadWACand(ctx)
	if cands["https://chat.whatsapp.com/GroupCatsCatsCats00001"].Why != "не про бизнес" || !strings.Contains(cands["https://chat.whatsapp.com/GroupDeadLinkDead000001"].Why, "не открылась") {
		t.Fatalf("cands %+v %+v", cands["https://chat.whatsapp.com/GroupCatsCatsCats00001"], cands["https://chat.whatsapp.com/GroupDeadLinkDead000001"])
	}
	if cands["https://chat.whatsapp.com/GroupDeadLinkDead000001"].Src != srv.URL+"/clubs/biznes" {
		t.Fatalf("deeper src %+v", cands["https://chat.whatsapp.com/GroupDeadLinkDead000001"])
	}
	res, _ = h.refreshWhatsApp(ctx)
	if res.Opened != 0 || res.Added != 0 || res.Total != 2 {
		t.Fatalf("second run %+v", res)
	}
}

// rewrite sends whatsapp.com links to the test server (/wa/...).
type rewrite struct {
	to   string
	base http.RoundTripper
}

func (r *rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	if strings.HasSuffix(host, "whatsapp.com") {
		u := r.to + "/wa" + req.URL.Path
		if strings.HasPrefix(req.URL.Path, "/channel/") {
			u = r.to + "/wa" + req.URL.Path
		}
		nr, _ := http.NewRequestWithContext(req.Context(), req.Method, u, nil)
		return r.base.RoundTrip(nr)
	}
	return r.base.RoundTrip(req)
}

// R83: the daily SEO job writes the report and the history; the weekly AI
// check records a mention, a miss and «нет данных» when the free search is
// refused (and asks no more that week).
func TestRunSEO(t *testing.T) {
	repo, ctx := testPlatformDB(t, seoDocKey, seoStateKey)
	oa, op := seoAIAsk, seoAIPause
	defer func() { seoAIAsk, seoAIPause = oa, op }()
	seoAIPause = 0
	calls := 0
	seoAIAsk = func(ctx context.Context, c *ai.Client, prompt string) (string, string, error) {
		calls++
		switch calls {
		case 1:
			return "Вот варианты: клуб Business Surgery (bxclub.kz) в Алматы проводит разборы каждые 10 дней.", "gemini-test", nil
		case 2:
			return "Можно обратиться в бизнес-школы Алматы.", "gemini-test", nil
		}
		return "", "", ai.ErrNoSearch
	}
	h := NewPlatformAI(repo, &ai.Client{})
	rep, err := h.RunSEO(ctx, false)
	if err != nil || rep == nil {
		t.Fatal(err)
	}
	if rep.Pages < 100 || rep.Errors != 0 || rep.Broken != 0 {
		for _, is := range rep.Issues {
			if is.Sev == "error" {
				t.Log(is)
			}
		}
		t.Fatalf("report: %d pages, %d errors, %d broken", rep.Pages, rep.Errors, rep.Broken)
	}
	d, _ := h.loadSEODoc(ctx)
	if d.Report == nil || len(d.History) != 1 || len(d.AI) != 1 || len(d.Prompts) != len(seoPrompts) {
		t.Fatalf("doc %+v", d)
	}
	a := d.AI[0]
	if calls != 3 || a.Mentioned != 1 || a.Asked != 2 || a.NoData != len(seoPrompts)-2 || a.Items[0].Status != "упомянут" || !strings.Contains(a.Items[0].Snippet, "Business Surgery") ||
		a.Items[1].Status != "не упомянут" || a.Items[2].Status != "нет данных" {
		t.Fatalf("ai %+v (calls %d)", a, calls)
	}
	// the same day again: one history row, the AI waits for its week
	if _, err := h.RunSEO(ctx, false); err != nil {
		t.Fatal(err)
	}
	d, _ = h.loadSEODoc(ctx)
	if len(d.History) != 1 || len(d.AI) != 1 || calls != 3 {
		t.Fatalf("second: %d days, %d ai, %d calls", len(d.History), len(d.AI), calls)
	}
	st, _ := h.loadSEOState(ctx)
	if len(st.Pages) != rep.Sitemap {
		t.Fatalf("state %d pages, sitemap %d", len(st.Pages), rep.Sitemap)
	}
}

// R83: an external card already in the club takes the shipped text (the
// server owns it); the team's own cards stay as they are.
func TestMergeLibExtUpdatesExternal(t *testing.T) {
	repo, ctx := testPlatformDB(t, libExtStateKey, "bs_diag", "bs_tools", "bs_questions", "bs_libver")
	put := func(k, v string) {
		if _, err := repo.PutDoc(ctx, "club", k, 0, v, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	put("bs_libver", strconv.Itoa(libExtMinLib))
	put("bs_diag", `[{"title":"x"}]`)
	put("bs_questions", `{"Финансы":["q"]}`)
	put("bs_tools", `[{"id":"ext_f12_sales_calc","isExt":true,"title":"Калькулятор продаж","short":"старый текст"},{"id":"own","title":"Своё","short":"команда"}]`)
	h := NewPlatformAI(repo, nil)
	if _, err := h.MergeLibExt(ctx); err != nil {
		t.Fatal(err)
	}
	d, _ := repo.GetDoc(ctx, "club", "bs_tools")
	var list []map[string]any
	_ = json.Unmarshal([]byte(d.Value), &list)
	if list[0]["short"] == "старый текст" || list[0]["link"] != "https://t.me/Fantastik_12/107" || list[1]["short"] != "команда" {
		t.Fatalf("%v | %v", list[0], list[1])
	}
}
