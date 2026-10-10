package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// R32d: the server owns the club's data; the Google Sheet and its script are
// optional. SheetOwner keeps the two things left between them:
//
//   - the export: the dormant script takes an hourly read-only copy of the
//     server's data (ДДС, PL and copy tabs of residents, fines, meetings,
//     reports). R77: only with SHEET_MODE=mirror; there an admin's switch
//     (bot_meta sheet_export) or SHEET_EXPORT=on|off turns it off or on. Nothing on
//     the server waits for it: the sheet or the script may disappear.
//   - the reconciliation: once, at the first deploy, the server asks the
//     sheet for a copy and compares it with its own data (club.Reconcile).
//     During the cutover that copy is the final import (the sheet still was
//     the master: it replaces, the server's writes go back on top); after it
//     the server keeps its data and takes from the sheet only what the sheet
//     alone has (new rows), and logs every difference. An admin can ask for
//     it again, or send a copy by hand (merge, or replace in an emergency).
//
// Rollback: SHEET_MODE=legacy (the script is the bot and the master again;
// the export and the reconciliation stop).

const (
	metaSheetExport   = "sheet_export" // "on" | "off" (an admin's choice); "" = default
	metaSheetExportBy = "sheet_export_by"
	metaSheetExportAt = "sheet_export_at"
	metaReconcile     = "sheet_reconcile" // "wanted:<RFC3339>" | "done:<RFC3339>" | "skipped:<RFC3339>"
	metaReconcileRes  = "sheet_reconcile_result"

	// reconcileWait: how long the server waits for the sheet's copy.
	reconcileWait = 48 * time.Hour
)

// SheetOwnerRepo is what the owner needs of the club data.
type SheetOwnerRepo interface {
	Master(ctx context.Context) (string, error)
	LastImportAt(ctx context.Context) (*time.Time, error)
	LoadBundle(ctx context.Context, since time.Time) (*club.Snapshot, error)
	MergeFromSheet(ctx context.Context, a *club.RecAdd, by string) (int, error)
	ReplaceAllThen(ctx context.Context, s *club.Snapshot, by string, then func(ctx context.Context, tx pgx.Tx) error) error
}

type SheetOwner struct {
	repo     SheetOwnerRepo
	meta     MetaStore
	cut      *SheetCutover
	botToken string
	now      func() time.Time
	// OnChange: the club tables changed (the platform's sections, the app's bundles).
	OnChange func()

	mu    sync.Mutex
	cache map[string]cachedMeta
	runMu sync.Mutex
}

type cachedMeta struct {
	v  string
	at time.Time
}

func NewSheetOwner(repo SheetOwnerRepo, meta MetaStore, cut *SheetCutover, botToken string) *SheetOwner {
	return &SheetOwner{repo: repo, meta: meta, cut: cut, botToken: strings.TrimSpace(botToken), now: time.Now, cache: map[string]cachedMeta{}}
}

func (s *SheetOwner) get(ctx context.Context, key string) string {
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && s.now().Sub(c.at) < 15*time.Second {
		s.mu.Unlock()
		return c.v
	}
	s.mu.Unlock()
	v, err := s.meta.GetMeta(ctx, key)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	s.cache[key] = cachedMeta{v: v, at: s.now()}
	s.mu.Unlock()
	return v
}

func (s *SheetOwner) set(ctx context.Context, key, v string) error {
	if err := s.meta.SetMeta(ctx, key, v); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = cachedMeta{v: v, at: s.now()}
	s.mu.Unlock()
	return nil
}

// Install makes the bot's control call and the export endpoint ask this owner.
func (s *SheetOwner) Install() func() {
	return club.SetSheetHooks(func(ctx context.Context) bool { return s.Export(ctx).On },
		func(ctx context.Context) bool { return s.ReconcileWanted(ctx) })
}

// ExportState is the export's switch as the platform shows it.
type ExportState struct {
	On bool `json:"on"`
	// Why: admin (the platform's switch), env (SHEET_EXPORT or SHEET_MODE),
	// week (the first week after the cutover), default, legacy.
	Why   string `json:"why"`
	Until string `json:"until,omitempty"` // the default week ends (RFC3339)
	By    string `json:"by,omitempty"`
	At    string `json:"at,omitempty"`
}

// Export decides whether the sheet gets the hourly copy.
func (s *SheetOwner) Export(ctx context.Context) ExportState {
	if club.SheetLegacy() {
		return ExportState{Why: "legacy"}
	}
	// R77: SHEET_MODE=off (the default) is the full detachment: the sheet
	// gets no copy at all, whatever the switch or SHEET_EXPORT say. Only
	// SHEET_MODE=mirror (an env choice) brings the hourly copy back.
	if club.SheetMode() != club.SheetModeMirror {
		return ExportState{Why: "off"}
	}
	switch s.get(ctx, metaSheetExport) {
	case "on":
		return ExportState{On: true, Why: "admin", By: s.get(ctx, metaSheetExportBy), At: s.get(ctx, metaSheetExportAt)}
	case "off":
		return ExportState{Why: "admin", By: s.get(ctx, metaSheetExportBy), At: s.get(ctx, metaSheetExportAt)}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SHEET_EXPORT"))) {
	case "on", "1", "true", "yes":
		return ExportState{On: true, Why: "env"}
	case "off", "0", "false", "no":
		return ExportState{Why: "env"}
	}
	return ExportState{On: true, Why: "env"}
}

// SetExport: an admin's choice; "" goes back to the default.
func (s *SheetOwner) SetExport(ctx context.Context, v, by string) error {
	if v != "on" && v != "off" && v != "" {
		return errors.New("on, off or default")
	}
	for k, x := range map[string]string{metaSheetExport: v, metaSheetExportBy: by, metaSheetExportAt: s.now().UTC().Format(time.RFC3339)} {
		if err := s.set(ctx, k, x); err != nil {
			return err
		}
	}
	log.Printf("sheet export: %q by %s", v, by)
	return nil
}

// ── the reconciliation ──

// ReconcileState: "", wanted, done or skipped, and since when.
func (s *SheetOwner) ReconcileState(ctx context.Context) (string, string) {
	st, at, _ := strings.Cut(s.get(ctx, metaReconcile), ":")
	return st, at
}

func (s *SheetOwner) ReconcileWanted(ctx context.Context) bool {
	if !sheetMirror() {
		return false
	}
	st, _ := s.ReconcileState(ctx)
	return st == "wanted"
}

func (s *SheetOwner) mark(ctx context.Context, st string) error {
	return s.set(ctx, metaReconcile, st+":"+s.now().UTC().Format(time.RFC3339))
}

// Request asks the sheet for one copy to compare (at the deploy, or an admin).
func (s *SheetOwner) Request(ctx context.Context, by string) error {
	log.Printf("sheet reconcile: requested by %s", by)
	return s.mark(ctx, "wanted")
}

// Begin runs at start: the first deploy asks for the reconciliation once.
func (s *SheetOwner) Begin(ctx context.Context) {
	if !sheetMirror() {
		return
	}
	if st, _ := s.ReconcileState(ctx); st == "" {
		if err := s.Request(ctx, "deploy"); err != nil {
			log.Printf("sheet reconcile: %v", err)
		}
	}
}

// Check gives up waiting for a sheet that does not answer.
func (s *SheetOwner) Check(ctx context.Context) {
	st, at := s.ReconcileState(ctx)
	if st != "wanted" {
		return
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil || s.now().Sub(t) < reconcileWait {
		return
	}
	log.Printf("sheet reconcile: no copy from the sheet in %s, skipped (the server keeps its data)", reconcileWait)
	_ = s.mark(ctx, "skipped")
}

func (s *SheetOwner) Loop(ctx context.Context) {
	if !sheetMirror() {
		return // R77: no timer while the sheet is detached
	}
	s.Begin(ctx)
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Check(ctx)
		}
	}
}

// sheetMirror (R77): the sheet still takes part as a copy (SHEET_MODE=mirror).
// Off (the default) is the full detachment: no export, no reconciliation
// copy asked of the sheet, no timer; legacy is the emergency rollback.
func sheetMirror() bool { return club.SheetMode() == club.SheetModeMirror }

// ReconcileResult is kept in bot_meta and shown on the platform.
type ReconcileResult struct {
	At      time.Time            `json:"at"`
	By      string               `json:"by"`
	Mode    string               `json:"mode"` // final | merge | replace
	DryRun  bool                 `json:"dryRun"`
	Saved   bool                 `json:"saved"`
	Added   int                  `json:"added"`
	Summary string               `json:"summary"`
	Report  *club.Reconciliation `json:"report"`
}

func (s *SheetOwner) LastResult(ctx context.Context) *ReconcileResult {
	v := s.get(ctx, metaReconcileRes)
	if v == "" {
		return nil
	}
	var r ReconcileResult
	if json.Unmarshal([]byte(v), &r) != nil {
		return nil
	}
	return &r
}

func (s *SheetOwner) keep(ctx context.Context, r *ReconcileResult) {
	b, _ := json.Marshal(r)
	if err := s.set(ctx, metaReconcileRes, string(b)); err != nil {
		log.Printf("sheet reconcile: keep result: %v", err)
	}
}

func (s *SheetOwner) serverSnap(ctx context.Context) (*club.Snapshot, error) {
	snap, err := s.repo.LoadBundle(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	if sr, ok := s.repo.(interface {
		AllSettings(ctx context.Context) ([]club.Setting, error)
	}); ok {
		if snap.Settings, err = sr.AllSettings(ctx); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

// NoteFinal: the sheet's final import during the cutover is about to replace
// the server's tables. The differences are logged and kept; the import goes on.
func (s *SheetOwner) NoteFinal(ctx context.Context, sheet *club.Snapshot) {
	if s == nil {
		return
	}
	srv, err := s.serverSnap(ctx)
	if err != nil {
		log.Printf("sheet reconcile (final import): %v", err)
		return
	}
	rep, _ := club.Reconcile(srv, sheet, club.RecOptions{})
	res := &ReconcileResult{At: s.now(), By: "sheet", Mode: "final", Saved: true, Report: rep,
		Summary: "Последний перенос из таблицы: её копия заменила данные сервера, записи сервера возвращены поверх. " + rep.Summary()}
	log.Printf("sheet reconcile (final import): %s", rep.Summary())
	s.keep(ctx, res)
	_ = s.mark(ctx, "done")
}

// Merge compares a copy of the sheet with the server and takes what only
// the sheet has (club.Reconcile); dry only compares.
func (s *SheetOwner) Merge(ctx context.Context, sheet *club.Snapshot, by string, since *time.Time, mirroredDDS, dry bool) (*ReconcileResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	srv, err := s.serverSnap(ctx)
	if err != nil {
		return nil, err
	}
	o := club.RecOptions{MirroredDDS: mirroredDDS}
	if since != nil {
		o.Since = *since
	} else if t, err := s.repo.LastImportAt(ctx); err == nil && t != nil {
		o.Since = *t
	}
	rep, add := club.Reconcile(srv, sheet, o)
	res := &ReconcileResult{At: s.now(), By: by, Mode: "merge", DryRun: dry, Report: rep, Summary: rep.Summary()}
	if !dry {
		n, err := s.repo.MergeFromSheet(ctx, add, "reconcile:"+by)
		if err != nil {
			return nil, err
		}
		res.Saved, res.Added = true, n
		if n > 0 && s.OnChange != nil {
			go s.OnChange()
		}
		log.Printf("sheet reconcile (%s): %s; rows added: %d", by, rep.Summary(), n)
		s.keep(ctx, res)
		_ = s.mark(ctx, "done")
	}
	return res, nil
}

// Replace: an admin's emergency import, the sheet's copy replaces the
// server's tables although the server is the master.
func (s *SheetOwner) Replace(ctx context.Context, sheet *club.Snapshot, by string, dry bool) (*ReconcileResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	srv, err := s.serverSnap(ctx)
	if err != nil {
		return nil, err
	}
	rep, _ := club.Reconcile(srv, sheet, club.RecOptions{})
	res := &ReconcileResult{At: s.now(), By: by, Mode: "replace", DryRun: dry, Report: rep,
		Summary: "Копия таблицы заменяет данные сервера. " + rep.Summary()}
	if dry {
		return res, nil
	}
	if err := s.repo.ReplaceAllThen(pg.WithForceReplace(ctx), sheet, "manual:"+by, nil); err != nil {
		return nil, err
	}
	res.Saved = true
	log.Printf("sheet import (replace) by %s: %s", by, rep.Summary())
	s.keep(ctx, res)
	if s.OnChange != nil {
		go s.OnChange()
	}
	return res, nil
}

// ── endpoints ──

type SheetOwnerModule struct {
	s      *SheetOwner
	secret []byte
}

func NewSheetOwnerModule(s *SheetOwner, secret []byte) *SheetOwnerModule {
	return &SheetOwnerModule{s: s, secret: secret}
}

func (m *SheetOwnerModule) Register(r *gin.Engine) {
	// The dormant script's copy for the reconciliation (signed with the bot token).
	r.POST("/api/v1/club/reconcile", m.fromScript)
	g := r.Group("/api/v1/club")
	g.Use(middleware.AuthJWT(m.secret))
	g.Use(middleware.RequireRole("admin", "moderator"))
	g.GET("/sheet", m.status)
	a := r.Group("/api/v1/club")
	a.Use(middleware.AuthJWT(m.secret))
	a.Use(middleware.RequireRole("admin"))
	a.POST("/sheet/export", m.setExport)
	a.POST("/import/request", m.request)
	a.POST("/import/manual", m.manual)
}

// SheetStatus is what the platform shows of the sheet.
type SheetStatus struct {
	SheetMode string           `json:"sheetMode"`
	Master    string           `json:"master"`
	Cutover   string           `json:"cutover,omitempty"`
	CutoverAt string           `json:"cutoverAt,omitempty"`
	Export    ExportState      `json:"export"`
	Reconcile string           `json:"reconcile,omitempty"`
	ReconAt   string           `json:"reconcileAt,omitempty"`
	Last      *ReconcileResult `json:"last,omitempty"`
}

func (s *SheetOwner) Status(ctx context.Context) SheetStatus {
	st := SheetStatus{SheetMode: club.SheetMode(), Export: s.Export(ctx), Last: s.LastResult(ctx)}
	st.Master, _ = s.repo.Master(ctx)
	st.Cutover, st.CutoverAt = s.cut.State(ctx)
	st.Reconcile, st.ReconAt = s.ReconcileState(ctx)
	return st
}

// status godoc
// @Summary  The Google Sheet after the cutover: the export switch and the last reconciliation
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/sheet [get]
func (m *SheetOwnerModule) status(c *gin.Context) {
	c.JSON(http.StatusOK, m.s.Status(c.Request.Context()))
}

// setExport godoc
// @Summary  Turn the hourly read-only copy into the Google Sheet on or off (admin)
// @Description  Body {on: true|false} or {reset: true} for the default (first week after the cutover, SHEET_EXPORT).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/sheet/export [post]
func (m *SheetOwnerModule) setExport(c *gin.Context) {
	var req struct {
		On    *bool `json:"on"`
		Reset bool  `json:"reset"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.On == nil && !req.Reset) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body {on: true|false} or {reset: true}"})
		return
	}
	v := ""
	if !req.Reset {
		v = "off"
		if *req.On {
			v = "on"
		}
	}
	ctx := c.Request.Context()
	if err := m.s.SetExport(ctx, v, platformUser(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, m.s.Status(ctx))
}

// request godoc
// @Summary  Ask the sheet for one copy to compare with the server (admin); the dormant script sends it within the hour
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/import/request [post]
func (m *SheetOwnerModule) request(c *gin.Context) {
	ctx := c.Request.Context()
	if club.SheetLegacy() {
		c.JSON(http.StatusConflict, gin.H{"error": "legacy", "detail": "в аварийном режиме таблица главная"})
		return
	}
	if err := m.s.Request(ctx, platformUser(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, m.s.Status(ctx))
}

type sheetCopyReq struct {
	TS          int64       `json:"ts"`
	Sheets      club.Sheets `json:"sheets"`
	MirroredDDS bool        `json:"mirroredDDS"`
}

func parseCopy(c *gin.Context, body []byte) (*sheetCopyReq, *club.Snapshot, bool) {
	var req sheetCopyReq
	if err := json.Unmarshal(body, &req); err != nil || req.Sheets == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {ts, sheets}"})
		return nil, nil, false
	}
	snap, warn, err := club.Parse(req.Sheets)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "parse", "detail": err.Error(), "warnings": nz(warn)})
		return nil, nil, false
	}
	return &req, snap, true
}

// fromScript godoc
// @Summary  The sheet's copy for the reconciliation, sent once by the dormant script
// @Description  Signed (X-BS-Signature = HMAC-SHA256 of the body with the bot token). Body {ts, sheets, mirroredDDS}. Taken only while the server asks for it (bot/control reconcile:true): the server keeps its data and adds what only the sheet has; every difference is logged.
// @Tags     club
// @Router   /api/v1/club/reconcile [post]
func (m *SheetOwnerModule) fromScript(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body too large or unreadable"})
		return
	}
	if !VerifyBotSignature(body, c.GetHeader("X-BS-Signature"), m.s.botToken) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad_signature"})
		return
	}
	req, snap, ok := parseCopy(c, body)
	if !ok {
		return
	}
	if d := time.Since(time.Unix(req.TS, 0)); d > time.Hour || d < -5*time.Minute {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "stale_request"})
		return
	}
	ctx := c.Request.Context()
	if !m.s.ReconcileWanted(ctx) {
		c.JSON(http.StatusConflict, gin.H{"error": "not_wanted", "detail": "сервер не просил копию таблицы"})
		return
	}
	if mst, err := m.s.repo.Master(ctx); err != nil || mst != "server" {
		c.JSON(http.StatusConflict, gin.H{"error": "final_import_first", "detail": "сначала последний перенос (final)"})
		return
	}
	res, err := m.s.Merge(ctx, snap, "sheet", nil, req.MirroredDDS, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// manual godoc
// @Summary  Emergency import of a copy of the sheet (admin)
// @Description  Body {sheets:{name:[[cells…]]}} with display values (as the script sends). ?mode=merge (default: the server keeps its data, takes what only the sheet has) or replace (the copy replaces the server's tables; needs ?confirm=replace). ?dry=1 only compares. ?since=dd.mm.yyyy: older rows only the sheet has are not taken (default: the last import).
// @Tags     club
// @Security BearerAuth
// @Router   /api/v1/club/import/manual [post]
func (m *SheetOwnerModule) manual(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body too large or unreadable"})
		return
	}
	req, snap, ok := parseCopy(c, body)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	dry := c.Query("dry") == "1"
	by := platformUser(c)
	var res *ReconcileResult
	switch mode := c.DefaultQuery("mode", "merge"); mode {
	case "merge":
		var since *time.Time
		if v := strings.TrimSpace(c.Query("since")); v != "" {
			t, ok := club.Date(v)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "since: dd.mm.yyyy"})
				return
			}
			since = &t
		}
		res, err = m.s.Merge(ctx, snap, by, since, req.MirroredDDS, dry)
	case "replace":
		if !dry && c.Query("confirm") != "replace" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "confirm", "detail": "замена данных сервера: добавьте ?confirm=replace (сначала ?dry=1)"})
			return
		}
		res, err = m.s.Replace(ctx, snap, by, dry)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode: merge or replace"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, res)
}
