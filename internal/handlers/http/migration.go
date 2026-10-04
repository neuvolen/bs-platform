package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// Moving the app's bundle to the server, the way the bot's features moved
// (internal/bot/rollout.go): nobody has to press anything.
//
//	sheet   the script answers the app; nothing is compared
//	shadow  the script answers; once a day the server builds the same bundle
//	        for a few people (team, resident, lead) and compares it with the
//	        script's, field by field
//	server  the server answers (app_bundle.go); the script only for what it
//	        still keeps alone, or when the server fails
//
// It starts in shadow. After BundleStreak days in a row without a single
// difference it goes to server by itself. The owner can set any stage from
// the platform; "sheet" stops the comparisons, "shadow" lets the server move
// on by itself again. The result is kept in the club doc bs_migration_status
// for the platform; the owner gets a message only on a day with differences
// (or with writes stuck on the way to the sheet), at most once a day.
const (
	StageSheet   = "sheet"
	StageShadow  = "shadow"
	StageServer  = "server"
	BundleStreak = 5

	MigrationStatusKey = "bs_migration_status"

	metaBundleStage    = "bundle_stage"
	metaBundleStageBy  = "bundle_stage_by"
	metaBundleStageAt  = "bundle_stage_at"
	metaBundleNotified = "bundle_notified_day"

	checkFromHour  = 10 // the comparison waits for the morning's data
	checkGiveUpAt  = 21 // after this, compare even if the data is not in step
	maxShown       = 30 // differences kept in the status doc
	stuckWriteTime = 2 * time.Hour
)

// MetaStore keeps small server settings (bot_meta).
type MetaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// MigrationRepo: what the comparison reads and keeps.
type MigrationRepo interface {
	LastImportAt(ctx context.Context) (*time.Time, error)
	LastWriteAt(ctx context.Context) (*time.Time, error)
	OpenWrites(ctx context.Context) ([]pg.ClubWrite, error)
	WriteStats(ctx context.Context) (pg.WriteStats, error)
	SaveBundleCheck(ctx context.Context, day time.Time, ok bool, result any) error
	BundleChecks(ctx context.Context, n int) ([]pg.BundleCheck, error)
	ClubTgIDs(ctx context.Context) ([]int64, error)
}

// SampleUser is one person whose bundle is compared.
type SampleUser struct {
	Role string `json:"role"`
	TgID int64  `json:"tg"`
}

type BundleMigration struct {
	gw   *AppGateway
	repo MigrationRepo
	meta MetaStore
	docs interface {
		PutServerDoc(ctx context.Context, key, value string) error
	}
	writes *ClubWrites

	// Notify sends the owner a message.
	Notify func(ctx context.Context, text string)
	// Sample overrides who is compared (tests, BUNDLE_SAMPLE).
	Sample []SampleUser
	// Owner is compared as the team; Admins pick a resident who is not team.
	Owner int64

	now      func() time.Time
	mu       sync.Mutex
	stage    string
	stageAt  time.Time
	lastTry  time.Time
	publishQ chan struct{}
}

func NewBundleMigration(gw *AppGateway, repo MigrationRepo, meta MetaStore, docs interface {
	PutServerDoc(ctx context.Context, key, value string) error
}, writes *ClubWrites) *BundleMigration {
	m := &BundleMigration{gw: gw, repo: repo, meta: meta, docs: docs, writes: writes, now: time.Now, publishQ: make(chan struct{}, 1)}
	gw.Stage = m.Stage
	if writes != nil {
		writes.OnChange = m.publishSoon
	}
	return m
}

// Stage: who answers the app's bundle now (read at most every 15 seconds).
func (m *BundleMigration) Stage(ctx context.Context) string {
	if !club.SheetLegacy() {
		return StageServer // after the cutover only the server answers
	}
	m.mu.Lock()
	if m.stage != "" && m.now().Sub(m.stageAt) < 15*time.Second {
		s := m.stage
		m.mu.Unlock()
		return s
	}
	m.mu.Unlock()
	v, err := m.meta.GetMeta(ctx, metaBundleStage)
	if err != nil {
		return StageSheet // unsure: the script answers, as always
	}
	if v != StageSheet && v != StageServer {
		v = StageShadow // the start
	}
	m.mu.Lock()
	m.stage, m.stageAt = v, m.now()
	m.mu.Unlock()
	return v
}

// SetStage changes the stage; by is "auto" or who did it.
func (m *BundleMigration) SetStage(ctx context.Context, stage, by string) error {
	if stage != StageSheet && stage != StageShadow && stage != StageServer {
		return fmt.Errorf("stage: sheet, shadow or server")
	}
	for k, v := range map[string]string{metaBundleStage: stage, metaBundleStageBy: by, metaBundleStageAt: m.now().UTC().Format(time.RFC3339)} {
		if err := m.meta.SetMeta(ctx, k, v); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.stage, m.stageAt = stage, m.now()
	m.mu.Unlock()
	log.Printf("app bundle: stage %s (%s)", stage, by)
	return nil
}

// CheckUser is how one person's bundle compared.
type CheckUser struct {
	Role       string `json:"role"`
	TgID       int64  `json:"tg"`
	OK         bool   `json:"ok"`
	Mismatches int    `json:"mismatches"`
	Error      string `json:"error,omitempty"`
}

// BundleCheckResult is one comparison.
type BundleCheckResult struct {
	Day        string                `json:"day"`
	At         time.Time             `json:"at"`
	OK         bool                  `json:"ok"`
	Complete   bool                  `json:"complete"` // every person's script bundle came
	InStep     bool                  `json:"inStep"`   // no club write since the last import
	Sections   []string              `json:"sections"`
	Script     []string              `json:"scriptSections"`
	Users      []CheckUser           `json:"users"`
	Count      int                   `json:"mismatchCount"`
	Mismatches []club.BundleMismatch `json:"mismatches"`
}

func (m *BundleMigration) sample(ctx context.Context) []SampleUser {
	if len(m.Sample) > 0 {
		return m.Sample
	}
	owner := m.Owner
	if owner == 0 {
		owner = 453800951
	}
	out := []SampleUser{{Role: "admin", TgID: owner}}
	// A resident who is not team, a different one each day.
	if ids, err := m.repo.ClubTgIDs(ctx); err == nil {
		var res []int64
		for _, id := range ids {
			if _, team := m.gw.Admins[id]; !team && id != owner {
				res = append(res, id)
			}
		}
		sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
		if len(res) > 0 {
			out = append(out, SampleUser{Role: "resident", TgID: res[m.now().YearDay()%len(res)]})
		}
	}
	return append(out, SampleUser{Role: "lead", TgID: 999})
}

// Check compares the script's bundle with the server's for the sample.
func (m *BundleMigration) Check(ctx context.Context) (*BundleCheckResult, error) {
	now := m.now()
	res := &BundleCheckResult{Day: now.In(club.Almaty).Format("2006-01-02"), At: now, Complete: true, Mismatches: []club.BundleMismatch{}}
	if m.gw.Club == nil {
		return nil, fmt.Errorf("no club data source")
	}
	done := m.gw.doneSet(ctx)
	// The server's bundle of one person: some sections depend on who asks
	// (the checklists they took, their avatar).
	serverOf := func(tgID int64) (map[string]any, []string, error) {
		srvParts, built, err := m.gw.ServerBundle(ctx, tgID)
		if err != nil {
			return nil, nil, err
		}
		var server map[string]any
		if err := json.Unmarshal(hideDone(mergeBundle(srvParts, nil, now), done, now), &server); err != nil {
			return nil, nil, err
		}
		return server, built, nil
	}
	if _, built, err := serverOf(0); err != nil {
		return nil, err
	} else {
		res.Sections = built
	}
	isBuilt := map[string]bool{}
	for _, k := range res.Sections {
		isBuilt[k] = true
	}
	for _, k := range club.BundleKeys {
		if !isBuilt[k] && k != "ts" {
			res.Script = append(res.Script, k)
		}
	}
	res.InStep = m.inStep(ctx)

	type key struct{ f, a, b string }
	seen := map[key]int{}
	for _, u := range m.sample(ctx) {
		cu := CheckUser{Role: u.Role, TgID: u.TgID}
		server, built, err := serverOf(u.TgID)
		if err != nil {
			return nil, err
		}
		body, err := m.gw.ScriptBundle(ctx, u.TgID)
		var sheet map[string]any
		if err == nil {
			err = json.Unmarshal(hideDone(body, done, now), &sheet)
		}
		if err == nil {
			if e, _ := sheet["error"].(string); e != "" {
				err = fmt.Errorf("script: %s", e)
			}
		}
		if err != nil {
			cu.Error = err.Error()
			res.Complete = false
			res.Users = append(res.Users, cu)
			continue
		}
		diff := club.DiffBundle(sheet, server, built, now)
		cu.Mismatches, cu.OK = len(diff), len(diff) == 0
		for _, d := range diff {
			k := key{d.Field, d.Sheet, d.Server}
			if i, ok := seen[k]; ok {
				res.Mismatches[i].Role += "," + u.Role
				continue
			}
			d.Role = u.Role
			seen[k] = len(res.Mismatches)
			res.Mismatches = append(res.Mismatches, d)
		}
		res.Users = append(res.Users, cu)
	}
	res.Count = len(res.Mismatches)
	res.OK = res.Complete && res.Count == 0
	if len(res.Mismatches) > 300 {
		res.Mismatches = res.Mismatches[:300]
	}
	return res, nil
}

// inStep: the server's tables hold what the sheet holds — the last import
// came after the last club write and no write waits for the sheet.
func (m *BundleMigration) inStep(ctx context.Context) bool {
	imp, err := m.repo.LastImportAt(ctx)
	if err != nil || imp == nil || m.now().Sub(*imp) > 75*time.Minute {
		return false
	}
	if w, err := m.repo.LastWriteAt(ctx); err != nil || (w != nil && w.After(*imp)) {
		return false
	}
	open, err := m.repo.OpenWrites(ctx)
	return err == nil && len(open) == 0
}

// RunCheck compares now, keeps the day's result and acts on it.
func (m *BundleMigration) RunCheck(ctx context.Context) (*BundleCheckResult, error) {
	res, err := m.Check(ctx)
	if err != nil {
		return nil, err
	}
	if !res.Complete {
		// The script did not answer for everyone: not a day's verdict.
		m.publishSoon()
		return res, nil
	}
	day, _ := time.ParseInLocation("2006-01-02", res.Day, club.Almaty)
	if err := m.repo.SaveBundleCheck(ctx, day, res.OK, res); err != nil {
		return res, err
	}
	m.afterCheck(ctx, res)
	return res, nil
}

// Streak: days in a row without a difference, up to yesterday or today.
func (m *BundleMigration) Streak(ctx context.Context) (int, error) {
	list, err := m.repo.BundleChecks(ctx, 60)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	a := m.now().In(club.Almaty)
	today := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, club.Almaty)
	dayOf := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, club.Almaty) }
	if today.Sub(dayOf(list[0].Day)) > 24*time.Hour {
		return 0, nil
	}
	n := 0
	for i, c := range list {
		if !c.OK {
			break
		}
		if i > 0 && dayOf(list[i-1].Day).AddDate(0, 0, -1) != dayOf(c.Day) {
			break
		}
		n++
	}
	return n, nil
}

func (m *BundleMigration) afterCheck(ctx context.Context, res *BundleCheckResult) {
	if n, err := m.Streak(ctx); err == nil && n >= BundleStreak && m.Stage(ctx) == StageShadow {
		if err := m.SetStage(ctx, StageServer, "auto"); err != nil {
			log.Printf("app bundle: flip: %v", err)
		}
	}
	m.Publish(ctx)
	m.maybeNotify(ctx, res)
}

// maybeNotify tells the owner about a day with differences or stuck writes,
// once a day at most.
func (m *BundleMigration) maybeNotify(ctx context.Context, res *BundleCheckResult) {
	if m.Notify == nil {
		return
	}
	var stuck []pg.ClubWrite
	if open, err := m.repo.OpenWrites(ctx); err == nil {
		for _, w := range open {
			if m.now().Sub(w.At) > stuckWriteTime {
				stuck = append(stuck, w)
			}
		}
	}
	if (res == nil || res.Count == 0) && len(stuck) == 0 {
		return
	}
	day := m.now().In(club.Almaty).Format("2006-01-02")
	if v, _ := m.meta.GetMeta(ctx, metaBundleNotified); v == day {
		return
	}
	if err := m.meta.SetMeta(ctx, metaBundleNotified, day); err != nil {
		return
	}
	var b strings.Builder
	b.WriteString("📊 Приложение на сервере: сверка с таблицей\n")
	if res != nil && res.Count > 0 {
		fmt.Fprintf(&b, "\nРасхождений: %d. Серия без расхождений обнулилась.\n", res.Count)
		for i, d := range res.Mismatches {
			if i == 5 {
				b.WriteString("…\n")
				break
			}
			fmt.Fprintf(&b, "• %s: таблица %s, сервер %s\n", d.Field, d.Sheet, d.Server)
		}
	}
	if len(stuck) > 0 {
		fmt.Fprintf(&b, "\nНе дошли до таблицы: %d (самое раннее %s). Сохранены на сервере, отправка повторяется.\n",
			len(stuck), stuck[0].At.In(club.Almaty).Format("02.01 15:04"))
	}
	b.WriteString("\nПодробно: платформа, статус переезда.")
	m.Notify(ctx, b.String())
}

// MigrationStatus is the club doc bs_migration_status.
type MigrationStatus struct {
	Stage    string `json:"stage"`
	StageBy  string `json:"stageBy,omitempty"`
	StageAt  string `json:"stageAt,omitempty"`
	Streak   int    `json:"streak"`
	Need     int    `json:"need"`
	NeedDays int    `json:"needDays"`
	// SectionsBy: who gives each section of the app's bundle now ("server"
	// or "script"), by the stage and what the server can build.
	SectionsBy map[string]string     `json:"sections"`
	LastCheck  *time.Time            `json:"lastCheck,omitempty"`
	OK         bool                  `json:"ok"`
	InStep     bool                  `json:"inStep"`
	Count      int                   `json:"mismatchCount"`
	Mismatches []club.BundleMismatch `json:"mismatches"`
	Users      []CheckUser           `json:"users,omitempty"`
	Sections   []string              `json:"serverSections,omitempty"`
	Script     []string              `json:"scriptSections,omitempty"`
	History    []map[string]any      `json:"history"`
	Writes     pg.WriteStats         `json:"writes"`
	Served     int                   `json:"servedByServer"`
	Fallbacks  int                   `json:"fallbacks"`
	// ScriptUpdate: the sheet's self-update (latest shipped, running, last update, last error)
	ScriptUpdate bot.ScriptUpdateStatus `json:"script"`
	UpdatedAt    time.Time              `json:"updatedAt"`
	// SheetMode: off, mirror or legacy (club.SheetMode); Cutover: "", pending
	// (waiting for the sheet's final import) or done, CutoverAt since when.
	SheetMode string `json:"sheetMode"`
	Cutover   string `json:"cutover,omitempty"`
	CutoverAt string `json:"cutoverAt,omitempty"`
}

func (m *BundleMigration) Status(ctx context.Context) (MigrationStatus, error) {
	st := MigrationStatus{Stage: m.Stage(ctx), Need: BundleStreak, NeedDays: BundleStreak, Mismatches: []club.BundleMismatch{},
		History: []map[string]any{}, UpdatedAt: m.now(), SectionsBy: map[string]string{}, SheetMode: club.SheetMode()}
	if m.writes != nil {
		st.Cutover, st.CutoverAt = m.writes.Cutover.State(ctx)
	}
	built := map[string]bool{}
	if m.gw.Club != nil {
		if _, list, err := m.gw.ServerBundle(ctx, 0); err == nil {
			for _, k := range list {
				built[k] = true
			}
		}
	}
	for _, k := range club.BundleKeys {
		if k == "ts" {
			continue
		}
		st.SectionsBy[k] = "script"
		if built[k] && st.Stage == StageServer {
			st.SectionsBy[k] = "server"
		}
	}
	st.StageBy, _ = m.meta.GetMeta(ctx, metaBundleStageBy)
	st.StageAt, _ = m.meta.GetMeta(ctx, metaBundleStageAt)
	var err error
	if st.Streak, err = m.Streak(ctx); err != nil {
		return st, err
	}
	list, err := m.repo.BundleChecks(ctx, 14)
	if err != nil {
		return st, err
	}
	for i, c := range list {
		var r BundleCheckResult
		_ = json.Unmarshal(c.Result, &r)
		st.History = append(st.History, map[string]any{"day": c.Day.Format("2006-01-02"), "ok": c.OK, "mismatches": r.Count})
		if i == 0 {
			at := c.At
			st.LastCheck, st.OK, st.InStep, st.Count, st.Users, st.Sections, st.Script = &at, c.OK, r.InStep, r.Count, r.Users, r.Sections, r.Script
			if len(r.Mismatches) > maxShown {
				r.Mismatches = r.Mismatches[:maxShown]
			}
			if r.Mismatches != nil {
				st.Mismatches = r.Mismatches
			}
		}
	}
	if st.Writes, err = m.repo.WriteStats(ctx); err != nil {
		return st, err
	}
	st.Served, st.Fallbacks = m.gw.ServerStats()
	st.ScriptUpdate = bot.ReadScriptUpdate(ctx, m.meta)
	return st, nil
}

// Publish writes the status into the club doc the platform shows.
func (m *BundleMigration) Publish(ctx context.Context) {
	if m.docs == nil {
		return
	}
	st, err := m.Status(ctx)
	if err != nil {
		log.Printf("migration status: %v", err)
		return
	}
	st.UpdatedAt = time.Time{} // the doc changes only when something in it does
	b, _ := json.Marshal(st)
	if err := m.docs.PutServerDoc(ctx, MigrationStatusKey, string(b)); err != nil {
		log.Printf("migration status doc: %v", err)
	}
}

func (m *BundleMigration) publishSoon() {
	select {
	case m.publishQ <- struct{}{}:
	default:
	}
}

// Tick runs the day's comparison when it is due.
func (m *BundleMigration) Tick(ctx context.Context) (*BundleCheckResult, error) {
	if m.Stage(ctx) == StageSheet || !club.SheetLegacy() {
		return nil, nil
	}
	a := m.now().In(club.Almaty)
	if a.Hour() < checkFromHour {
		return nil, nil
	}
	list, err := m.repo.BundleChecks(ctx, 1)
	if err != nil {
		return nil, err
	}
	if len(list) > 0 && list[0].Day.Format("2006-01-02") == a.Format("2006-01-02") {
		return nil, nil // today's is done
	}
	m.mu.Lock()
	recent := m.now().Sub(m.lastTry) < 30*time.Minute
	m.mu.Unlock()
	if recent {
		return nil, nil
	}
	if !m.inStep(ctx) && a.Hour() < checkGiveUpAt {
		return nil, nil // wait until the sheet's data and the server's are in step
	}
	m.mu.Lock()
	m.lastTry = m.now()
	m.mu.Unlock()
	return m.RunCheck(ctx)
}

// Loop runs the daily comparison and keeps the status doc current.
func (m *BundleMigration) Loop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	m.Publish(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.Tick(ctx); err != nil {
				log.Printf("app bundle check: %v", err)
			}
		case <-m.publishQ:
			time.Sleep(2 * time.Second) // several changes, one write
			m.Publish(ctx)
		}
	}
}

// ── team endpoints ──

type MigrationModule struct {
	m      *BundleMigration
	secret []byte
}

func NewMigrationModule(m *BundleMigration, secret []byte) *MigrationModule {
	return &MigrationModule{m: m, secret: secret}
}

func (mm *MigrationModule) Register(r *gin.Engine) {
	g := r.Group("/api/v1/club")
	g.Use(middleware.AuthJWT(mm.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("/migration", mm.status)
	g.POST("/migration/check", mm.check)
	g.GET("/writes", mm.listWrites)
	g.POST("/writes/flush", mm.flush)
	a := r.Group("/api/v1/club")
	a.Use(middleware.AuthJWT(mm.secret))
	a.Use(middleware.RequireRole("admin"))
	a.POST("/migration/stage", mm.setStage)
}

// status godoc
// @Summary  Moving the app to the server: stage, streak, last comparison, writes
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/migration [get]
func (mm *MigrationModule) status(c *gin.Context) {
	st, err := mm.m.Status(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, st)
}

// check godoc
// @Summary  Compare the app's bundle now (script against server); kept as today's result
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/migration/check [post]
func (mm *MigrationModule) check(c *gin.Context) {
	res, err := mm.m.RunCheck(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// setStage godoc
// @Summary  Set who answers the app's bundle: sheet, shadow (compare, move on by itself) or server
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/migration/stage [post]
func (mm *MigrationModule) setStage(c *gin.Context) {
	var req struct {
		Stage string `json:"stage"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	if err := mm.m.SetStage(c.Request.Context(), req.Stage, platformUser(c)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mm.m.Publish(c.Request.Context())
	st, _ := mm.m.Status(c.Request.Context())
	c.JSON(http.StatusOK, st)
}

// listWrites godoc
// @Summary  Club writes still on their way to the sheet
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/writes [get]
func (mm *MigrationModule) listWrites(c *gin.Context) {
	open, err := mm.m.repo.OpenWrites(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if open == nil {
		open = []pg.ClubWrite{}
	}
	c.JSON(http.StatusOK, gin.H{"writes": open})
}

// flush godoc
// @Summary  Send the waiting club writes to the sheet now
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/writes/flush [post]
func (mm *MigrationModule) flush(c *gin.Context) {
	if mm.m.writes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "writes not configured"})
		return
	}
	n, err := mm.m.writes.Flush(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	open, _ := mm.m.repo.OpenWrites(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"sent": n, "waiting": len(open)})
}
