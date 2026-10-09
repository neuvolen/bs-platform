package bot

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
)

// Onboarding after the cutover: a new resident who accepted the offer gets
// five messages, one a day (the script's ONBOARDING_MSGS). Day 1 comes at
// once (in the daytime), the next ones from 10:00 Almaty, never at night.
// Each message goes at most once: the state is kept in bot_meta "onb:<id>".

// OnboardingMessages are the script's texts, {имя} is the first name.
var OnboardingMessages = []string{
	"День 1️⃣\n\nПривет! Добро пожаловать в Business Surgery! 🎉\n\nЯ бот проекта. В ближайшие 5 дней буду присылать тебе всё что нужно знать чтобы получить максимум.\n\nС чего начать прямо сейчас:\n✅ Напиши первый отчёт сегодня (шаблон: /help)\n✅ Запиши кружочек о себе во вкладке РЕЗИДЕНТЫ в нашей группе. расскажи кто ты, чем занимаешься и какая твоя главная цель\n✅ Познакомься с резидентами\n\n💻 Платформа BS: app.bxclub.kz\nВаши разборы, задачи, отчёты и прогресс. Удобнее открывать с ноутбука, вход через Telegram.\n\nМы рады что ты с нами! 💪\n\nbxclub.kz",
	"День 2️⃣\n\nКак писать отчёт чтобы это работало на тебя, а не просто для галочки.\n\nОтчёт. это не контроль. Это инструмент для тебя самого.\n\nКогда ты фиксируешь:\n  Что сделал\n  Что не получилось\n  Что завтра\n\nТы начинаешь видеть паттерны. Где теряешь время. Где растёшь.\n\nШаблон: /help\n\nВажно: отчёт до 23:59. За пропуск. штраф 10 000 тг.\n\n💻 Шаг на сегодня: открой платформу app.bxclub.kz с ноутбука и войди через Telegram. Там твои разборы, задачи, отчёты и прогресс в одном месте.",
	"День 3️⃣\n\nТрекинг встреча. главный инструмент проекта.\n\nКак подготовиться:\n1. Запиши 3 главных задачи которые хочешь разобрать\n2. Принеси цифры: выручка, расходы, конверсии\n3. Будь готов говорить честно. здесь нет места для красивых историй\n\nВстречи каждые 10 дней. Следи за расписанием в группе.\n\nОпоздание = штраф 10 000 тг.\nНе пришёл на встречу, не предупредив = штраф 50 000 тг.",
	"День 4️⃣\n\nИстория одного резидента.\n\nОн зашёл в проект с выручкой 80к в месяц.\nЧерез 3 месяца. 380к.\n\nЧто изменилось? Не волшебная таблетка.\nПросто: ежедневные отчёты + честный разбор на трекинге + окружение которое требует роста.\n\nТы уже в правильном месте.\nОстальное. твои действия. 💪",
	"День 5️⃣\n\nТы уже 5 дней в системе. Это больше чем делают многие.\n\nНапоминание о главном:\n📝 Отчёт каждый день до 23:59\n🗓 Трекинг встреча каждые 10 дней\n💰 Штраф за пропуск: 10 000 тг\n\nЕсли есть вопросы или трудности. мы здесь. Пиши куратору или в группу.\n\nРады что ты с нами. Давай сделаем результат! 🚀\n\nbxclub.kz",
}

type onbState struct {
	Day  int       `json:"day"` // the next message, 1-based
	Name string    `json:"name"`
	Next time.Time `json:"next"`
}

const (
	onbPrefix  = "onb:"
	onbFrom    = 9  // day 1 is sent from 9:00
	onbDayFrom = 10 // the next ones from 10:00
	onbUntil   = 22 // never at night
)

// StartOnboarding begins the five days for a resident (again only after the
// last one ended).
func (s *Service) StartOnboarding(ctx context.Context, tg int64, name string) {
	key := onbPrefix + strconv.FormatInt(tg, 10)
	if v, _ := s.repo.GetMeta(ctx, key); v != "" {
		return
	}
	b, _ := json.Marshal(onbState{Day: 1, Name: name}) // day 1 at the next daytime tick
	if err := s.repo.SetMeta(ctx, key, string(b)); err != nil {
		log.Printf("bot onboarding %d: %v", tg, err)
		return
	}
	s.Wake()
}

func (s *Service) maybeOnboarding(ctx context.Context, now time.Time) ([]string, error) {
	a := now.In(club.Almaty)
	if a.Hour() < onbFrom || a.Hour() >= onbUntil {
		return nil, nil
	}
	all, err := s.repo.MetaPrefix(ctx, onbPrefix)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sent []string
	for _, k := range keys {
		var st onbState
		if json.Unmarshal([]byte(all[k]), &st) != nil || st.Day < 1 {
			continue
		}
		tg, err := strconv.ParseInt(strings.TrimPrefix(k, onbPrefix), 10, 64)
		if err != nil || tg == 0 {
			continue
		}
		if st.Day > len(OnboardingMessages) {
			continue // done; kept so the five days never start again
		}
		if now.Before(st.Next) || (st.Day > 1 && a.Hour() < onbDayFrom) {
			continue
		}
		if !s.once(ctx, "onbsent:"+strconv.FormatInt(tg, 10)+":"+strconv.Itoa(st.Day), 365*24*time.Hour) {
			continue // already sent (a restart in between)
		}
		txt := strings.ReplaceAll(OnboardingMessages[st.Day-1], "{имя}", firstName(st.Name))
		if err := s.SendMessage(ctx, tg, txt); err != nil {
			log.Printf("bot onboarding %s day %d: %v", st.Name, st.Day, err)
		} else {
			sent = append(sent, st.Name)
		}
		st.Day++
		tomorrow := time.Date(a.Year(), a.Month(), a.Day(), onbDayFrom, 0, 0, 0, club.Almaty).AddDate(0, 0, 1)
		st.Next = tomorrow.UTC()
		b, _ := json.Marshal(st)
		if err := s.repo.SetMeta(ctx, k, string(b)); err != nil {
			return sent, err
		}
	}
	return sent, nil
}
