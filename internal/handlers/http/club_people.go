package http

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/tgevents"
	"github.com/gin-gonic/gin"
)

// R78: the club for a resident on the platform and in the app.
//
//   - «Клуб → Участники»: every resident with their Telegram photo and the
//     profile they filled in the app («Мои данные»: what they do, how they can
//     help, Instagram, phone), their partner and the resources they offer.
//     Nothing about money, debts or fines.
//   - «Профиль → Мои данные» on the platform writes the same profile as the
//     app (the club's «Профили» sheet, through the same write queue), so both
//     always show the same.
//   - «Добавить в ресурсы клуба»: a resident's card in «Ресурсы клуба»
//     (bs_reslib), with the resident as its owner; they edit or remove it.
//   - «Мероприятия»: the events feed (sites and Telegram channels) and the
//     club's own events for residents and the team, on the platform and in
//     the app, with an .ics file for the calendar.

// ClubPeople serves the routes above.
type ClubPeople struct {
	G      *AppGateway
	Docs   *pg.PlatformRepo
	Secret []byte
	now    func() time.Time

	mu     sync.Mutex
	cache  *peopleList
	cached time.Time
	resMu  sync.Mutex // one resource write at a time
}

func NewClubPeople(g *AppGateway, docs *pg.PlatformRepo, secret []byte) *ClubPeople {
	return &ClubPeople{G: g, Docs: docs, Secret: secret, now: time.Now}
}

// Register: the platform's routes (JWT), the app's (Telegram initData) and the public .ics.
func (p *ClubPeople) Register(r *gin.Engine) {
	g := r.Group("/api/v1/platform/club")
	g.Use(middleware.AuthJWT(p.Secret))
	g.Use(middleware.RequireRole("admin", "moderator", "resident"))
	g.GET("/people", p.People)
	g.GET("/profile", p.MyProfile)
	g.PUT("/profile", p.SaveProfile)
	g.POST("/resources", p.AddResource)
	g.PUT("/resources/:id", p.EditResource)
	g.DELETE("/resources/:id", p.DeleteResource)
	g.GET("/events", p.Events)
	r.GET("/api/v1/app/events", appGzip, p.AppEvents)
	r.GET("/api/v1/public/event/:file", p.EventICS)
}

// ── people ──

// ClubPerson is one member of the club as other residents see them.
type ClubPerson struct {
	Name      string        `json:"name"`
	Tg        string        `json:"tg,omitempty"` // for the avatar (/api/v1/app/avatar/<tg>)
	Team      bool          `json:"team,omitempty"`
	Format    string        `json:"format,omitempty"`
	Since     string        `json:"since,omitempty"`
	Niche     string        `json:"niche,omitempty"`
	Bio       string        `json:"bio,omitempty"`
	Help      string        `json:"help,omitempty"`
	Instagram string        `json:"instagram,omitempty"`
	Phone     string        `json:"phone,omitempty"`
	Partner   string        `json:"partner,omitempty"`
	Resources []resLibEntry `json:"resources,omitempty"`
}

type peopleList struct {
	People   []ClubPerson
	Profiles map[string]club.BundleProfile // by chat id
}

func (p *ClubPeople) load(ctx context.Context) (*peopleList, error) {
	p.mu.Lock()
	if p.cache != nil && p.now().Sub(p.cached) < time.Minute {
		c := p.cache
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()
	if p.G == nil || p.G.Club == nil {
		return nil, errors.New("club data unavailable")
	}
	snap, err := p.G.Club.LoadBundle(ctx, p.now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	parts, _ := club.AppBundle(snap, p.now())
	residents, _ := parts["residents"].([]club.BundleResident)
	profs, _ := parts["adminProfiles"].([]club.BundleProfile)
	byID := map[string]club.BundleProfile{}
	for _, x := range profs {
		byID[strings.TrimSpace(x.ChatID)] = x
	}
	out := &peopleList{Profiles: byID}
	for _, r := range residents {
		if r.IsFired || r.Name == "" || r.Name == "Тест" || r.Name == "Тест2" {
			continue
		}
		pr := byID[r.ChatID]
		out.People = append(out.People, ClubPerson{Name: r.Name, Tg: r.ChatID, Team: r.IsAdmin, Format: r.Format, Since: r.StartDate,
			Niche: pr.Niche, Bio: pr.Bio, Help: pr.Help, Instagram: pr.Instagram, Phone: pr.Phone, Partner: strings.TrimSpace(r.Partner)})
	}
	sort.SliceStable(out.People, func(i, j int) bool { return strings.ToLower(out.People[i].Name) < strings.ToLower(out.People[j].Name) })
	p.mu.Lock()
	p.cache, p.cached = out, p.now()
	p.mu.Unlock()
	return out, nil
}

func (p *ClubPeople) forget() {
	p.mu.Lock()
	p.cache = nil
	p.mu.Unlock()
}

// People: GET /api/v1/platform/club/people
func (p *ClubPeople) People(c *gin.Context) {
	ctx := c.Request.Context()
	list, err := p.load(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "club_unavailable"})
		return
	}
	lib, _ := p.resLib(ctx)
	people := make([]ClubPerson, len(list.People))
	copy(people, list.People)
	for i := range people {
		people[i].Resources = nil
		for _, x := range lib.Items {
			if x.OwnerTg != "" && x.OwnerTg == people[i].Tg {
				people[i].Resources = append(people[i].Resources, x)
			}
		}
	}
	me := ""
	if id := platformTgID(c); id > 0 {
		me = strconv.FormatInt(id, 10)
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"me": me, "people": people})
}

// ── the resident's own profile (the app's «Мои данные») ──

type profileIn struct {
	Niche     string `json:"niche"`
	Bio       string `json:"bio"`
	Help      string `json:"help"`
	Instagram string `json:"instagram"`
	Phone     string `json:"phone"`
}

// MyProfile: GET /api/v1/platform/club/profile
func (p *ClubPeople) MyProfile(c *gin.Context) {
	id := platformTgID(c)
	if id <= 0 {
		c.JSON(http.StatusOK, gin.H{"profile": profileIn{}, "telegram": false})
		return
	}
	list, err := p.load(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "club_unavailable"})
		return
	}
	pr := list.Profiles[strconv.FormatInt(id, 10)]
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"telegram": true, "tg": strconv.FormatInt(id, 10),
		"profile": profileIn{Niche: pr.Niche, Bio: pr.Bio, Help: pr.Help, Instagram: pr.Instagram, Phone: pr.Phone}})
}

// SaveProfile: PUT /api/v1/platform/club/profile, the app's saveProfile for the caller.
func (p *ClubPeople) SaveProfile(c *gin.Context) {
	var in profileIn
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	id := platformTgID(c)
	if id <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user", "detail": "Профиль заполняется после входа через Telegram"})
		return
	}
	if p.G == nil || p.G.Writes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "writes_unavailable"})
		return
	}
	ctx := c.Request.Context()
	cid := strconv.FormatInt(id, 10)
	list, _ := p.load(ctx)
	avatar := ""
	if list != nil {
		avatar = list.Profiles[cid].Avatar
	}
	q0 := url.Values{}
	q0.Set("niche", clip(in.Niche, 200))
	q0.Set("bio", clip(in.Bio, 2000))
	q0.Set("help", clip(in.Help, 2000))
	q0.Set("instagram", clip(in.Instagram, 100))
	q0.Set("phone", clip(in.Phone, 40))
	q0.Set("avatar", avatar)
	u := &platformTgUser{ID: id}
	q := p.G.params(q0, "saveProfile", u)
	body := p.G.Writes.Do(ctx, "platform", u, "saveProfile", q, true)
	p.G.dropBundles()
	p.G.logOp(ctx, "platform", u, "saveProfile", q, body)
	var res map[string]any
	_ = json.Unmarshal(body, &res)
	if e, _ := res["error"].(string); e != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "save_failed", "detail": e})
		return
	}
	p.forget()
	c.JSON(http.StatusOK, gin.H{"ok": true, "profile": profileIn{Niche: q0.Get("niche"), Bio: q0.Get("bio"), Help: q0.Get("help"), Instagram: q0.Get("instagram"), Phone: q0.Get("phone")}})
}

// ── «Ресурсы клуба» (bs_reslib) ──

const resLibKey = "bs_reslib"

// resLibEntry is one card of «Ресурсы клуба» as the platform keeps it.
type resLibEntry struct {
	ID        string `json:"id,omitempty"`
	Cat       string `json:"cat"`
	T         string `json:"t"`
	D         string `json:"d"`
	Who       string `json:"who"`
	URL       string `json:"url"`
	Contact   string `json:"contact,omitempty"`
	OwnerTg   string `json:"ownerTg,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	At        string `json:"at,omitempty"`
}

// resLibDoc: the platform's {items:[…]}; an older copy kept a bare list.
type resLibDoc struct {
	Items []resLibEntry `json:"items"`
}

// ResidentResourcesCat: the category of the cards residents add.
const ResidentResourcesCat = "От резидентов"

// defaultResLib is the list the platform starts with while the team has not saved its own.
const defaultResLib = `[{"cat":"Услуги клуба","t":"Бади-консультация","d":"Разбор с напарником по системе бади","who":"","url":""},{"cat":"Услуги клуба","t":"Gallup тест: разбор","d":"Живой разбор талантов с трекером","who":"","url":""},{"cat":"Услуги клуба","t":"Gallup тест через ИИ","d":"Автоматический разбор отчёта","who":"","url":""},{"cat":"Услуги клуба","t":"Разбор отдела продаж","d":"Аудит воронки, скриптов и команды","who":"","url":""},{"cat":"Услуги клуба","t":"Мобилограф","d":"Съёмка контента для соцсетей","who":"Адам","url":""},{"cat":"Услуги клуба","t":"PR консультация","d":"Как выходить в медиа","who":"","url":""},{"cat":"Услуги клуба","t":"Тест на профориентацию","d":"Для себя и для команды","who":"","url":""},{"cat":"Услуги клуба","t":"Организация выступления","d":"Площадки и подготовка","who":"","url":""},{"cat":"Эксперты","t":"Маркетинг","d":"Denny, Эльдар Мардиев","who":"","url":""},{"cat":"Эксперты","t":"SMM и мобилография","d":"Адам","who":"","url":""},{"cat":"Эксперты","t":"Менеджмент и трекинг","d":"Баглан","who":"","url":""},{"cat":"Эксперты","t":"HR","d":"Курс по найму и адаптации","who":"","url":""},{"cat":"Эксперты","t":"Бухгалтерия и налоги","d":"Сопровождение для резидентов","who":"","url":""},{"cat":"Эксперты","t":"Трекеры","d":"Группа в Telegram, Димаш Okoo","who":"","url":""},{"cat":"Управление","t":"Финансовая модель","d":"Шаблон и разбор","who":"","url":""},{"cat":"Управление","t":"Система найма","d":"Воронка, тестовое, адаптация","who":"","url":""},{"cat":"Управление","t":"CRM и система продаж","d":"Настройка под ваш процесс","who":"","url":""},{"cat":"Подписки","t":"ChatGPT","d":"Общий доступ","who":"","url":""},{"cat":"Подписки","t":"Canva","d":"Дизайн без дизайнера","who":"","url":""},{"cat":"Подписки","t":"Tilda","d":"Лендинги и сайты","who":"","url":""},{"cat":"Подписки","t":"CapCut и монтаж","d":"Видео для соцсетей","who":"","url":""},{"cat":"Партнёры","t":"Ресторан R Sultan","d":"Площадка для встреч","who":"","url":""},{"cat":"Партнёры","t":"СТО, второй этаж","d":"Обслуживание авто","who":"","url":""},{"cat":"Партнёры","t":"Таксопарк","d":"Корпоративные условия","who":"","url":""},{"cat":"Партнёры","t":"Охранное агентство","d":"Объекты и сопровождение","who":"","url":""},{"cat":"Партнёры","t":"Gasyr, Airflux","d":"Партнёрские сервисы","who":"","url":""},{"cat":"Партнёры","t":"Земельный участок","d":"Под проекты резидентов","who":"","url":""},{"cat":"Партнёры","t":"Мафия и бизнес-игры","d":"Форматы для команды","who":"","url":""},{"cat":"Блогеры","t":"Рашит Ильясов","d":"Охваты и коллаборации","who":"","url":""},{"cat":"Блогеры","t":"Рауан Ахмедов","d":"Охваты и коллаборации","who":"","url":""},{"cat":"Блогеры","t":"Айбар","d":"Охваты и коллаборации","who":"","url":""},{"cat":"Блогеры","t":"Алия","d":"Охваты и коллаборации","who":"","url":""},{"cat":"Господдержка","t":"Astana Hub","d":"Программы и льготы","who":"","url":""},{"cat":"Господдержка","t":"Центр занятости","d":"Субсидии на найм","who":"","url":""},{"cat":"Господдержка","t":"Бизнес-планы","d":"Для грантов и кредитов","who":"","url":""}]`

// parseResLib reads bs_reslib in either shape; raw keeps unknown fields of
// each item (a later version of the page may add some).
func parseResLib(val string) (resLibDoc, []map[string]any) {
	var d resLibDoc
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	v := strings.TrimSpace(val)
	if strings.HasPrefix(v, "[") {
		_ = json.Unmarshal([]byte(v), &d.Items)
		_ = json.Unmarshal([]byte(v), &raw.Items)
	} else {
		_ = json.Unmarshal([]byte(v), &d)
		_ = json.Unmarshal([]byte(v), &raw)
	}
	if len(raw.Items) != len(d.Items) {
		raw.Items = nil
	}
	return d, raw.Items
}

func (p *ClubPeople) resLib(ctx context.Context) (resLibDoc, int) {
	if p.Docs == nil {
		return resLibDoc{}, -1
	}
	doc, err := p.Docs.GetDoc(ctx, "club", resLibKey)
	if err != nil || doc == nil || doc.Deleted {
		var d resLibDoc
		_ = json.Unmarshal([]byte(defaultResLib), &d.Items)
		if err == nil && doc != nil {
			return d, doc.Version
		}
		return d, 0
	}
	d, _ := parseResLib(doc.Value)
	return d, doc.Version
}

// mutateResLib changes the list under the lock, again on a version conflict.
func (p *ClubPeople) mutateResLib(ctx context.Context, by string, fn func(items []map[string]any) ([]map[string]any, error)) error {
	if p.Docs == nil {
		return errors.New("no storage")
	}
	p.resMu.Lock()
	defer p.resMu.Unlock()
	var err error
	for try := 0; try < 4; try++ {
		var items []map[string]any
		base := 0
		doc, gerr := p.Docs.GetDoc(ctx, "club", resLibKey)
		if gerr == nil && doc != nil {
			base = doc.Version
		}
		if gerr != nil || doc == nil || doc.Deleted {
			_ = json.Unmarshal([]byte(defaultResLib), &items)
		} else {
			d, raw := parseResLib(doc.Value)
			items = raw
			if items == nil { // could not keep unknown fields: the known ones
				b, _ := json.Marshal(d.Items)
				_ = json.Unmarshal(b, &items)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		items, err = fn(items)
		if err != nil {
			return err
		}
		val, _ := json.Marshal(map[string]any{"items": items})
		if _, err = p.Docs.PutDoc(ctx, "club", resLibKey, base, string(val), false, by); err == nil {
			return nil
		}
	}
	return err
}

type resourceIn struct {
	T   string `json:"t"`
	D   string `json:"d"`
	URL string `json:"url"`
}

func (in *resourceIn) clean() string {
	in.T, in.D, in.URL = clip(in.T, 120), clip(in.D, 600), clip(in.URL, 400)
	if in.URL != "" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		if strings.HasPrefix(in.URL, "@") {
			in.URL = "https://t.me/" + strings.TrimPrefix(in.URL, "@")
		} else {
			in.URL = "https://" + in.URL
		}
	}
	if in.T == "" {
		return "Нужно название ресурса"
	}
	return ""
}

// whoCalls: the caller's tg id and name (a resident from the list, the team by its name).
func (p *ClubPeople) whoCalls(c *gin.Context) (string, string, bool) {
	id := platformTgID(c)
	if id <= 0 {
		return "", "", false
	}
	name := ""
	if p.G != nil {
		if n, ok := p.G.Admins[id]; ok {
			name = n
		}
		if name == "" && p.G.Boards != nil {
			if n, active, err := p.G.Boards.ResidentByTg(c.Request.Context(), id); err == nil && active {
				name = n
			}
		}
	}
	return strconv.FormatInt(id, 10), name, true
}

func newResID(now time.Time, tg string) string {
	h := sha1.Sum([]byte(tg + "|" + now.Format(time.RFC3339Nano)))
	return "u" + hex.EncodeToString(h[:5])
}

// AddResource: POST /api/v1/platform/club/resources {t, d, url}
func (p *ClubPeople) AddResource(c *gin.Context) {
	var in resourceIn
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	if msg := in.clean(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": msg})
		return
	}
	tg, name, ok := p.whoCalls(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user", "detail": "Нужен вход через Telegram"})
		return
	}
	now := p.now()
	it := resLibEntry{ID: newResID(now, tg), Cat: ResidentResourcesCat, T: in.T, D: in.D, URL: in.URL, Who: name, OwnerTg: tg, OwnerName: name, At: now.UTC().Format(time.RFC3339)}
	err := p.mutateResLib(c.Request.Context(), "tg:"+tg, func(items []map[string]any) ([]map[string]any, error) {
		n := 0
		for _, x := range items {
			if fmt.Sprint(x["ownerTg"]) == tg {
				n++
			}
		}
		if n >= 30 {
			return nil, errors.New("Не больше 30 ресурсов от одного резидента")
		}
		b, _ := json.Marshal(it)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return append(items, m), nil
	})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "save_failed", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": it})
}

func (p *ClubPeople) ownedChange(c *gin.Context, del bool) {
	var in resourceIn
	if !del {
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
			return
		}
		if msg := in.clean(); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_params", "detail": msg})
			return
		}
	}
	tg, _, ok := p.whoCalls(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_telegram_user"})
		return
	}
	team := !isResident(c)
	id := c.Param("id")
	found := false
	err := p.mutateResLib(c.Request.Context(), "tg:"+tg, func(items []map[string]any) ([]map[string]any, error) {
		out := items[:0]
		for _, x := range items {
			if fmt.Sprint(x["id"]) == id && id != "" {
				if fmt.Sprint(x["ownerTg"]) != tg && !team {
					return nil, errors.New("forbidden")
				}
				found = true
				if del {
					continue
				}
				x["t"], x["d"], x["url"] = in.T, in.D, in.URL
			}
			out = append(out, x)
		}
		if !found {
			return nil, errors.New("not_found")
		}
		return out, nil
	})
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"ok": true})
	case err.Error() == "forbidden":
		c.JSON(http.StatusForbidden, gin.H{"error": "not_owner", "detail": "Менять можно только свои ресурсы"})
	case err.Error() == "not_found":
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "save_failed", "detail": err.Error()})
	}
}

// EditResource: PUT /api/v1/platform/club/resources/:id (the owner or the team)
func (p *ClubPeople) EditResource(c *gin.Context) { p.ownedChange(c, false) }

// DeleteResource: DELETE /api/v1/platform/club/resources/:id (the owner or the team)
func (p *ClubPeople) DeleteResource(c *gin.Context) { p.ownedChange(c, true) }

// ── «Мероприятия» ──

// ClubEvent is one event for residents: the feed's (sites, Telegram
// channels) and the club's own (Маркетинг → Мероприятия).
type ClubEvent struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Date     string   `json:"date,omitempty"` // YYYY-MM-DD
	Time     string   `json:"time,omitempty"`
	Place    string   `json:"place,omitempty"`
	URL      string   `json:"url,omitempty"`
	Post     string   `json:"post,omitempty"`
	Price    string   `json:"price,omitempty"`
	Source   string   `json:"source,omitempty"`
	Org      string   `json:"org,omitempty"`
	Desc     string   `json:"desc,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Kind     string   `json:"kind,omitempty"` // «возможность» for an opportunity
	Deadline string   `json:"deadline,omitempty"`
	Online   bool     `json:"online,omitempty"`
	Ours     bool     `json:"ours,omitempty"`
}

func eventKey(origin, title, date, tm string) string {
	h := sha1.Sum([]byte(origin + "|" + strings.TrimSpace(title) + "|" + date + "|" + tm))
	return hex.EncodeToString(h[:6])
}

func looksOnline(place string) bool {
	s := strings.ToLower(place)
	return strings.Contains(s, "онлайн") || strings.Contains(s, "online") || strings.Contains(s, "zoom")
}

// clubEvents: what is coming (today included), the club's own first on the same day.
func (p *ClubPeople) clubEvents(ctx context.Context) ([]ClubEvent, string) {
	out := []ClubEvent{}
	updated := ""
	if p.Docs == nil {
		return out, updated
	}
	loc := time.FixedZone("Almaty", 5*3600)
	today := p.now().In(loc).Format("2006-01-02")
	if d, err := p.Docs.GetDoc(ctx, "club", eventsFeedKey); err == nil && d != nil && !d.Deleted {
		var f eventsFeed
		if json.Unmarshal([]byte(d.Value), &f) == nil {
			updated = f.Updated
			for _, e := range f.Items {
				due := e.Date
				if e.Kind == tgevents.KindOpportunity && e.Deadline != "" {
					due = e.Deadline
				}
				if e.Title == "" || (due != "" && due < today) {
					continue
				}
				out = append(out, feedEvent(e))
			}
		}
	}
	if d, err := p.Docs.GetDoc(ctx, "club", "bs_mkt"); err == nil && d != nil && !d.Deleted {
		var m struct {
			Events []map[string]any `json:"events"`
		}
		if json.Unmarshal([]byte(d.Value), &m) == nil {
			for _, e := range m.Events {
				s := func(k string) string { v, _ := e[k].(string); return strings.TrimSpace(v) }
				if s("t") == "" || s("kind") == "Материал в Полезное" || (s("date") != "" && s("date") < today) {
					continue
				}
				out = append(out, ClubEvent{Key: eventKey("bs", s("t"), s("date"), s("time")), Title: s("t"), Date: s("date"), Time: s("time"),
					Place: s("place"), URL: s("url"), Desc: s("d"), Kind: s("kind"), Online: looksOnline(s("place")), Ours: true, Source: "Business Surgery"})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Date, out[j].Date
		if a == "" {
			a = "9999"
		}
		if b == "" {
			b = "9999"
		}
		if a != b {
			return a < b
		}
		if out[i].Ours != out[j].Ours {
			return out[i].Ours
		}
		return out[i].Time < out[j].Time
	})
	return out, updated
}

func feedEvent(e ai.Event) ClubEvent {
	return ClubEvent{Key: eventKey("feed", e.Title, e.Date, e.Time), Title: e.Title, Date: e.Date, Time: e.Time, Place: e.Place, URL: e.URL,
		Post: e.Post, Price: e.Price, Source: e.Source, Org: e.Org, Desc: e.Desc, Tags: e.Tags, Kind: e.Kind, Deadline: e.Deadline,
		Online: e.Online || looksOnline(e.Place)}
}

// Events: GET /api/v1/platform/club/events
func (p *ClubPeople) Events(c *gin.Context) {
	items, upd := p.clubEvents(c.Request.Context())
	c.Header("Cache-Control", "private, max-age=60")
	c.JSON(http.StatusOK, gin.H{"items": items, "updated": upd})
}

// AppEvents: GET /api/v1/app/events (residents and the team)
func (p *ClubPeople) AppEvents(c *gin.Context) {
	if p.G == nil || !p.G.libraryAllowed(c) {
		return
	}
	items, upd := p.clubEvents(c.Request.Context())
	c.Header("Cache-Control", "private, max-age=60")
	c.JSON(http.StatusOK, gin.H{"items": items, "updated": upd})
}

func icsEsc(s string) string {
	r := strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\r\n", "\\n", "\n", "\\n")
	return r.Replace(s)
}

// EventICS: GET /api/v1/public/event/<key>.ics, the event for the phone's calendar.
// Only events in the club's lists (the key is their hash), nothing else.
func (p *ClubPeople) EventICS(c *gin.Context) {
	key := strings.TrimSuffix(c.Param("file"), ".ics")
	items, _ := p.clubEvents(c.Request.Context())
	var ev *ClubEvent
	for i := range items {
		if items[i].Key == key {
			ev = &items[i]
			break
		}
	}
	if ev == nil || len(ev.Date) != 10 && len(ev.Deadline) != 10 {
		c.Status(http.StatusNotFound)
		return
	}
	date := ev.Date
	if ev.Kind == tgevents.KindOpportunity && ev.Deadline != "" {
		date = ev.Deadline
	}
	loc := time.FixedZone("Almaty", 5*3600)
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Business Surgery//Events//RU\r\nCALSCALE:GREGORIAN\r\nMETHOD:PUBLISH\r\nBEGIN:VEVENT\r\n")
	b.WriteString("UID:" + ev.Key + "@bxclub.kz\r\n")
	b.WriteString("DTSTAMP:" + p.now().UTC().Format("20060102T150405Z") + "\r\n")
	if hm, err := time.Parse("15:04", ev.Time); err == nil && ev.Kind != tgevents.KindOpportunity {
		st := time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		b.WriteString("DTSTART:" + st.UTC().Format("20060102T150405Z") + "\r\nDTEND:" + st.Add(2*time.Hour).UTC().Format("20060102T150405Z") + "\r\n")
	} else {
		b.WriteString("DTSTART;VALUE=DATE:" + day.Format("20060102") + "\r\nDTEND;VALUE=DATE:" + day.AddDate(0, 0, 1).Format("20060102") + "\r\n")
	}
	title := ev.Title
	if ev.Kind == tgevents.KindOpportunity {
		title = "Подать заявку: " + title
	}
	b.WriteString("SUMMARY:" + icsEsc(title) + "\r\n")
	if ev.Place != "" {
		b.WriteString("LOCATION:" + icsEsc(ev.Place) + "\r\n")
	}
	link := ev.URL
	if link == "" {
		link = ev.Post
	}
	desc := strings.TrimSpace(ev.Desc + "\n" + link)
	if desc != "" {
		b.WriteString("DESCRIPTION:" + icsEsc(desc) + "\r\n")
	}
	if link != "" {
		b.WriteString("URL:" + link + "\r\n")
	}
	b.WriteString("BEGIN:VALARM\r\nTRIGGER:-PT2H\r\nACTION:DISPLAY\r\nDESCRIPTION:" + icsEsc(title) + "\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	c.Header("Content-Disposition", `inline; filename="event.ics"`)
	c.Header("Cache-Control", "public, max-age=600")
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", []byte(b.String()))
}
