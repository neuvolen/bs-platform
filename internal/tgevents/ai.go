package tgevents

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
)

// JSONFunc: the text model in JSON mode (ai.Client.JSON).
type JSONFunc func(ctx context.Context, system, prompt string) (string, error)

const aiSystem = `Ты читаешь посты Telegram-канала с анонсами мероприятий и возможностей для предпринимателей и стартапов.
Из каждого поста выпиши мероприятия (дата проведения) и возможности: гранты, программы, акселераторы, конкурсы, стажировки (у них дедлайн подачи заявок).
Верни ТОЛЬКО JSON: {"items":[{"post":номер поста,"title":"коротко, по-русски, без эмодзи","date":"YYYY-MM-DD дата проведения или пусто","time":"HH:MM или пусто","deadline":"YYYY-MM-DD дедлайн заявок или пусто","kind":"мероприятие|возможность","place":"город или адрес, или Онлайн","online":true|false,"org":"организатор или пусто","url":"ссылка на регистрацию из поста или пусто","desc":"одно предложение о сути","tags":["грант|акселератор|конкурс|обучение|нетворкинг|конференция|IT|стартапы|инвестиции"]}]}
Год без указания бери из даты поста; если дата раньше даты поста больше чем на два месяца, это следующий год.
Только то, что есть в тексте. Рекламу, вакансии и посты без даты и дедлайна пропусти. Пост-подборку разбей на отдельные пункты.`

// AIExtract reads posts the rules could not read (digests, unclear dates)
// with the free text model: one call for all of them.
func AIExtract(ctx context.Context, model JSONFunc, posts []Post, now time.Time) ([]ai.Event, error) {
	if model == nil || len(posts) == 0 {
		return nil, nil
	}
	var b strings.Builder
	byID := map[int]Post{}
	for _, p := range posts {
		byID[p.ID] = p
		t := p.Text
		if r := []rune(t); len(r) > 1800 {
			t = string(r[:1800])
		}
		links := ""
		if len(p.Links) > 0 {
			ls := p.Links
			if len(ls) > 8 {
				ls = ls[:8]
			}
			links = "\nСсылки: " + strings.Join(ls, " ")
		}
		fmt.Fprintf(&b, "=== Пост %d от %s ===\n%s%s\n\n", p.ID, p.Time.In(Almaty).Format("2006-01-02"), t, links)
	}
	ans, err := model(ctx, aiSystem, "Сегодня "+now.In(Almaty).Format("2006-01-02")+".\n\n"+b.String())
	if err != nil {
		return nil, err
	}
	evs, err := ai.ParseEventsLoose(ans, now.In(Almaty))
	if err != nil {
		return nil, err
	}
	out := evs[:0]
	for _, e := range evs {
		id, _ := strconv.Atoi(strings.TrimSpace(e.Post))
		p, ok := byID[id]
		if !ok && len(posts) == 1 {
			p, ok = posts[0], true
		}
		e.Post = ""
		if ok {
			e.Post, e.Source = p.URL(), "t.me/"+p.Channel
		} else {
			e.Source = "t.me/" + posts[0].Channel
		}
		if e.URL == "" {
			e.URL = e.Post
		}
		if e.Kind != KindOpportunity {
			e.Kind = ""
			if e.Date == e.Deadline && e.Deadline != "" && e.Time == "" {
				e.Kind = KindOpportunity
			}
		}
		if e.Kind == KindOpportunity && e.Deadline != "" {
			e.Date = e.Deadline
			e.Tags = append([]string{KindOpportunity}, e.Tags...)
		}
		e.Title = cut(e.Title, 140)
		e.Desc = cut(e.Desc, 280)
		e.Origin = "tg"
		out = append(out, e)
	}
	return out, nil
}
