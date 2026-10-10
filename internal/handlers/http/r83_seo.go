package http

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
	"github.com/bnursik/business_surgery_backend/web"
	"github.com/gin-gonic/gin"
)

// ── R83: Маркетинг → SEO, the daily job «SEO/GEO» ──
// «ИИ нас рекомендовал (ChatGPT и т.д.), хочу, чтобы ты сам всё это делал и
// также ежедневно оптимизировал SEO».
//
// Every day (the first run a few minutes after a deploy): web.SEOAudit
// checks every page of the sitemap, the fixes it allows go live at once
// (lastmod by content, descriptions from the page's text), the report and
// a short history go to the club doc bs_seo (the page shows it in
// Маркетинг → SEO). Once a week the AI visibility: the same questions a
// person asks an AI («лучший бизнес-трекинг Алматы», …) go to the free AI
// with web search (Gemini with Google Search grounding); whether Business
// Surgery is named is written down with the history. When the free search
// is refused or used up, the week says «нет данных» (never a guess).

const (
	seoDocKey   = "bs_seo"    // club doc: the report for the page
	seoStateKey = "seo_state" // server doc: page hashes and lastmods
	seoHistMax  = 60          // daily rows kept
	seoAIMax    = 26          // weekly AI rows kept
)

var (
	seoEvery   = 24 * time.Hour
	seoAIEvery = 7 * 24 * time.Hour
	seoRunMu   sync.Mutex
	seoAIPause = 4 * time.Second
	// seoAIAsk: the free AI with web search (tests replace it).
	seoAIAsk func(ctx context.Context, c *ai.Client, prompt string) (string, string, error) = func(ctx context.Context, c *ai.Client, prompt string) (string, string, error) {
		return c.SearchFree(ctx, prompt)
	}
)

// seoPrompts: what owners ask an AI when they look for what BS sells.
var seoPrompts = []string{
	"Какой лучший бизнес-трекинг в Алматы?",
	"Посоветуй бизнес-клуб для собственников бизнеса в Алматы",
	"Как найти бизнес-трекера в Казахстане?",
	"Где в Алматы сделать разбор бизнеса с трекером?",
	"Какие есть клубы предпринимателей в Алматы для роста прибыли?",
	"Сколько стоит бизнес-трекинг в Казахстане и кто его проводит?",
	"Кто в Казахстане помогает собственнику малого бизнеса вырасти, если бизнес упёрся в потолок?",
	"Business Surgery Алматы: что это и стоит ли туда идти?",
}

// seoMention: the club is named in an answer.
var seoMention = regexp.MustCompile(`(?i)business\s*surgery|bxclub|бизнес[\s-]*с[её]рджери|bs\s*club|@bsurgery`)

type seoAIItem struct {
	Q       string `json:"q"`
	Status  string `json:"status"` // упомянут | не упомянут | нет данных
	Model   string `json:"model,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Err     string `json:"err,omitempty"`
}

type seoAIRun struct {
	At        string      `json:"at"`
	Day       string      `json:"day"`
	Asked     int         `json:"asked"`
	Mentioned int         `json:"mentioned"`
	NoData    int         `json:"noData"`
	Items     []seoAIItem `json:"items"`
}

type seoDay struct {
	Day     string `json:"day"`
	Score   int    `json:"score"`
	Pages   int    `json:"pages"`
	Errors  int    `json:"errors"`
	Warns   int    `json:"warns"`
	Fixed   int    `json:"fixed"`
	Broken  int    `json:"broken"`
	Changed int    `json:"changed"`
}

type seoDoc struct {
	Updated string         `json:"updated"`
	Report  *web.SEOReport `json:"report,omitempty"`
	History []seoDay       `json:"history"`
	AI      []seoAIRun     `json:"ai"` // newest first
	Prompts []string       `json:"prompts"`
	Next    string         `json:"next,omitempty"`   // the next daily run
	NextAI  string         `json:"nextAI,omitempty"` // the next AI check
}

type seoState struct {
	Pages map[string]web.SEOPage `json:"pages"`
	Desc  map[string]string      `json:"desc"`
}

func (h *PlatformAI) loadSEODoc(ctx context.Context) (seoDoc, int) {
	var d seoDoc
	doc, err := h.repo.GetDoc(ctx, "club", seoDocKey)
	if err != nil || doc == nil {
		return d, 0
	}
	if !doc.Deleted {
		_ = json.Unmarshal([]byte(doc.Value), &d)
	}
	return d, doc.Version
}

func (h *PlatformAI) loadSEOState(ctx context.Context) (seoState, int) {
	var s seoState
	doc, err := h.repo.GetDoc(ctx, "server", seoStateKey)
	if err == nil && doc != nil && !doc.Deleted {
		_ = json.Unmarshal([]byte(doc.Value), &s)
	}
	if s.Pages == nil {
		s.Pages = map[string]web.SEOPage{}
	}
	if s.Desc == nil {
		s.Desc = map[string]string{}
	}
	if doc == nil {
		return s, 0
	}
	return s, doc.Version
}

// putSEODoc writes the page's doc, again on a version conflict.
func (h *PlatformAI) putSEODoc(ctx context.Context, fn func(d *seoDoc)) error {
	var err error
	for try := 0; try < 4; try++ {
		d, base := h.loadSEODoc(ctx)
		fn(&d)
		d.Prompts = seoPrompts
		if d.History == nil {
			d.History = []seoDay{}
		}
		if d.AI == nil {
			d.AI = []seoAIRun{}
		}
		val, _ := json.Marshal(d)
		if _, err = h.repo.PutDoc(ctx, "club", seoDocKey, base, string(val), false, "server:seo"); err == nil {
			return nil
		}
	}
	return err
}

// RunSEO: the daily audit, and the AI visibility when its week is up.
func (h *PlatformAI) RunSEO(ctx context.Context, forceAI bool) (*web.SEOReport, error) {
	if !seoRunMu.TryLock() {
		return nil, nil
	}
	defer seoRunMu.Unlock()
	now := time.Now()
	day := now.In(tgevents.Almaty).Format("2006-01-02")
	st, stBase := h.loadSEOState(ctx)
	// the fixes of the last run are live before the check (a restart forgets them)
	web.SetSEOFixes(seoLastmods(st.Pages), st.Desc)
	rep, pages, lastmod, descs := web.SEOAudit(st.Pages, day)
	web.SetSEOFixes(lastmod, descs)
	st.Pages, st.Desc = pages, descs
	if val, err := json.Marshal(st); err == nil {
		if _, err := h.repo.PutDoc(ctx, "server", seoStateKey, stBase, string(val), false, "server:seo"); err != nil {
			log.Printf("seo: state not saved: %v", err)
		}
	}
	// AI visibility, weekly
	prev, _ := h.loadSEODoc(ctx)
	var aiRun *seoAIRun
	due := forceAI || len(prev.AI) == 0
	if !due {
		if t, err := time.Parse(time.RFC3339, prev.AI[0].At); err != nil || now.Sub(t) >= seoAIEvery-time.Hour {
			due = true
		}
	}
	if due {
		r := h.seoAIVisibility(ctx, day)
		aiRun = &r
	}
	err := h.putSEODoc(ctx, func(d *seoDoc) {
		d.Updated = rep.At
		r := rep
		d.Report = &r
		row := seoDay{Day: day, Score: rep.Score, Pages: rep.Pages, Errors: rep.Errors, Warns: rep.Warns, Fixed: rep.Fixed, Broken: rep.Broken, Changed: rep.Changed}
		if len(d.History) > 0 && d.History[0].Day == day {
			d.History[0] = row
		} else {
			d.History = append([]seoDay{row}, d.History...)
		}
		if len(d.History) > seoHistMax {
			d.History = d.History[:seoHistMax]
		}
		if aiRun != nil {
			d.AI = append([]seoAIRun{*aiRun}, d.AI...)
			if len(d.AI) > seoAIMax {
				d.AI = d.AI[:seoAIMax]
			}
		}
		d.Next = now.Add(seoEvery).UTC().Format(time.RFC3339)
		if len(d.AI) > 0 {
			if t, err := time.Parse(time.RFC3339, d.AI[0].At); err == nil {
				d.NextAI = t.Add(seoAIEvery).UTC().Format(time.RFC3339)
			}
		}
	})
	aiTxt := "не в этот день"
	if aiRun != nil {
		aiTxt = sprintAI(aiRun)
	}
	log.Printf("seo: daily audit: %d pages, score %d%%, %d errors, %d warnings, %d fixed (descriptions %d, lastmod changed %d), %d internal links (%d broken), sitemap %d urls, llms.txt %d KB, llms-full.txt %d KB, took %s; ai visibility: %s; err=%v",
		rep.Pages, rep.Score, rep.Errors, rep.Warns, rep.Fixed, rep.Counts["description"], rep.Changed, rep.Links, rep.Broken, rep.Sitemap, rep.LLMS>>10, rep.LLMSFull>>10, rep.Took, aiTxt, err)
	for i, is := range rep.Issues {
		if i >= 8 || is.Sev != "error" {
			break
		}
		log.Printf("seo: error %s %s: %s", is.Check, is.URL, is.Msg)
	}
	return &rep, err
}

func sprintAI(r *seoAIRun) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(r.Mentioned) + " из " + strconv.Itoa(r.Asked) + " ответов называют BS")
	if r.NoData > 0 {
		b.WriteString(", нет данных: " + strconv.Itoa(r.NoData))
	}
	return b.String()
}

func seoLastmods(p map[string]web.SEOPage) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = v.Lastmod
	}
	return out
}

// seoAIVisibility asks every prompt; a refused or used-up free search
// makes the rest «нет данных» without more calls.
func (h *PlatformAI) seoAIVisibility(ctx context.Context, day string) seoAIRun {
	run := seoAIRun{At: time.Now().UTC().Format(time.RFC3339), Day: day}
	var stop error
	for i, q := range seoPrompts {
		it := seoAIItem{Q: q}
		if stop != nil || h.AI == nil {
			it.Status = "нет данных"
			if stop != nil {
				it.Err = ai.UserMessage(stop)
			} else {
				it.Err = "ИИ не подключён"
			}
			run.NoData++
			run.Items = append(run.Items, it)
			continue
		}
		if i > 0 && seoAIPause > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(seoAIPause):
			}
		}
		prompt := q + "\nОтветь так, как ответил бы обычному человеку: назови конкретные компании, клубы или специалистов с названиями и ссылками, которые ты нашёл в поиске."
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		ans, model, err := seoAIAsk(cctx, h.AI, prompt)
		cancel()
		run.Asked++
		if err != nil {
			it.Status, it.Err = "нет данных", ai.UserMessage(err)
			run.NoData++
			run.Asked--
			if ai.SearchUnavailable(err) {
				stop = err
			}
			run.Items = append(run.Items, it)
			continue
		}
		it.Model = model
		if loc := seoMention.FindStringIndex(ans); loc != nil {
			it.Status = "упомянут"
			run.Mentioned++
			it.Snippet = seoAround(ans, loc[0], loc[1])
		} else {
			it.Status = "не упомянут"
			it.Snippet = seoAround(ans, 0, 0)
		}
		run.Items = append(run.Items, it)
	}
	return run
}

// seoAround: up to 280 characters of the answer around the mention (or its start).
func seoAround(s string, a, b int) string {
	r := []rune(s)
	ra := len([]rune(s[:a]))
	rb := len([]rune(s[:b]))
	from, to := ra-120, rb+160
	if from < 0 {
		from = 0
	}
	if to > len(r) {
		to = len(r)
	}
	out := strings.Join(strings.Fields(string(r[from:to])), " ")
	if from > 0 {
		out = "…" + out
	}
	if to < len(r) {
		out += "…"
	}
	return out
}

// SEOLoop: the first run a few minutes after start, then once a day
// (counting from the last run).
func (h *PlatformAI) SEOLoop(ctx context.Context) {
	if h.repo == nil {
		return
	}
	wait := 3 * time.Minute
	if d, _ := h.loadSEODoc(ctx); d.Report != nil {
		if t, err := time.Parse(time.RFC3339, d.Report.At); err == nil {
			if left := seoEvery - time.Since(t); left > wait {
				wait = left
			}
		}
	}
	log.Printf("seo: daily audit in %s", wait.Round(time.Minute))
	timer := time.NewTimer(wait)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		_, _ = h.RunSEO(rctx, false)
		cancel()
		timer.Reset(seoEvery)
	}
}

// SEORun: «Проверить сейчас» (the team); ai=1 asks the AI questions too.
func (h *PlatformAI) SEORun(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	if h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no storage"})
		return
	}
	forceAI := c.Query("ai") == "1"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_, _ = h.RunSEO(ctx, forceAI)
	}()
	c.JSON(http.StatusOK, gin.H{"started": true})
}
