package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/content"
	"github.com/gin-gonic/gin"
)

// «Я резидент BS»: кнопка в боте (и в приложении) больше не будит команду.
//
//  1. Сервер сам сверяет человека со списком резидентов: Chat ID в
//     «BS - резиденты дебет», отчёты в группе с этого Telegram, username
//     и телефон из карточек CRM в колонке «Резидент».
//  2. Нашёлся по Chat ID: «вы уже резидент», без уведомлений. Нашёлся по
//     отчётам, username или телефону, а Chat ID у резидента пуст: Telegram
//     привязывается сам (запись в таблицу через очередь клуба).
//  3. Не нашёлся: тёплый ответ лиду (доступ резидента для участников клуба,
//     вход через экспресс-разбор, а пока гайды, диагностика и платформа),
//     короткий прогрев (день 0 разбор, день 1 кейс, день 3 ближайшее окно)
//     и отметка в CRM. Команде ничего не приходит: в CRM платформы есть
//     полоса «Хотели в резиденты» с кнопкой «Это резидент».
//  4. Спорный случай (имя совпадает с резидентом без Chat ID, или человек
//     нажал «Я уже в клубе»): одно сообщение команде с кнопками «Это
//     резидент» / «Это лид», только днём (10:00-20:00 Алматы); ночью оно
//     ждёт утра. «Это лид» сразу запускает прогрев, а не холодный отказ.
//
// R70 (владелец: «верни как раньше»): пункты 3 и 4 заменены прежним порядком
// в одно касание. Не нашёлся в списке или спорный: владельцу сразу (кроме
// 23:00-08:00 Алматы) сообщение «X говорит, что он резидент» с кнопками
// «✅ Подтвердить» / «❌ Отклонить». «Подтвердить»: резидент привязывается
// (строка без Chat ID) или создаётся (формат из заявки, иначе второй тап
// «Онлайн» / «Офлайн»), человеку приветствие с платформой и Mini App, личная
// ссылка в группу клуба, оферта и онбординг. Карточка CRM находится и
// отмечается сама. «Отклонить»: тёплый ответ лиду и прогрев, как раньше.
// Выбирать резидента в CRM больше не нужно; полоса в CRM осталась запасной.

// PlatformURL: the platform's address in the bot's messages.
const PlatformURL = "https://app.bxclub.kz"

// claim kinds
const (
	claimLinked = "linked" // already a resident by Chat ID
	claimLink   = "link"   // found by reports, username or phone: Telegram linked now
	claimFormer = "former" // a former resident: treated as a lead
	claimMaybe  = "maybe"  // looks like a resident: the team decides
	claimLead   = "lead"   // not in the list
)

// ClaimWho is the person who pressed «Я резидент BS».
type ClaimWho struct {
	TgID      int64
	FirstName string
	LastName  string
	Username  string
}

func (w ClaimWho) user() *platformTgUser {
	return &platformTgUser{ID: w.TgID, FirstName: w.FirstName, LastName: w.LastName, Username: w.Username}
}

// ClaimMatch: what the list says about the person.
type ClaimMatch struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"` // the resident it matched
	Why  string `json:"why,omitempty"`
}

func normUser(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
}

// phoneKey: the last 10 digits (+7 701…, 8 701…, 701… are the same number).
func phoneKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) < 10 {
		return ""
	}
	return d[len(d)-10:]
}

// MatchClaim checks the person against the residents. crm is the bs_crm
// doc (may be nil).
func MatchClaim(snap *club.Snapshot, crm map[string]any, w ClaimWho) ClaimMatch {
	if snap == nil {
		return ClaimMatch{Kind: claimLead}
	}
	active := func(r club.Resident) bool { return !r.Former && !r.Archived }
	byName := map[string]*club.Resident{}
	for i := range snap.Residents {
		r := &snap.Residents[i]
		k := club.NormName(r.Name)
		if k == "" {
			continue
		}
		if cur, ok := byName[k]; !ok || (!active(*cur) && active(*r)) {
			byName[k] = r
		}
	}
	// 1. Chat ID in the debet sheet
	former := ""
	for _, r := range snap.Residents {
		if w.TgID != 0 && r.TgID == w.TgID {
			if active(r) {
				return ClaimMatch{Kind: claimLinked, Name: r.Name, Why: "Chat ID в таблице резидентов"}
			}
			former = r.Name
		}
	}
	// 2. Evidence that ties this Telegram to a resident's name
	type ev struct{ name, why string }
	var evs []ev
	user := normUser(w.Username)
	for _, e := range snap.Reports {
		if e.Name == "" {
			continue
		}
		if w.TgID != 0 && e.TgUserID == w.TgID {
			evs = append(evs, ev{e.Name, "писал отчёты в группу клуба с этого Telegram"})
		} else if user != "" && normUser(e.Username) == user {
			evs = append(evs, ev{e.Name, "username @" + user + " в логе отчётов"})
		}
	}
	leads, _ := crm["leads"].([]any)
	myPhone := ""
	if me := findLeadByTg(leads, w.TgID); me != nil {
		myPhone = phoneKey(fmt.Sprint(me["phone"]))
	}
	for _, l := range leads {
		m, _ := l.(map[string]any)
		if m == nil || fmt.Sprint(m["col"]) != "won" {
			continue
		}
		name := fmt.Sprint(m["name"])
		switch {
		case w.TgID != 0 && leadTg(m) == w.TgID:
			evs = append(evs, ev{name, "карточка CRM в колонке «Резидент» с этим Telegram"})
		case user != "" && normUser(fmt.Sprint(m["tg"])) == user:
			evs = append(evs, ev{name, "username @" + user + " в карточке CRM «Резидент»"})
		case myPhone != "" && phoneKey(fmt.Sprint(m["phone"])) == myPhone:
			evs = append(evs, ev{name, "телефон совпадает с карточкой CRM «Резидент»"})
		}
	}
	var maybe *ClaimMatch
	for _, e := range evs {
		r := byName[club.NormName(e.name)]
		if r == nil || !active(*r) {
			continue
		}
		if r.TgID == 0 {
			return ClaimMatch{Kind: claimLink, Name: r.Name, Why: e.why}
		}
		if maybe == nil {
			maybe = &ClaimMatch{Kind: claimMaybe, Name: r.Name, Why: e.why + ", но у резидента в таблице другой Chat ID"}
		}
	}
	if maybe != nil {
		return *maybe
	}
	// 3. Only the name looks like a resident without a Chat ID (first and
	// last name, in any order): the team decides.
	mine := strings.Fields(club.NormName(w.FirstName + " " + w.LastName))
	if len(mine) >= 2 {
		for _, r := range snap.Residents {
			if !active(r) || r.TgID != 0 {
				continue
			}
			toks := map[string]bool{}
			for _, x := range strings.Fields(club.NormName(r.Name)) {
				toks[x] = true
			}
			all := true
			for _, x := range mine {
				all = all && toks[x]
			}
			if all {
				return ClaimMatch{Kind: claimMaybe, Name: r.Name, Why: "имя в Telegram совпадает с резидентом без Chat ID"}
			}
		}
	}
	// R70: a resident row of one word (the owner added «Амирхан») and that
	// word in the Telegram name: shown to the owner as the likely row.
	if len(mine) >= 1 {
		has := map[string]bool{}
		for _, x := range mine {
			has[x] = true
		}
		for _, r := range snap.Residents {
			if t := strings.Fields(club.NormName(r.Name)); active(r) && r.TgID == 0 && len(t) == 1 && has[t[0]] {
				return ClaimMatch{Kind: claimMaybe, Name: r.Name, Why: "резидент без Chat ID с таким именем"}
			}
		}
	}
	if former != "" {
		return ClaimMatch{Kind: claimFormer, Name: former, Why: "бывший резидент"}
	}
	return ClaimMatch{Kind: claimLead}
}

// ── Прогрев «хотел стать резидентом» ──

type claimStep struct {
	day  float64
	text func(first, slotLine string) string
	keys func(f *LeadFunnel) map[string]any
}

func hiName(first string) string {
	if first == "" {
		return ""
	}
	return first + ", "
}

func upFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

func platformBtn() map[string]any {
	return map[string]any{"text": "💻 Платформа BS", "url": PlatformURL}
}

// claimOffer: the first answer to «Я резидент BS» when the person is not in the list.
func claimOffer(first string, price int64) string {
	if price <= 0 {
		price = razborPrice
	}
	return upFirst(hiName(first)+"спасибо, что написали. Я сверил вас со списком резидентов Business Surgery и не нашёл в нём.") + "\n\n" +
		"Доступ резидента открыт участникам клуба. Вход в клуб у всех один: экспресс-разбор с основателями BS. Час на ваших цифрах: находим, где бизнес теряет деньги, и составляем план на 10 дней. Стоимость " + tenge(price) + ".\n\n" +
		"Пока бесплатно:\n📘 99 гайдов с чек-листами\n🔬 диагностика бизнеса за 5 минут\n💻 платформа app.bxclub.kz: вход через Telegram, экспресс-диагностика и часть библиотеки\n\n" +
		"В библиотеке клуба " + content.ScaleText() + ": резиденты работают с ней на каждом разборе.\n\n" +
		"Если вы уже в клубе, нажмите «Я уже в клубе», команда проверит вручную."
}

func claimOfferKB(f *LeadFunnel) map[string]any {
	return kb(
		row(f.appBtn("📅 Записаться на экспресс-разбор", "razbor")),
		row(f.appBtn("📘 99 гайдов", "checklists"), f.appBtn("🔬 Диагностика", "diagnostic")),
		row(platformBtn()),
		row(map[string]any{"text": "Я уже в клубе", "callback_data": "claim_recheck"}),
	)
}

// claimSteps: day 0 is the answer itself (claimOffer); then a case and a slot.
func claimSteps() []claimStep {
	return []claimStep{
		{1, func(first, _ string) string {
			return upFirst(hiName(first)+"короткая история из разборов.") + "\n\n" +
				"Даурен, оптовая торговля стройматериалами, Караганда. Выручка около 32 млн ₸ в месяц, прибыль по отчёту 2,9 млн ₸. " +
				"При этом каждый месяц 20-25 числа он занимал у брата 4-6 млн ₸ на зарплату и аренду складов.\n\n" +
				"Собрали платёжный календарь на 8 недель и увидели, что разрыв 27 числа предсказуем с точностью до 300 000 ₸. " +
				"За 3 месяца дебиторка снизилась с 11 до 6,5 млн ₸, неликвид продан на 4,2 млн ₸, на отдельном счёте резерв 3 млн ₸. У брата он больше не занимает.\n\n" +
				"Такое место есть почти в каждом бизнесе. Разбор нужен, чтобы найти ваше."
		}, func(f *LeadFunnel) map[string]any {
			return kb(row(f.appBtn("📅 Записаться на разбор", "razbor")), row(platformBtn()))
		}},
		{3, func(first, slotLine string) string {
			if slotLine != "" {
				return upFirst(hiName(first)+"ближайшее свободное время для разбора: "+slotLine+".") + "\n\n" +
					"Час с основателями BS на ваших цифрах и план на 10 дней. После разбора вы сами решаете, идти ли в клуб резидентом.\n\n" +
					"Окно держится, пока его не занял кто-то другой."
			}
			return upFirst(hiName(first)+"напоминаю про экспресс-разбор.") + "\n\n" +
				"Час с основателями BS на ваших цифрах и план на 10 дней. После разбора вы сами решаете, идти ли в клуб резидентом.\n\n" +
				"Свободные окна в приложении, выберите удобное."
		}, func(f *LeadFunnel) map[string]any {
			return kb(row(f.appBtn("📅 Выбрать время", "razbor")), row(f.appBtn("📘 Гайды", "checklists")))
		}},
	}
}

// claimWarmState: the stage of the «Я резидент» sequence and when it began.
func claimWarmState(m map[string]any) (int, time.Time, bool) {
	cl, _ := m["claim"].(map[string]any)
	if cl == nil || cl["status"] != claimLead {
		return 0, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, fmt.Sprint(cl["at"]))
	if err != nil {
		return 0, time.Time{}, false
	}
	stage := 0
	if v, ok := m["claimWarm"].(float64); ok {
		stage = int(v)
	} else if v, ok := m["claimWarm"].(int); ok {
		stage = v
	}
	return stage, at, true
}

// nearestSlotLine: «пн, 5 октября, 11:00 (Алматы), онлайн» or "".
func (f *LeadFunnel) nearestSlotLine(ctx context.Context, now time.Time) string {
	doc, err := f.readSlots(ctx)
	if err != nil {
		return ""
	}
	list, _ := doc["slots"].([]any)
	var free []slot
	for _, v := range list {
		if s, ok := readSlot(v); ok && s.free() && s.Start.After(now.Add(2*time.Hour)) && s.Start.Before(now.Add(slotsAhead)) {
			free = append(free, s)
		}
	}
	if len(free) == 0 {
		return ""
	}
	sort.Slice(free, func(i, j int) bool { return free[i].Start.Before(free[j].Start) })
	s := free[0]
	line := whenRu(s.Start) + " (Алматы)"
	if fm := strings.ToLower(s.str("format")); fm != "" {
		line += ", " + fm
	}
	return line
}

// ── Сервис ──

type claimClub interface {
	Load(ctx context.Context) (*club.Snapshot, error)
}

// ResidentClaims answers «Я резидент BS».
type ResidentClaims struct {
	f    *LeadFunnel
	club claimClub
	// Link writes the Telegram chat id into the resident's row (club writes).
	Link func(ctx context.Context, name string, tg int64) error
	// Add creates a resident row (format «Онлайн» / «Офлайн») with the chat id.
	Add func(ctx context.Context, name, format string, tg int64) error
	// Welcome sends the confirmed resident the welcome package (platform,
	// Mini App, group invite, offer) and starts the onboarding (bot/welcome.go).
	Welcome func(ctx context.Context, tg int64, name string) (string, error)
	// Edit replaces the text and buttons of the team's message (nil: not edited).
	Edit func(ctx context.Context, chatID, msgID int64, text string, kb map[string]any) error
	// Names: the team's names (PLATFORM_TEAM) for the journal.
	Names map[int64]string
	now   func() time.Time
	mu    sync.Mutex
}

func NewResidentClaims(f *LeadFunnel, c claimClub) *ResidentClaims {
	return &ResidentClaims{f: f, club: c, now: func() time.Time { return f.now() }}
}

// daytime: R70: the owner hears about claims at once, except 23:00-08:00
// Almaty; a night claim reaches the owner at 08:00.
func daytime(t time.Time) bool {
	h := t.In(almaty).Hour()
	return h >= 8 && h < 23
}

func (rc *ResidentClaims) crm(ctx context.Context) map[string]any {
	crm := map[string]any{}
	if d, err := rc.f.docs.GetDoc(ctx, "club", "bs_crm"); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &crm)
	}
	return crm
}

func (rc *ResidentClaims) send(ctx context.Context, chat int64, text string, keys map[string]any) {
	if rc.f.send == nil {
		return
	}
	if err := rc.f.send(ctx, chat, text, keys); err != nil {
		log.Printf("claim: send %d: %v", chat, err)
	}
}

func (rc *ResidentClaims) price(ctx context.Context) int64 {
	doc, err := rc.f.readSlots(ctx)
	if err != nil {
		return razborPrice
	}
	p, _ := slotsPrice(doc)
	return p
}

// adoptCard: R70: a CRM card of this person without a Telegram id (made by
// hand: same username, or the same name in «Решение» / «Резидент») gets the
// id, so the claim lands on it instead of a new card. The owner never links.
func adoptCard(crm map[string]any, w ClaimWho) bool {
	leads, _ := crm["leads"].([]any)
	if w.TgID == 0 || findLeadByTg(leads, w.TgID) != nil {
		return false
	}
	user, name := normUser(w.Username), club.NormName(w.FirstName+" "+w.LastName)
	for _, l := range leads {
		m, _ := l.(map[string]any)
		if m == nil || leadTg(m) != 0 {
			continue
		}
		col := fmt.Sprint(m["col"])
		if (user != "" && normUser(fmt.Sprint(m["tg"])) == user) ||
			(len(strings.Fields(name)) >= 2 && club.NormName(fmt.Sprint(m["name"])) == name && (col == "won" || col == "decide")) {
			m["tgId"] = float64(w.TgID)
			return true
		}
	}
	return false
}

// setClaim writes the claim onto the lead's card (creating the card quietly).
func (rc *ResidentClaims) setClaim(ctx context.Context, w ClaimWho, via string, fn func(lead, cl map[string]any)) error {
	src := "Бот: «Я резидент BS»"
	if via == "app" {
		src = "Приложение: «Я резидент BS»"
	}
	_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool { return adoptCard(crm, w) })
	if _, _, err := rc.f.ensureLeadN(ctx, w.TgID, w.FirstName, w.LastName, w.Username, src,
		"Нажал «Я резидент BS»", "", false, false); err != nil {
		return err
	}
	return rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, w.TgID)
		if lead == nil {
			return false
		}
		cl, _ := lead["claim"].(map[string]any)
		if cl == nil {
			cl = map[string]any{}
		}
		fn(lead, cl)
		lead["claim"] = cl
		return true
	})
}

// platformLine: the platform in the bot's answers, with the one-tap login.
const platformLine = "💻 Платформа BS: app.bxclub.kz, вход в один тап через Telegram (пароль не нужен)"

func (rc *ResidentClaims) residentKB() map[string]any {
	return kb(row(rc.f.appBtn("📱 Открыть приложение BS", "home")), row(platformBtn()))
}

// Claim handles «Я резидент BS» from the bot (via "bot") or the app ("app").
func (rc *ResidentClaims) Claim(ctx context.Context, w ClaimWho, via string) (ClaimMatch, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	snap, err := rc.club.Load(ctx)
	if err != nil {
		return ClaimMatch{}, err
	}
	crm := rc.crm(ctx)
	m := MatchClaim(snap, crm, w)
	leads, _ := crm["leads"].([]any)
	prev := map[string]any{}
	if l := findLeadByTg(leads, w.TgID); l != nil {
		prev, _ = l["claim"].(map[string]any)
		if prev == nil {
			prev = map[string]any{}
		}
	}
	first := upFirst(strings.TrimSpace(w.FirstName))

	if m.Kind == claimLink && rc.Link != nil {
		if err := rc.Link(ctx, m.Name, w.TgID); err != nil {
			log.Printf("claim: link %d → %q: %v", w.TgID, m.Name, err)
			m = ClaimMatch{Kind: claimMaybe, Name: m.Name, Why: m.Why + " (автопривязка не прошла: " + err.Error() + ")"}
		}
	} else if m.Kind == claimLink {
		m.Kind = claimMaybe
	}

	switch m.Kind {
	case claimLinked:
		rc.send(ctx, w.TgID, "✅ Вы уже резидент BS, всё подключено.\n\n📱 Приложение клуба: кнопка ниже.\n"+platformLine+".", rc.residentKB())
		rc.markResident(ctx, w.TgID, m.Name, "Нажал «Я резидент BS»: уже резидент («"+m.Name+"»)")
		return m, nil
	case claimLink:
		_ = rc.setClaim(ctx, w, via, func(lead, cl map[string]any) {
			cl["at"], cl["status"], cl["name"], cl["why"] = now.UTC().Format(time.RFC3339), "resident", m.Name, m.Why
			cl["by"] = "auto"
			lead["col"], lead["warmStop"] = "won", true
			addLog(lead, now, "«Я резидент BS»: привязан к резиденту «"+m.Name+"» автоматически ("+m.Why+")")
		})
		rc.send(ctx, w.TgID, "✅ Нашёл вас в списке резидентов: "+m.Name+".\n\nTelegram привязан, доступ открыт:\n📱 приложение клуба, кнопка ниже\n"+platformLine+" (доступ резидента откроется в течение часа)\n\nДобро пожаловать в Business Surgery!",
			rc.residentKB())
		return m, nil
	}
	// R70: everyone else goes to the owner, one tap decides.
	if prev["status"] == "pending" {
		rc.send(ctx, w.TgID, "Заявка уже у команды, подтвердим в ближайшее время. Пока можно открыть платформу и гайды.\n\n"+platformLine+".",
			kb(row(platformBtn()), row(rc.f.appBtn("📘 99 гайдов", "checklists"))))
		return m, nil
	}
	if err := rc.setClaim(ctx, w, via, func(lead, cl map[string]any) {
		cl["at"], cl["status"], cl["why"], cl["notified"] = now.UTC().Format(time.RFC3339), "pending", m.Why, ""
		if cl["why"] == "" {
			cl["why"] = "нет в списке резидентов"
		}
		if m.Name != "" && m.Kind == claimMaybe {
			cl["name"] = m.Name
		} else {
			delete(cl, "name")
		}
		delete(cl, "by")
		lead["warmStop"] = true // пока владелец не решил
		addLog(lead, now, "«Я резидент BS»: заявка владельцу на подтверждение")
	}); err != nil {
		return m, err
	}
	rc.send(ctx, w.TgID, upFirst(hiName(first)+"спасибо! Передал заявку команде, подтвердим в ближайшее время. После подтверждения пришлю доступы: чат резидентов, приложение и платформу.")+
		"\n\n"+platformLine+".",
		kb(row(platformBtn()), row(rc.f.appBtn("📘 99 гайдов", "checklists"), rc.f.appBtn("🔬 Диагностика", "diagnostic"))))
	if daytime(now) {
		rc.notifyOne(ctx, w.TgID, false)
	}
	return m, nil
}

// startWarm: the offer now, the case and the slot later; the card is marked
// first (the answer goes out only after the CRM has the claim).
func (rc *ResidentClaims) startWarm(ctx context.Context, w ClaimWho, via string, m ClaimMatch, now time.Time, by string) {
	_ = rc.setClaim(ctx, w, via, func(lead, cl map[string]any) {
		cl["at"], cl["status"] = now.UTC().Format(time.RFC3339), claimLead
		if m.Kind == claimFormer {
			cl["name"], cl["why"] = m.Name, m.Why
		} else {
			delete(cl, "name")
			cl["why"] = "нет в списке резидентов"
		}
		if by != "" {
			cl["by"] = by
		}
		delete(lead, "warmStop")
		lead["claimWarm"] = 0
		lead["hot"] = true
		lead["warmAt"] = now.UTC().Format(time.RFC3339)
		txt := "«Я резидент BS»: в списке резидентов нет, бот позвал на экспресс-разбор (прогрев 3 касания)"
		if m.Kind == claimFormer {
			txt = "«Я резидент BS»: бывший резидент «" + m.Name + "», бот позвал на экспресс-разбор"
		}
		if by != "" {
			txt = "Команда отметила: лид. Бот позвал на экспресс-разбор (прогрев 3 касания)"
		}
		addLog(lead, now, txt)
	})
	rc.send(ctx, w.TgID, claimOffer(upFirst(strings.TrimSpace(w.FirstName)), rc.price(ctx)), claimOfferKB(rc.f))
}

// markResident: an existing lead card of a resident goes to «Резидент».
func (rc *ResidentClaims) markResident(ctx context.Context, tg int64, name, text string) {
	now := rc.now()
	_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, tg)
		if lead == nil {
			return false
		}
		cl, _ := lead["claim"].(map[string]any)
		if cl == nil {
			cl = map[string]any{}
		}
		if cl["status"] == "resident" && lead["col"] == "won" {
			return false
		}
		cl["at"], cl["status"], cl["name"] = now.UTC().Format(time.RFC3339), "resident", name
		lead["claim"], lead["col"], lead["warmStop"] = cl, "won", true
		addLog(lead, now, text)
		return true
	})
}

// Recheck: the person pressed «Я уже в клубе» under the offer.
func (rc *ResidentClaims) Recheck(ctx context.Context, w ClaimWho) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	snap, err := rc.club.Load(ctx)
	if err != nil {
		return err
	}
	m := MatchClaim(snap, rc.crm(ctx), w)
	if m.Kind == claimLinked {
		rc.send(ctx, w.TgID, "✅ Вы уже резидент BS, всё подключено.\n\n"+platformLine+".", rc.residentKB())
		return nil
	}
	name, why := "", "сам сообщил, что уже в клубе"
	if m.Kind == claimMaybe || m.Kind == claimLink || m.Kind == claimFormer {
		name, why = m.Name, why+"; "+m.Why
	}
	already := false
	_ = rc.setClaim(ctx, w, "bot", func(lead, cl map[string]any) {
		if cl["status"] == "pending" {
			already = true
			return
		}
		cl["status"], cl["name"], cl["why"], cl["notified"] = "pending", name, why, ""
		if name == "" {
			delete(cl, "name")
		}
		delete(cl, "by")
		cl["at"] = now.UTC().Format(time.RFC3339)
		lead["warmStop"] = true // пока команда не решила
		addLog(lead, now, "Нажал «Я уже в клубе»: заявка владельцу на подтверждение")
	})
	if already {
		rc.send(ctx, w.TgID, "Заявка уже у команды, подтвердим в ближайшее время.", nil)
		return nil
	}
	rc.send(ctx, w.TgID, "Принято. Передал заявку команде: подтвердим и пришлём доступы.", nil)
	if daytime(now) {
		rc.notifyOne(ctx, w.TgID, false)
	}
	return nil
}

// claimKB: the owner's buttons under «X говорит, что он резидент».
func claimKB(tg int64, name string) map[string]any {
	id := strconv.FormatInt(tg, 10)
	rows := [][]map[string]any{row(
		map[string]any{"text": "✅ Подтвердить", "callback_data": "rcl_ok_" + id},
		map[string]any{"text": "❌ Отклонить", "callback_data": "rcl_no_" + id})}
	if name != "" {
		rows = append(rows, row(map[string]any{"text": "Это не «" + clip(name, 24) + "»: новый резидент", "callback_data": "rcl_new_" + id}))
	}
	return map[string]any{"inline_keyboard": rows}
}

// notifyOne sends the owner one message about a pending claim. again: send
// even if it was sent before (R70: the claim made before the fix).
func (rc *ResidentClaims) notifyOne(ctx context.Context, tg int64, again bool) {
	if rc.f.send == nil {
		return
	}
	now := rc.now()
	var lead map[string]any
	_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		l := findLeadByTg(leads, tg)
		if l == nil {
			return false
		}
		cl, _ := l["claim"].(map[string]any)
		if cl == nil || cl["status"] != "pending" {
			return false
		}
		if n, _ := cl["notified"].(string); n != "" && !again {
			return false
		}
		cl["notified"] = now.UTC().Format(time.RFC3339)
		lead = l
		return true
	})
	if lead == nil {
		return
	}
	cl, _ := lead["claim"].(map[string]any)
	name, _ := cl["name"].(string)
	who := fmt.Sprint(lead["name"])
	if t, _ := lead["tg"].(string); t != "" {
		who += " " + t
	}
	text := fmt.Sprintf("🙋 %s говорит, что он резидент\n🆔 %d\n", who, tg)
	if p, _ := lead["phone"].(string); strings.TrimSpace(p) != "" {
		text += "📞 " + p + "\n"
	}
	if name != "" {
		text += "Похоже на резидента «" + name + "»: " + fmt.Sprint(cl["why"]) + "\n"
	} else {
		text += "В списке резидентов нет: при подтверждении бот добавит его сам\n"
	}
	text += "\nПодтвердить: доступ, ссылка в группу клуба и онбординг уйдут сразу."
	for _, a := range rc.f.admins {
		rc.send(ctx, a, text, claimKB(tg, name))
	}
}

// NotifyPending: the claims that came at night reach the owner at 08:00.
func (rc *ResidentClaims) NotifyPending(ctx context.Context) int {
	if !daytime(rc.now()) {
		return 0
	}
	var tgs []int64
	crm := rc.crm(ctx)
	leads, _ := crm["leads"].([]any)
	for _, l := range leads {
		m, _ := l.(map[string]any)
		if m == nil {
			continue
		}
		cl, _ := m["claim"].(map[string]any)
		if n, _ := cl["notified"].(string); cl != nil && cl["status"] == "pending" && n == "" {
			tgs = append(tgs, leadTg(m))
		}
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, tg := range tgs {
		rc.notifyOne(ctx, tg, false)
	}
	return len(tgs)
}

// ResendRecent: R70, once: a claim of the last day that the owner never
// decided (it went the CRM way: «лид, прогрев» or waiting there) is put back
// to «ждёт решения» and the owner gets the confirm message again. Nothing is
// confirmed by itself. Returns the names sent.
func (rc *ResidentClaims) ResendRecent(ctx context.Context, since time.Duration) []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	var tgs []int64
	var names []string
	_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		changed := false
		for _, l := range leads {
			m, _ := l.(map[string]any)
			if m == nil || leadTg(m) == 0 {
				continue
			}
			cl, _ := m["claim"].(map[string]any)
			if cl == nil || cl["r70"] == true {
				continue
			}
			st, by := fmt.Sprint(cl["status"]), fmt.Sprint(cl["by"])
			at, err := time.Parse(time.RFC3339, fmt.Sprint(cl["at"]))
			if err != nil || now.Sub(at) > since || (st != "pending" && st != claimLead) || (cl["by"] != nil && by != "") {
				continue
			}
			cl["r70"], cl["status"], cl["notified"] = true, "pending", ""
			m["warmStop"] = true
			addLog(m, now, "Заявка «Я резидент BS» снова отправлена владельцу на подтверждение (R70)")
			tgs = append(tgs, leadTg(m))
			names = append(names, fmt.Sprint(m["name"]))
			changed = true
		}
		return changed
	})
	for _, tg := range tgs {
		rc.notifyOne(ctx, tg, true)
	}
	return names
}

// Loop sends the morning's pending claims (checks every 15 minutes).
func (rc *ResidentClaims) Loop(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if n := rc.NotifyPending(c); n > 0 {
			log.Printf("claims: %d pending sent to the owner", n)
		}
		cancel()
	}
}

var errClaimNoName = errors.New("не выбран резидент")

// errNeedFormat: a new resident and the claim does not say online or offline.
var errNeedFormat = errors.New("нужен формат")

// claimFormat: «Онлайн» / «Офлайн» from what the person told before (the card's
// format, the booked разбор's slot), "" if nothing.
func (rc *ResidentClaims) claimFormat(ctx context.Context, lead map[string]any) string {
	pick := func(s string) string {
		s = strings.ToLower(s)
		switch {
		case strings.Contains(s, "онлайн") || strings.Contains(s, "online"):
			return "Онлайн"
		case strings.Contains(s, "офлайн") || strings.Contains(s, "offline") || strings.Contains(s, "очно"):
			return "Офлайн"
		}
		return ""
	}
	if lead == nil {
		return ""
	}
	for _, k := range []string{"format", "fmt"} {
		if f := pick(fmt.Sprint(lead[k])); f != "" {
			return f
		}
	}
	if id := fmt.Sprint(lead["razborSlot"]); id != "" && id != "<nil>" {
		if doc, err := rc.f.readSlots(ctx); err == nil {
			list, _ := doc["slots"].([]any)
			for _, v := range list {
				if s, ok := readSlot(v); ok && s.ID == id {
					return pick(s.str("format"))
				}
			}
		}
	}
	return ""
}

// Confirm is the owner's «Подтвердить»: the resident row is found and linked,
// or created (format: the given one, from the claim, or errNeedFormat); then
// the welcome package and the CRM. fresh: create a new row even if the claim
// looked like a resident without a Chat ID («Это не X: новый резидент»).
func (rc *ResidentClaims) Confirm(ctx context.Context, tg int64, format, by string, fresh bool) (string, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	snap, err := rc.club.Load(ctx)
	if err != nil {
		return "", err
	}
	crm := rc.crm(ctx)
	leads, _ := crm["leads"].([]any)
	lead := findLeadByTg(leads, tg)
	cl := map[string]any{}
	if lead != nil {
		if c, _ := lead["claim"].(map[string]any); c != nil {
			cl = c
		}
	}
	if cl["status"] == "resident" && cl["welcome"] != nil {
		return "Уже подтверждён: " + fmt.Sprint(cl["name"]), nil
	}
	// 1. the row: by Chat ID, the claim's candidate, or a new one
	name, how := "", ""
	for _, r := range snap.Residents {
		if r.TgID == tg && !r.Archived && !r.Former {
			name, how = r.Name, "уже в списке"
			break
		}
	}
	if cand, _ := cl["name"].(string); name == "" && cand != "" && !fresh {
		for _, r := range snap.Residents {
			if club.NormName(r.Name) == club.NormName(cand) && !r.Archived && !r.Former && r.TgID == 0 {
				if rc.Link == nil {
					return "", errors.New("запись в таблицу клуба не настроена")
				}
				if err := rc.Link(ctx, r.Name, tg); err != nil {
					return "", err
				}
				name, how = r.Name, "Telegram привязан к «"+r.Name+"»"
				break
			}
		}
	}
	if name == "" {
		name = "Без имени"
		if lead != nil {
			name = strings.TrimSpace(fmt.Sprint(lead["name"]))
		}
		if name == "" || name == "Без имени" || name == "<nil>" {
			return "", errors.New("у заявки нет имени: добавьте резидента на платформе")
		}
		parts := strings.Fields(name)
		for i := range parts {
			parts[i] = upFirst(parts[i])
		}
		name = strings.Join(parts, " ")
		// a row with this name and another Chat ID would be a duplicate
		for _, r := range snap.Residents {
			if club.NormName(r.Name) == club.NormName(name) && !r.Archived && r.TgID != 0 && r.TgID != tg {
				name = name + " (" + strconv.FormatInt(tg, 10) + ")"
				break
			}
		}
		if format == "" {
			format = rc.claimFormat(ctx, lead)
		}
		if format == "" {
			return "", errNeedFormat
		}
		if rc.Add == nil {
			return "", errors.New("запись в таблицу клуба не настроена")
		}
		if err := rc.Add(ctx, name, format, tg); err != nil {
			return "", err
		}
		how = "новый резидент «" + name + "», " + strings.ToLower(format)
	}
	// 2. the CRM card, found and marked by itself (before the welcome: the
	// card says «Резидент» by the time the person reads it)
	w := ClaimWho{TgID: tg}
	if lead != nil {
		parts := strings.Fields(fmt.Sprint(lead["name"]))
		if len(parts) > 0 {
			w.FirstName, w.LastName = parts[0], strings.Join(parts[1:], " ")
		}
		w.Username = normUser(fmt.Sprint(lead["tg"]))
	} else {
		w.FirstName = name
	}
	_ = rc.setClaim(ctx, w, "bot", func(l, c map[string]any) {
		c["status"], c["name"], c["by"], c["at"] = "resident", name, by, now.UTC().Format(time.RFC3339)
		c["welcome"] = now.UTC().Format(time.RFC3339)
		l["col"], l["warmStop"] = "won", true
		addLog(l, now, "Владелец ("+by+") подтвердил: резидент («"+how+"»). Приветствие, ссылка в группу и онбординг отправлены")
	})
	// 3. the welcome
	group := ""
	if rc.Welcome != nil {
		g, err := rc.Welcome(ctx, tg, name)
		if err != nil {
			log.Printf("claim: welcome %d: %v", tg, err)
			_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
				leads, _ := crm["leads"].([]any)
				if l := findLeadByTg(leads, tg); l != nil {
					if c, _ := l["claim"].(map[string]any); c != nil {
						delete(c, "welcome") // a second «Подтвердить» sends it again
						return true
					}
				}
				return false
			})
			return "", fmt.Errorf("резидент записан (%s), но приветствие не ушло: %v", how, err)
		}
		group = g
	} else {
		rc.send(ctx, tg, "✅ Команда подтвердила: вы резидент BS ("+name+").\n\nДоступ открыт:\n📱 приложение клуба, кнопка ниже\n"+platformLine, rc.residentKB())
	}
	log.Printf("claim: %d confirmed by %s: %s, welcome sent (group link %t)", tg, by, how, group != "")
	return "✅ " + name + ": резидент (" + how + "). Приветствие с платформой, ссылка в группу и онбординг отправлены", nil
}

// Resolve is the team's decision from the platform's CRM strip: action
// "resident" links the Telegram to name (or, without a name, confirms as the
// bot's «Подтвердить» does), "lead" starts the warming.
func (rc *ResidentClaims) Resolve(ctx context.Context, tg int64, action, name, by string) (string, error) {
	if action == "resident" && strings.TrimSpace(name) == "" {
		return rc.Confirm(ctx, tg, "", by, false)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	crm := rc.crm(ctx)
	leads, _ := crm["leads"].([]any)
	lead := findLeadByTg(leads, tg)
	w := ClaimWho{TgID: tg}
	if lead != nil {
		parts := strings.Fields(fmt.Sprint(lead["name"]))
		if len(parts) > 0 && parts[0] != "Без" {
			w.FirstName = parts[0]
			w.LastName = strings.Join(parts[1:], " ")
		}
		w.Username = normUser(fmt.Sprint(lead["tg"]))
	}
	switch action {
	case "resident":
		if rc.Link == nil {
			return "", errors.New("запись в таблицу клуба не настроена")
		}
		if err := rc.Link(ctx, name, tg); err != nil {
			return "", err
		}
		welcomed := false
		if rc.Welcome != nil {
			if _, err := rc.Welcome(ctx, tg, name); err != nil {
				log.Printf("claim: welcome %d: %v", tg, err)
			} else {
				welcomed = true
			}
		}
		if !welcomed {
			rc.send(ctx, tg, "✅ Команда подтвердила: вы резидент BS ("+name+").\n\nTelegram привязан, доступ открыт:\n📱 приложение клуба, кнопка ниже\n"+platformLine+" (доступ резидента откроется в течение часа)",
				rc.residentKB())
		}
		if lead != nil {
			_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
				leads, _ := crm["leads"].([]any)
				l := findLeadByTg(leads, tg)
				if l == nil {
					return false
				}
				cl, _ := l["claim"].(map[string]any)
				if cl == nil {
					cl = map[string]any{}
				}
				cl["status"], cl["name"], cl["by"], cl["at"] = "resident", name, by, now.UTC().Format(time.RFC3339)
				if welcomed {
					cl["welcome"] = now.UTC().Format(time.RFC3339)
				}
				l["claim"], l["col"], l["warmStop"] = cl, "won", true
				addLog(l, now, "Команда ("+by+"): это резидент «"+name+"», Telegram привязан")
				return true
			})
		}
		return "Привязан к «" + name + "»", nil
	case "lead":
		if lead != nil {
			if cl, _ := lead["claim"].(map[string]any); cl != nil && cl["status"] == claimLead && cl["by"] != nil {
				return "Уже в прогреве", nil
			}
		}
		rc.startWarm(ctx, w, "bot", ClaimMatch{Kind: claimLead}, now, by)
		return "Лид: бот позвал на экспресс-разбор", nil
	}
	return "", fmt.Errorf("неизвестное действие %q", action)
}

// claimCallbacks: the owner's buttons. rcl_ok_ «Подтвердить», rcl_new_ «новый
// резидент», rcl_on_ / rcl_off_ the format (second tap), rcl_no_ «Отклонить»;
// the older rcl_res_ / rcl_lead_ and the script's approve_res_ / reject_res_.
var claimCallbacks = []struct{ prefix, action string }{
	{"rcl_ok_", "ok"}, {"rcl_new_", "new"}, {"rcl_on_", "on"}, {"rcl_off_", "off"}, {"rcl_no_", "lead"},
	{"rcl_res_", "ok"}, {"rcl_lead_", "lead"}, {"approve_res_", "ok"}, {"reject_res_", "lead"},
}

// TeamCallback: the owner's buttons under the claim message.
func (rc *ResidentClaims) TeamCallback(ctx context.Context, cb bot.CallbackUpdate) (string, bool) {
	action, rest := "", ""
	for _, c := range claimCallbacks {
		if strings.HasPrefix(cb.Data, c.prefix) {
			action, rest = c.action, strings.TrimPrefix(cb.Data, c.prefix)
			break
		}
	}
	if action == "" {
		return "", false
	}
	tg, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || tg <= 0 {
		return "Не понял, о ком речь", true
	}
	by := "Telegram"
	if n, ok := rc.Names[cb.FromID]; ok && n != "" {
		by = n
	}
	edit := func(text string, keys map[string]any) {
		if rc.Edit != nil && cb.MessageID != 0 {
			if err := rc.Edit(ctx, cb.ChatID, cb.MessageID, text, keys); err != nil {
				log.Printf("claim: edit: %v", err)
			}
		}
	}
	var res string
	switch action {
	case "lead":
		res, err = rc.Resolve(ctx, tg, "lead", "", by)
	default:
		format := map[string]string{"on": "Онлайн", "off": "Офлайн"}[action]
		fresh := action == "new" || (format != "" && rc.wantsFresh(ctx, tg))
		if action == "new" {
			rc.markFresh(ctx, tg)
		}
		res, err = rc.Confirm(ctx, tg, format, by, fresh)
		if errors.Is(err, errNeedFormat) {
			id := strconv.FormatInt(tg, 10)
			edit(fmt.Sprintf("🆔 %d: новый резидент. Формат участия?", tg), kb(row(
				map[string]any{"text": "💻 Онлайн", "callback_data": "rcl_on_" + id},
				map[string]any{"text": "🏢 Офлайн", "callback_data": "rcl_off_" + id})))
			return "Выберите формат", true
		}
	}
	if err != nil {
		return clip("Не получилось: "+err.Error(), 190), true
	}
	edit(fmt.Sprintf("🆔 %d: %s (%s)", tg, res, by), nil)
	return clip(res, 190), true
}

// markFresh / wantsFresh: «Это не X: новый резидент» survives the format tap.
func (rc *ResidentClaims) markFresh(ctx context.Context, tg int64) {
	_ = rc.f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		if l := findLeadByTg(leads, tg); l != nil {
			if cl, _ := l["claim"].(map[string]any); cl != nil {
				cl["fresh"] = true
				return true
			}
		}
		return false
	})
}

func (rc *ResidentClaims) wantsFresh(ctx context.Context, tg int64) bool {
	leads, _ := rc.crm(ctx)["leads"].([]any)
	if l := findLeadByTg(leads, tg); l != nil {
		cl, _ := l["claim"].(map[string]any)
		return cl != nil && cl["fresh"] == true
	}
	return false
}

// LeadCallback: the person's own buttons: «Я резидент BS» and «Я уже в клубе».
func (rc *ResidentClaims) LeadCallback(ctx context.Context, cb bot.CallbackUpdate) bool {
	w := ClaimWho{TgID: cb.FromID, FirstName: cb.FirstName, LastName: cb.LastName, Username: cb.Username}
	switch cb.Data {
	case "i_am_resident":
		if _, err := rc.Claim(ctx, w, "bot"); err != nil {
			log.Printf("claim %d: %v", cb.FromID, err)
			return false
		}
		return true
	case "claim_recheck":
		if err := rc.Recheck(ctx, w); err != nil {
			log.Printf("claim recheck %d: %v", cb.FromID, err)
			return false
		}
		return true
	}
	return false
}

// ── HTTP ──

// ClaimFromScript: POST /api/v1/bot/claim, signed by the Apps Script with the
// bot token (X-BS-Signature = hex HMAC-SHA256 of the raw body). Body
// {ts, chatId, name, username, via} or {ts, chatId, action:"lead"}. The script's own «Я резидент» (an old
// button or a direct app call) comes here first and only falls back to the
// old admin request when the server does not answer.
func (g *AppGateway) ClaimFromScript(c *gin.Context) {
	if g.Claims == nil || g.token == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	m := hmac.New(sha256.New, []byte(g.token))
	m.Write(body)
	got, _ := hex.DecodeString(strings.ToLower(strings.TrimSpace(c.GetHeader("X-BS-Signature"))))
	if !hmac.Equal(m.Sum(nil), got) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return
	}
	var req struct {
		TS       int64  `json:"ts"`
		ChatID   any    `json:"chatId"`
		Name     string `json:"name"`
		Username string `json:"username"`
		Via      string `json:"via"`
		Action   string `json:"action"` // "lead": the old «Отклонить» button, warm instead of a cold no
	}
	if json.Unmarshal(body, &req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	if d := time.Since(time.Unix(req.TS, 0)); d > 10*time.Minute || d < -5*time.Minute {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return
	}
	tg, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(req.ChatID)), 10, 64)
	if tg <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no chatId"})
		return
	}
	if req.Action == "lead" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := g.Claims.Resolve(ctx, tg, "lead", "", "кнопка «Отклонить»")
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "kind": claimLead, "result": res})
		return
	}
	first, last, _ := strings.Cut(strings.TrimSpace(req.Name), " ")
	via := "bot"
	if req.Via == "app" {
		via = "app"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := g.Claims.Claim(ctx, ClaimWho{TgID: tg, FirstName: first, LastName: last, Username: normUser(req.Username)}, via)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "kind": res.Kind, "alreadyResident": res.Kind == claimLinked || res.Kind == claimLink})
}

// claimFromApp: the app's requestResident goes to the server's flow.
func (g *AppGateway) claimFromApp(c *gin.Context, u *platformTgUser) bool {
	if g.Claims == nil {
		return false
	}
	if _, admin := g.Admins[u.ID]; admin {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := g.Claims.Claim(ctx, ClaimWho{TgID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username}, "app")
	if err != nil {
		log.Printf("claim (app) %d: %v", u.ID, err)
		return false
	}
	switch res.Kind {
	case claimLinked, claimLink:
		c.JSON(http.StatusOK, gin.H{"alreadyResident": true, "kind": res.Kind})
	case claimMaybe:
		c.JSON(http.StatusOK, gin.H{"alreadyPending": true, "kind": res.Kind})
	default:
		c.JSON(http.StatusOK, gin.H{"ok": true, "kind": res.Kind})
	}
	return true
}

// LinkResidentTg writes a Telegram chat id into a resident's row: the same
// club write as the team's resident editor (server at once, sheet queued),
// done in the name of the owner (as the script expects a team member).
func (g *AppGateway) LinkResidentTg(ctx context.Context, clubSrc claimClub, owner int64, name string, tg int64) error {
	if g.Writes == nil {
		return errors.New("запись в клуб на сервере не настроена")
	}
	snap, err := clubSrc.Load(ctx)
	if err != nil {
		return err
	}
	var list []club.Resident
	for _, r := range snap.Residents {
		if !r.Archived {
			list = append(list, r)
		}
	}
	action, params, _, err := ResidentEditParams(list, name, "chatId", strconv.FormatInt(tg, 10))
	if err != nil {
		return err
	}
	in := url.Values{}
	for k, v := range params {
		in.Set(k, v)
	}
	u := &platformTgUser{ID: owner, FirstName: "Бот: «Я резидент BS»"}
	q := g.params(in, action, u)
	body := g.Writes.Do(ctx, "bot", u, action, q, true)
	lq := url.Values{}
	for k, v := range q {
		lq[k] = v
	}
	lq.Set("edit", "chatId")
	lq.Set("claim", strconv.FormatInt(tg, 10))
	g.logOp(ctx, "bot", u, action, lq, body)
	g.dropBundles()
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if e, _ := out["error"].(string); e != "" {
		return errors.New(e)
	}
	log.Printf("claim: resident %q linked to Telegram %d", name, tg)
	return nil
}

// AddResidentTg: R70: the owner confirmed someone who is not in the list: a
// new resident row with the chat id, the same addResident as the platform's
// «Новый резидент» (tariff empty: the payment fills it), in the owner's name.
func (g *AppGateway) AddResidentTg(ctx context.Context, owner int64, name, format string, tg int64) error {
	if g.Writes == nil {
		return errors.New("запись в клуб на сервере не настроена")
	}
	in := url.Values{}
	in.Set("name", name)
	in.Set("format", format)
	in.Set("visits", "3")
	in.Set("source", "Бот: «Я резидент BS»")
	in.Set("residentChatId", strconv.FormatInt(tg, 10))
	u := &platformTgUser{ID: owner, FirstName: "Бот: «Я резидент BS»"}
	q := g.params(in, "addResident", u)
	body := g.Writes.Do(ctx, "bot", u, "addResident", q, true)
	lq := url.Values{}
	for k, v := range q {
		lq[k] = v
	}
	lq.Set("claim", strconv.FormatInt(tg, 10))
	g.logOp(ctx, "bot", u, "addResident", lq, body)
	g.dropBundles()
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if e, _ := out["error"].(string); e != "" {
		return errors.New(e)
	}
	log.Printf("claim: new resident %q (%s) with Telegram %d", name, format, tg)
	return nil
}

// ResolveClaim godoc
// @Summary  The team's decision on «Я резидент BS»: {tg, action: resident|lead, name}
// @Description  resident: links the Telegram to the resident «name» (Chat ID in «BS - резиденты дебет», the person gets the welcome). lead: the bot invites to экспресс-разбор and warms up (3 touches).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/claim [post]
func (h *ClubActionHandler) ResolveClaim(c *gin.Context) {
	var req struct {
		TG     any    `json:"tg"`
		Action string `json:"action"`
		Name   string `json:"name"`
		Format string `json:"format"` // R70: online | offline for a new resident
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params"})
		return
	}
	tg, _ := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(req.TG)), "tg"), 10, 64)
	if tg <= 0 || (req.Action != "resident" && req.Action != "lead") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": "нужны tg и action: resident или lead"})
		return
	}
	if h.gw == nil || h.gw.Claims == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable", "detail": "бот на сервере не подключён"})
		return
	}
	by := "платформа"
	if id := platformTgID(c); id > 0 {
		if n, ok := h.gw.Admins[id]; ok && n != "" {
			by = n
		}
	}
	var res string
	var err error
	if req.Action == "resident" && strings.TrimSpace(req.Name) == "" {
		// R70: no resident picked: confirmed as the bot's «Подтвердить» (found or created)
		res, err = h.gw.Claims.Confirm(c.Request.Context(), tg, map[string]string{"online": "Онлайн", "offline": "Офлайн"}[req.Format], by, false)
		if errors.Is(err, errNeedFormat) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "need_format", "detail": "Новый резидент: выберите формат"})
			return
		}
	} else {
		res, err = h.gw.Claims.Resolve(c.Request.Context(), tg, req.Action, strings.TrimSpace(req.Name), by)
	}
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "claim", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}
