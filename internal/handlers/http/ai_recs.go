package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Рекомендации ИИ: раз в день (07:00 Алматы) сервер находит в интернете одну
// сильную мировую практику, инструмент или фреймворк для собственника малого
// и среднего бизнеса в Казахстане, проверяет, что её ещё нет в библиотеке
// (bs_tools, bs_diag, расширение библиотеки, прошлые рекомендации), и кладёт
// в club doc bs_ai_recs. Дальше сервер решает сам (ai_recs_auto.go):
// понятное добавляет в библиотеку в полном формате, кардинальное спрашивает
// у владельца в боте. Команда может решить и на платформе
// (POST /ai/recs/:id {"action":"add"|"replace"|"reject"}).
//
// bs_ai_recs = {"updated": RFC3339, "items": [rec...]}, новые сверху.
// rec = {id, date: "YYYY-MM-DD", at, status: "new"|"ask"|"added"|"rejected",
//        kind: "tool"|"diag", organ, title, summary, source, company, author,
//        url, why, steps: [5], metrics: [], adapt,
//        item: готовая карточка в формате bs_tools / bs_diag,
//        t, d, k: "world" (как карточки страницы «Рекомендации ИИ»),
//        addedAt, addedTo, rejectedAt,
//        note: строка истории («Добавлено автоматически», «Ждёт решения владельца: ...»),
//        auto, askWhy, similar, askSent, replaced, by}

const (
	aiRecsKey  = "bs_ai_recs"
	aiRecsHour = 7
	aiRecsMax  = 400
)

// Органы клуба по кругу: каждый день следующий.
var aiRecOrgans = []string{"Финансы", "Продажи", "Маркетинг", "Команда", "Процессы", "Стратегия", "Продукт", "Аналитика"}

var aiRecTone = map[string]string{"Стратегия": "#FFFFFF", "Маркетинг": "#CFCFCF", "Продажи": "#B5B5B5",
	"Команда": "#AAAAAA", "Финансы": "#E8E8E8", "Процессы": "#949494", "Аналитика": "#B6B6B6", "Продукт": "#C4C4C4"}

const aiRecPrompt = `Ты аналитик клуба бизнес-трекинга Business Surgery (Алматы). Резиденты: собственники малого и среднего бизнеса в Казахстане с чистой прибылью 2-20 млн ₸ в месяц (услуги, розница, общепит, производство, онлайн).
Найди в интернете ОДНУ лучшую мировую практику, инструмент или фреймворк по органу «%s», которую реально внедрить в таком бизнесе за 1-4 недели без больших бюджетов. Бери то, что проверено компаниями или авторами с именем: методики известных компаний (Toyota, Amazon, Netflix, Basecamp, Zappos и т.п.), книги и авторы (Друкер, Голдратт, Кови, Хормози и т.п.), исследования бизнес-школ. Укажи реальный источник со ссылкой, которую ты нашёл в поиске.
Не предлагай то, что уже есть в библиотеке клуба (даже под другим названием): %s.
Если практика лечит проблему, верни kind "tool" (инструмент). Если это способ распознать типовую болезнь бизнеса, верни kind "diag" (диагноз).
Пиши по-русски, коротко и конкретно, без тире «—», числа как «10 000». Не выдумывай цифры, факты и ссылки: если цифры нет в источнике, не пиши её.
Верни ТОЛЬКО JSON:
{"kind":"tool|diag","title":"короткое название до 6 слов","summary":"одно предложение: что это","source":"где описано: книга, статья, кейс","company":"компания, если есть","author":"автор, если есть","url":"ссылка на источник","why":"почему это работает, 2-3 предложения","steps":["5 шагов внедрения в бизнесе резидента"],"metrics":["2-4 метрики, по которым видно результат"],"adapt":"как адаптировать для Казахстана, 1-2 предложения","short":"для инструмента: суть в одной строке","example":"для инструмента: пример применения в бизнесе вроде кофейни, клиники, автомойки","time":"для инструмента: сколько занимает","check":["для инструмента: 3-4 пункта проверки, что внедрено"],"desc":"для диагноза: как выглядит проблема","signs":["для диагноза: 4 признака"],"questions":["для диагноза: 3 вопроса собственнику"],"risk":"для диагноза: что будет через год, если не лечить"}`

const aiRecDupPrompt = `Кандидат в библиотеку клуба: «%s». Суть: %s
Вот названия того, что уже есть в библиотеке и в прошлых рекомендациях:
%s
Это то же самое по смыслу, что какой-то из пунктов (тот же метод, инструмент или проблема под другим названием)? Похожая тема не считается дублем, дубль это тот же метод.
Верни ТОЛЬКО JSON {"duplicate": true или false, "of": "название из списка или пусто"}`

type aiRecCand struct {
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Source    string   `json:"source"`
	Company   string   `json:"company"`
	Author    string   `json:"author"`
	URL       string   `json:"url"`
	Why       string   `json:"why"`
	Steps     []string `json:"steps"`
	Metrics   []string `json:"metrics"`
	Adapt     string   `json:"adapt"`
	Short     string   `json:"short"`
	Example   string   `json:"example"`
	Time      string   `json:"time"`
	Check     []string `json:"check"`
	Desc      string   `json:"desc"`
	Signs     []string `json:"signs"`
	Questions []string `json:"questions"`
	Risk      string   `json:"risk"`
}

// recClean: brand rules for model text (no em dash), trimmed.
func recClean(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " — ", ": ")
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "–", "-")
	return s
}

func recList(in []string, max int) []string {
	var out []string
	for _, x := range in {
		if x = recClean(x); x != "" && len(out) < max {
			out = append(out, x)
		}
	}
	return out
}

func (c *aiRecCand) clean() {
	for _, p := range []*string{&c.Kind, &c.Title, &c.Summary, &c.Source, &c.Company, &c.Author, &c.URL, &c.Why, &c.Adapt, &c.Short, &c.Example, &c.Time, &c.Desc, &c.Risk} {
		*p = recClean(*p)
	}
	c.Title = strings.Trim(c.Title, "«»\"' .")
	c.Steps, c.Metrics, c.Check = recList(c.Steps, 5), recList(c.Metrics, 4), recList(c.Check, 5)
	c.Signs, c.Questions = recList(c.Signs, 5), recList(c.Questions, 4)
	if c.Kind != "diag" {
		c.Kind = "tool"
	}
	if !strings.HasPrefix(c.URL, "http") {
		c.URL = ""
	}
}

// card: the item in the exact shape of bs_tools / bs_diag.
func (c *aiRecCand) card(organ string) map[string]any {
	col := aiRecTone[organ]
	if col == "" {
		col = "#FFFFFF"
	}
	src := strings.Trim(strings.Join(nonEmpty(c.Company, c.Author, c.Source), ", "), " ,")
	if c.Kind == "diag" {
		desc := c.Desc
		if desc == "" {
			desc = c.Summary
		}
		return map[string]any{"organ": organ, "color": "#E8E8E8", "icon": "◆", "title": c.Title, "desc": desc,
			"signs": recNZ(c.Signs), "questions": recNZ(c.Questions), "risk": c.Risk, "origin": "ai_rec", "source": src, "url": c.URL}
	}
	short := c.Short
	if short == "" {
		short = c.Summary
	}
	why := c.Why
	if c.Adapt != "" {
		why = strings.TrimSpace(why + " " + c.Adapt)
	}
	check := c.Check
	if len(check) == 0 {
		check = c.Metrics
	}
	return map[string]any{"organ": organ, "color": col, "icon": "◇", "title": c.Title, "short": short, "why": why,
		"how": recNZ(c.Steps), "example": c.Example, "time": c.Time, "check": recNZ(check), "origin": "ai_rec", "source": src, "url": c.URL}
}

func recNZ(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// libTitles: everything the library already has, for the duplicate check.
func (h *PlatformAI) libTitles(ctx context.Context, recs []any) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if n := libNorm(t); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, strings.TrimSpace(t))
		}
	}
	for _, key := range []string{"bs_tools", "bs_diag"} {
		if d, err := h.repo.GetDoc(ctx, "club", key); err == nil && d != nil && !d.Deleted {
			var list []map[string]any
			_ = json.Unmarshal([]byte(d.Value), &list)
			for _, it := range list {
				t, _ := it["title"].(string)
				add(t)
			}
		}
	}
	if ext, err := content.LibExt(); err == nil {
		for _, l := range [][]map[string]any{ext.Tools, ext.Diag} {
			for _, it := range l {
				t, _ := it["title"].(string)
				add(t)
			}
		}
	}
	for _, r := range recs {
		if m, ok := r.(map[string]any); ok {
			t, _ := m["title"].(string)
			add(t)
		}
	}
	return out
}

func readRecs(raw string) (map[string]any, []any) {
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &doc)
	items, _ := doc["items"].([]any)
	return doc, items
}

func recStr(m any, k string) string {
	if mm, ok := m.(map[string]any); ok {
		s, _ := mm[k].(string)
		return s
	}
	return ""
}

// nextRecOrgan: the organ after the newest recommendation's one.
func nextRecOrgan(items []any, now time.Time) string {
	if len(items) > 0 {
		last := recStr(items[0], "organ")
		for i, o := range aiRecOrgans {
			if o == last {
				return aiRecOrgans[(i+1)%len(aiRecOrgans)]
			}
		}
	}
	return aiRecOrgans[now.In(almaty).YearDay()%len(aiRecOrgans)]
}

var errRecDone = errors.New("рекомендация на сегодня уже есть")

// dailyRec finds one recommendation and stores it. force: even if today has one.
func (h *PlatformAI) dailyRec(ctx context.Context, now time.Time, force bool) (map[string]any, error) {
	if h.repo == nil {
		return nil, errors.New("no repo")
	}
	today := now.In(almaty).Format("2006-01-02")
	var items []any
	if d, err := h.repo.GetDoc(ctx, "club", aiRecsKey); err == nil && d != nil && !d.Deleted {
		_, items = readRecs(d.Value)
	}
	if !force {
		for _, it := range items {
			if recStr(it, "date") == today {
				return nil, errRecDone
			}
		}
	}
	organ := nextRecOrgan(items, now)
	titles := h.libTitles(ctx, items)
	var rejected []string
	var cand *aiRecCand
	var lastErr error
	for try := 0; try < 3 && cand == nil; try++ {
		c, err := h.recCandidate(ctx, organ, titles, rejected)
		if err != nil {
			lastErr = err
			continue
		}
		if dup := recDupByTitle(c.Title, titles); dup != "" {
			rejected = append(rejected, c.Title)
			lastErr = fmt.Errorf("дубль: %s", dup)
			continue
		}
		if dup := h.recDupByMeaning(ctx, c, titles); dup != "" {
			rejected = append(rejected, c.Title)
			lastErr = fmt.Errorf("дубль по смыслу: %s", dup)
			continue
		}
		cand = c
	}
	if cand == nil {
		if lastErr == nil {
			lastErr = errors.New("ИИ не предложил практику")
		}
		return nil, lastErr
	}
	rec := map[string]any{
		"id": "rec_" + now.In(almaty).Format("20060102") + "_" + newID()[:6], "date": today,
		"at": now.UTC().Format(time.RFC3339), "status": "new", "kind": cand.Kind, "organ": organ,
		"title": cand.Title, "summary": cand.Summary, "source": cand.Source, "company": cand.Company,
		"author": cand.Author, "url": cand.URL, "why": cand.Why, "steps": recNZ(cand.Steps),
		"metrics": recNZ(cand.Metrics), "adapt": cand.Adapt, "item": cand.card(organ),
		"t": cand.Title, "d": cand.Summary, "k": "world",
	}
	err := h.updateRecs(ctx, func(items []any) ([]any, bool) {
		if !force {
			for _, it := range items {
				if recStr(it, "date") == today {
					return items, false
				}
			}
		}
		items = append([]any{rec}, items...)
		if len(items) > aiRecsMax {
			items = items[:aiRecsMax]
		}
		return items, true
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// updateRecs changes the items of bs_ai_recs (other fields stay).
func (h *PlatformAI) updateRecs(ctx context.Context, fn func([]any) ([]any, bool)) error {
	var err error
	for try := 0; try < 4; try++ {
		base, raw := 0, ""
		if d, e := h.repo.GetDoc(ctx, "club", aiRecsKey); e == nil && d != nil {
			base = d.Version
			if !d.Deleted {
				raw = d.Value
			}
		}
		doc, items := readRecs(raw)
		var ok bool
		if items, ok = fn(items); !ok {
			return nil
		}
		if items == nil {
			items = []any{}
		}
		doc["items"], doc["updated"] = items, time.Now().UTC().Format(time.RFC3339)
		val, _ := json.Marshal(doc)
		if _, err = h.repo.PutDoc(ctx, "club", aiRecsKey, base, string(val), false, "server:ai_recs"); err == nil {
			return nil
		}
	}
	return err
}

func (h *PlatformAI) recCandidate(ctx context.Context, organ string, titles, rejected []string) (*aiRecCand, error) {
	excl := append(append([]string{}, titles...), rejected...)
	list := strings.Join(excl, "; ")
	if r := []rune(list); len(r) > 12000 {
		list = string(r[:12000])
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	ans, err := h.AI.Search(c, fmt.Sprintf(aiRecPrompt, organ, list))
	if err != nil {
		return nil, err
	}
	var cand aiRecCand
	if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &cand) != nil {
		return nil, errors.New("ИИ не вернул JSON рекомендации")
	}
	cand.clean()
	if cand.Title == "" || len(cand.Steps) < 3 || cand.Why == "" {
		return nil, errors.New("ИИ вернул неполную рекомендацию")
	}
	return &cand, nil
}

func recDupByTitle(title string, titles []string) string {
	n := libNorm(title)
	for _, t := range titles {
		if libNorm(t) == n {
			return t
		}
	}
	return ""
}

// recDupByMeaning asks the model whether the candidate is an existing item
// under another name; on a model error the candidate passes.
func (h *PlatformAI) recDupByMeaning(ctx context.Context, c *aiRecCand, titles []string) string {
	if len(titles) == 0 {
		return ""
	}
	list := "- " + strings.Join(titles, "\n- ")
	if r := []rune(list); len(r) > 15000 {
		list = string(r[:15000])
	}
	cx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ans, err := h.AI.JSON(cx, "Ты редактор библиотеки инструментов для собственников бизнеса. Отвечай только JSON.",
		fmt.Sprintf(aiRecDupPrompt, c.Title, c.Summary+" "+c.Why, list))
	if err != nil {
		log.Printf("ai recs: duplicate check: %v", err)
		return ""
	}
	var out struct {
		Duplicate bool   `json:"duplicate"`
		Of        string `json:"of"`
	}
	if js := ai.JSONFrom(ans); js == "" || json.Unmarshal([]byte(js), &out) != nil || !out.Duplicate {
		return ""
	}
	if out.Of == "" {
		out.Of = "?"
	}
	return out.Of
}

// untilAlmatyHour: time left until the next hh:00 in Almaty.
func untilAlmatyHour(now time.Time, hh int) time.Duration {
	a := now.In(almaty)
	next := time.Date(a.Year(), a.Month(), a.Day(), hh, 0, 0, 0, almaty)
	if !next.After(a) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(a)
}

// RecsLoop: one recommendation a day at 07:00 Almaty (after a restart past
// 07:00 the day's one is made if it is missing). R39: a failed run is tried
// again within the day (ai_recs_status.go) and leaves its error in
// bs_ai_recs.run for the platform and /status.
func (h *PlatformAI) RecsLoop(ctx context.Context) {
	t := time.NewTimer(RecsStartDelay)
	started := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !started && h.repo != nil {
			started = true
			c, cancel := context.WithTimeout(ctx, time.Minute)
			if err := h.LoadRichExtra(c); err != nil {
				log.Printf("ai recs: rich items: %v", err)
			}
			cancel()
			go h.recsAskLoop(ctx)
		}
		t.Reset(h.recsTick(ctx, time.Now()).Sub(time.Now()))
	}
}

// RecsStartDelay: the first run waits for the server to settle (tests: 0).
var RecsStartDelay = 3 * time.Minute

// recsTick: one pass of the loop; the answer is when to run again.
func (h *PlatformAI) recsTick(ctx context.Context, now time.Time) time.Time {
	if now.In(almaty).Hour() < aiRecsHour || h.repo == nil {
		return now.Add(untilAlmatyHour(now, aiRecsHour))
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if h.AI == nil || h.AI.Status()["text"] == "" {
		err := errors.New("нет ключа Claude")
		next := recsNext(now, false, 0)
		h.noteRecsRun(c, now, err, next)
		log.Printf("ai recs: %v", err)
		return next
	}
	rec, err := h.dailyRec(c, now, false)
	if err == errRecDone {
		// Found earlier but not decided on (a restart in between).
		if d, e := h.repo.GetDoc(c, "club", aiRecsKey); e == nil && d != nil && !d.Deleted {
			_, items := readRecs(d.Value)
			if rec = pendingAutoRec(items, now.In(almaty).Format("2006-01-02")); rec == nil {
				return now.Add(untilAlmatyHour(now, aiRecsHour))
			}
			err = nil
		}
	}
	if err != nil {
		tries := h.noteRecsRun(c, now, err, time.Time{})
		next := recsNext(now, true, tries)
		h.noteRecsNext(c, next)
		log.Printf("ai recs: %v (attempt %d, next %s)", err, tries, next.In(almaty).Format("02.01 15:04"))
		return next
	}
	if rec != nil {
		out, aerr := h.autoRec(c, rec, now)
		log.Printf("ai recs: %s (%s): %s %v", rec["title"], rec["organ"], out, aerr)
	}
	next := recsNext(now, false, 0)
	h.noteRecsRun(c, now, nil, next)
	return next
}

// RecsNow: POST /ai/recs (team) finds one more recommendation now.
func (h *PlatformAI) RecsNow(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	rec, err := h.dailyRec(ctx, time.Now(), true)
	if err != nil {
		// R32c: one clean sentence (a used-up quota: ai.QuotaMessage), never the API's JSON
		c.JSON(http.StatusOK, gin.H{"error": ai.UserMessage(err), "quota": ai.IsQuota(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rec": rec})
}

// RecAction: POST /ai/recs/:id {"action": "add"|"replace"|"reject"} (team).
// add puts rec.item into bs_tools (kind tool) or bs_diag (kind diag) unless a
// card with that title is there, and marks the rec "added"; replace puts it
// in place of rec.similar. The rich card made for it goes into the library.
func (h *PlatformAI) RecAction(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Action != "add" && req.Action != "reject" && req.Action != "replace" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action"})
		return
	}
	code, out := h.recAction(c.Request.Context(), c.Param("id"), req.Action)
	c.JSON(code, out)
}

func (h *PlatformAI) recAction(ctx context.Context, id, action string) (int, gin.H) {
	note := map[string]string{"add": "Добавлено командой на платформе", "replace": "Заменено командой на платформе",
		"reject": "Отклонено командой на платформе"}[action]
	return h.recApply(ctx, id, action, "platform", note)
}

// appendLibItem adds a card to bs_tools / bs_diag unless its title is there.
func (h *PlatformAI) appendLibItem(ctx context.Context, key string, item map[string]any) (bool, error) {
	title, _ := item["title"].(string)
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", key)
		if err != nil {
			return false, err
		}
		if d == nil || d.Deleted {
			return false, errors.New("библиотеки нет на сервере")
		}
		var list []map[string]any
		if json.Unmarshal([]byte(d.Value), &list) != nil {
			return false, errors.New("библиотека не читается")
		}
		for _, it := range list {
			if t, _ := it["title"].(string); libNorm(t) == libNorm(title) {
				return false, nil
			}
		}
		list = append(list, item)
		val, _ := json.Marshal(list)
		if _, err := h.repo.PutDoc(ctx, "club", key, d.Version, string(val), false, "server:ai_recs"); err == nil {
			return true, nil
		} else if err != pg.ErrPlatformConflict {
			return false, err
		}
	}
	return false, pg.ErrPlatformConflict
}
