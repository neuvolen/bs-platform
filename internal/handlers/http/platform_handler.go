package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// PlatformHandler serves the storage behind the BS platform (boards and the
// sections that used to live in each browser's localStorage).
type PlatformHandler struct {
	repo  *pg.PlatformRepo
	names *residentNames
}

func NewPlatformHandler(repo *pg.PlatformRepo) *PlatformHandler {
	return &PlatformHandler{repo: repo, names: &residentNames{m: map[int64]residentName{}, repo: repo}}
}

func forbidden(c *gin.Context, why string) {
	c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "reason": why})
}

// Boards can carry cover images; keep the ceiling generous but finite.
const platformMaxBody = 25 << 20

// Keys that belong to one person's device habits, not to the club.
var platformPersonalKeys = map[string]bool{
	"bs_theme":   true,
	"bs_order":   true,
	"bs_onboard": true,
	"bs_me":      true, // who I am on the board (name, colour, presence id)
	// R32b: «Аналитика и инсайты»: что человек скрыл, отправил в идеи или задачи
	"bs_an_state": true,
}

// Keys that are pure local bookkeeping and never leave the browser.
var platformLocalOnlyKeys = map[string]bool{
	"bs_lastsync": true,
	"bs_peer":     true, // presence ping, rewritten every few seconds
}

// R32b: личный ключ (порядок меню, тема, обучение) живёт только в области
// человека. Старая копия того же ключа в области клуба (запись до того, как
// ключ стал личным, или импорт из файла) приходила вторым документом с тем
// же именем: браузер сверял версии не того документа и после перезагрузки
// возвращал чужой или старый порядок меню. Такие копии не отдаём.
func dropClubPersonal(docs []pg.PlatformDoc) []pg.PlatformDoc {
	out := docs[:0]
	for _, d := range docs {
		if d.Scope == "club" && platformPersonalKeys[d.Key] {
			continue
		}
		out = append(out, d)
	}
	return out
}

var platformKeyRe = regexp.MustCompile(`^bs_[a-z0-9_]{1,60}$`)
var platformIDRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,80}$`)

func platformUser(c *gin.Context) string {
	if v, ok := c.Get("userID"); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return ""
}

func platformScopeFor(c *gin.Context, key string, requested string) string {
	if requested == "me" || (requested == "" && platformPersonalKeys[key]) {
		return "user:" + platformUser(c)
	}
	return "club"
}

// Sync godoc
// @Summary      Changes since a revision
// @Description  Boards and sections changed after `since`. Poll with the returned `rev` to pick up other devices' edits.
// @Tags         platform
// @Security     BearerAuth
// @Produce      json
// @Param        since query int false "last rev the client has seen (0 = everything)"
// @Success      200 {object} map[string]any
// @Router       /api/v1/platform/sync [get]
func (h *PlatformHandler) Sync(c *gin.Context) {
	since, _ := strconv.ParseInt(c.DefaultQuery("since", "0"), 10, 64)
	if since < 0 {
		since = 0
	}
	var name string
	if isResident(c) {
		if name = h.residentOf(c); name == "" {
			forbidden(c, "not_resident")
			return
		}
	}
	boards, docs, rev, err := h.repo.Changes(c.Request.Context(), since, "user:"+platformUser(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if isResident(c) {
		boards, docs = filterForResident(boards, docs, name, "user:"+platformUser(c))
		boards = h.gateResidentCalls(c.Request.Context(), boards, name) // R32e: callsum_flow.go
	}
	docs = dropClubPersonal(docs)
	out := gin.H{"rev": rev, "boards": boards, "docs": docs, "serverTime": time.Now().UTC()}
	// R45: the page names the sections it already holds at the server's version
	// (have=bs_tools.12,bs_guides.4,…); those are not sent again
	if have := c.Query("have"); have != "" {
		var kept []string
		out["docs"], kept = omitHeldDocs(docs, have)
		if len(kept) > 0 {
			out["kept"] = kept
		}
	}
	c.JSON(http.StatusOK, out)
}

// omitHeldDocs drops the sections the client holds at exactly this version.
// The seed is never dropped: a resident gets a cut of it that depends on more
// than its version.
func omitHeldDocs(docs []pg.PlatformDoc, have string) ([]pg.PlatformDoc, []string) {
	held := map[string]int{}
	for _, p := range strings.Split(have, ",") {
		i := strings.LastIndexByte(p, '.')
		if i <= 0 {
			continue
		}
		v, err := strconv.Atoi(p[i+1:])
		if err != nil || v <= 0 || !platformKeyRe.MatchString(p[:i]) {
			continue
		}
		held[p[:i]] = v
	}
	out := make([]pg.PlatformDoc, 0, len(docs))
	var kept []string
	for _, d := range docs {
		if v, ok := held[d.Key]; ok && v == d.Version && !d.Deleted && d.Key != platformSeedKey {
			kept = append(kept, d.Key)
			continue
		}
		out = append(out, d)
	}
	return out, kept
}

type putBoardReq struct {
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// PutBoard godoc
// @Summary      Save a board
// @Description  `version` = the version the edit was based on (0 for a new board). A stale version gets 409 with the current copy.
// @Tags         platform
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id path string true "board id"
// @Success      200 {object} map[string]any
// @Failure      409 {object} map[string]any
// @Router       /api/v1/platform/boards/{id} [put]
func (h *PlatformHandler) PutBoard(c *gin.Context) {
	id := c.Param("id")
	if !platformIDRe.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad board id"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, platformMaxBody)
	var req putBoardReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Data) == 0 || req.Data[0] != '{' {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {version, data:{…}}"})
		return
	}
	if isResident(c) {
		name := h.residentOf(c)
		cur, err := h.repo.GetBoard(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		if name == "" || cur == nil || cur.Deleted || !boardBelongsTo(cur, name) {
			forbidden(c, "not_your_board")
			return
		}
		next := pg.PlatformBoard{}
		next.Resident, _ = pg.BoardLabels(req.Data)
		if !boardBelongsTo(&next, name) {
			forbidden(c, "cannot_reassign_board")
			return
		}
		// R32d: the resident's copy has no recordings; the board's calls stay as kept
		if kept, err := keepCalls(cur.Data, req.Data); err == nil {
			req.Data = kept
		}
	}
	out, err := h.repo.PutBoard(c.Request.Context(), id, req.Version, req.Data, platformUser(c))
	if errors.Is(err, pg.ErrPlatformConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "conflict", "current": out})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// DeleteBoard godoc
// @Summary      Delete a board
// @Description  Soft delete; the board stays in history and can be restored.
// @Tags         platform
// @Security     BearerAuth
// @Produce      json
// @Param        id path string true "board id"
// @Param        version query int true "version the delete is based on"
// @Success      200 {object} map[string]any
// @Failure      409 {object} map[string]any
// @Router       /api/v1/platform/boards/{id} [delete]
func (h *PlatformHandler) DeleteBoard(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "residents_cannot_delete")
		return
	}
	id := c.Param("id")
	if !platformIDRe.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad board id"})
		return
	}
	ver, err := strconv.Atoi(c.Query("version"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version is required"})
		return
	}
	out, err := h.repo.DeleteBoard(c.Request.Context(), id, ver, platformUser(c))
	if errors.Is(err, pg.ErrPlatformConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "conflict", "current": out})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if out == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// BoardVersions godoc
// @Summary      Board history
// @Tags         platform
// @Security     BearerAuth
// @Produce      json
// @Param        id path string true "board id"
// @Success      200 {array} map[string]any
// @Router       /api/v1/platform/boards/{id}/versions [get]
func (h *PlatformHandler) BoardVersions(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	list, err := h.repo.BoardVersions(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

// BoardVersion godoc
// @Summary      One saved copy of a board
// @Tags         platform
// @Security     BearerAuth
// @Produce      json
// @Param        id path string true "board id"
// @Param        version path int true "version"
// @Success      200 {object} map[string]any
// @Router       /api/v1/platform/boards/{id}/versions/{version} [get]
func (h *PlatformHandler) BoardVersion(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	v, err := strconv.Atoi(c.Param("version"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad version"})
		return
	}
	data, err := h.repo.BoardVersion(c.Request.Context(), c.Param("id"), v)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if data == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

type putDocReq struct {
	Version int    `json:"version"`
	Value   string `json:"value"`
	Deleted bool   `json:"deleted"`
	Scope   string `json:"scope"` // "club" (default) or "me"
}

// PutDoc godoc
// @Summary      Save a section
// @Description  A section keyed as in the browser (bs_crm, bs_tools, …). `value` is stored as-is. Same version rule as boards.
// @Tags         platform
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        key path string true "section key, e.g. bs_crm"
// @Success      200 {object} map[string]any
// @Failure      409 {object} map[string]any
// @Router       /api/v1/platform/docs/{key} [put]
func (h *PlatformHandler) PutDoc(c *gin.Context) {
	key := c.Param("key")
	if !platformKeyRe.MatchString(key) || key == "bs_boards" || key == platformSeedKey || platformLocalOnlyKeys[key] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad key"})
		return
	}
	if isResident(c) && residentReadableKeys[key] {
		forbidden(c, "read_only")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, platformMaxBody)
	var req putDocReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {version, value}"})
		return
	}
	scope := platformScopeFor(c, key, req.Scope)
	if isResident(c) {
		if h.residentOf(c) == "" {
			forbidden(c, "not_resident")
			return
		}
		scope = "user:" + platformUser(c) // всё, что пишет резидент, только его
	}
	out, err := h.repo.PutDoc(c.Request.Context(), scope, key, req.Version, req.Value, req.Deleted, platformUser(c))
	if errors.Is(err, pg.ErrPlatformConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "conflict", "current": out})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, out)
}

type importReq struct {
	Storage map[string]string `json:"storage"`
}

type importReport struct {
	BoardsAdded      []string `json:"boardsAdded"`
	BoardsUpdated    []string `json:"boardsUpdated"`
	BoardsSame       []string `json:"boardsSame"`
	BoardsKeptServer []string `json:"boardsKeptServer"`
	DocsAdded        []string `json:"docsAdded"`
	DocsMerged       []string `json:"docsMerged"`
	DocsSame         []string `json:"docsSame"`
	DocsKeptServer   []string `json:"docsKeptServer"`
	Skipped          []string `json:"skipped"`
}

// Import godoc
// @Summary      Move a browser's data to the server
// @Description  Body = the platform's localStorage dump. Boards are matched by id; the newer copy wins and the older one is kept in history. Sections already on the server are not overwritten and are listed in the report.
// @Tags         platform
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Success      200 {object} map[string]any
// @Router       /api/v1/platform/import [post]
func (h *PlatformHandler) Import(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4*platformMaxBody)
	var req importReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Storage == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be {storage:{key:value}}"})
		return
	}
	ctx := c.Request.Context()
	by := platformUser(c)
	rep := importReport{
		BoardsAdded: []string{}, BoardsUpdated: []string{}, BoardsSame: []string{}, BoardsKeptServer: []string{},
		DocsAdded: []string{}, DocsMerged: []string{}, DocsSame: []string{}, DocsKeptServer: []string{}, Skipped: []string{},
	}

	// Boards
	if raw, ok := req.Storage["bs_boards"]; ok && strings.TrimSpace(raw) != "" {
		var boards []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &boards); err != nil {
			rep.Skipped = append(rep.Skipped, "bs_boards: не читается как список")
		}
		for _, b := range boards {
			var head struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Updated string `json:"updated"`
			}
			if json.Unmarshal(b, &head) != nil || !platformIDRe.MatchString(head.ID) {
				rep.Skipped = append(rep.Skipped, "разбор без id")
				continue
			}
			label := head.Name
			if label == "" {
				label = head.ID
			}
			cur, err := h.repo.GetBoard(ctx, head.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			switch {
			case cur == nil:
				if _, err := h.repo.PutBoard(ctx, head.ID, 0, b, by); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
					return
				}
				rep.BoardsAdded = append(rep.BoardsAdded, label)
			case cur.Deleted:
				rep.BoardsKeptServer = append(rep.BoardsKeptServer, label+" (удалён на сервере)")
			case pgJSONEqual(cur.Data, b):
				rep.BoardsSame = append(rep.BoardsSame, label)
			case newerISO(head.Updated, boardUpdated(cur.Data)):
				if _, err := h.repo.PutBoard(ctx, head.ID, cur.Version, b, by); err != nil && !errors.Is(err, pg.ErrPlatformConflict) {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
					return
				}
				rep.BoardsUpdated = append(rep.BoardsUpdated, label)
			default:
				rep.BoardsKeptServer = append(rep.BoardsKeptServer, label)
			}
		}
	}

	// Sections
	keys := make([]string, 0, len(req.Storage))
	for k := range req.Storage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "bs_boards" || k == platformSeedKey || platformLocalOnlyKeys[k] {
			continue
		}
		if !platformKeyRe.MatchString(k) {
			rep.Skipped = append(rep.Skipped, k)
			continue
		}
		v := req.Storage[k]
		scope := platformScopeFor(c, k, "")
		cur, err := h.repo.GetDoc(ctx, scope, k)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			return
		}
		switch {
		case cur == nil || cur.Deleted:
			base := 0
			if cur != nil {
				base = cur.Version
			}
			if _, err := h.repo.PutDoc(ctx, scope, k, base, v, false, by); err != nil && !errors.Is(err, pg.ErrPlatformConflict) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			rep.DocsAdded = append(rep.DocsAdded, k)
		case cur.Value == v:
			rep.DocsSame = append(rep.DocsSame, k)
		default:
			// Both sides have data: keep everything the server has and add
			// what only the file has. Nothing on the server is overwritten.
			merged, changed := unionJSON(cur.Value, v)
			if !changed {
				rep.DocsKeptServer = append(rep.DocsKeptServer, k)
				continue
			}
			if _, err := h.repo.PutDoc(ctx, scope, k, cur.Version, merged, false, by); err != nil {
				if errors.Is(err, pg.ErrPlatformConflict) {
					rep.DocsKeptServer = append(rep.DocsKeptServer, k+" (изменён во время переноса, повторите)")
					continue
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			rep.DocsMerged = append(rep.DocsMerged, k)
		}
	}

	c.JSON(http.StatusOK, rep)
}

func boardUpdated(data json.RawMessage) string {
	var h struct {
		Updated string `json:"updated"`
	}
	_ = json.Unmarshal(data, &h)
	return h.Updated
}

// newerISO reports whether a is a later ISO timestamp than b.
// An unreadable timestamp never wins.
func newerISO(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	if errA != nil {
		return false
	}
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errB != nil {
		return true
	}
	return ta.After(tb)
}

func pgJSONEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

// unionJSON adds to `server` whatever only `incoming` has, never changing or
// removing what the server already holds:
//   - lists of records with "id": records whose id the server lacks are appended;
//   - other lists: elements the server lacks are appended;
//   - objects: missing keys are added, shared keys are merged the same way;
//   - plain values: the server's wins, unless it is empty.
//
// Returns the merged JSON and whether anything was added.
func unionJSON(server, incoming string) (string, bool) {
	var a, b any
	errA := json.Unmarshal([]byte(server), &a)
	errB := json.Unmarshal([]byte(incoming), &b)
	if errA != nil || errB != nil {
		if strings.TrimSpace(server) == "" && strings.TrimSpace(incoming) != "" {
			return incoming, true
		}
		return server, false
	}
	out, changed := unionValue(a, b)
	if !changed {
		return server, false
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return server, false
	}
	return string(buf), true
}

func unionValue(a, b any) (any, bool) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return a, false
		}
		changed := false
		keys := make([]string, 0, len(bv))
		for k := range bv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if cur, ok := av[k]; ok {
				if m, ch := unionValue(cur, bv[k]); ch {
					av[k] = m
					changed = true
				}
			} else {
				av[k] = bv[k]
				changed = true
			}
		}
		return av, changed
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return a, false
		}
		seen := map[string]bool{}
		for _, el := range av {
			seen[elemKey(el)] = true
		}
		changed := false
		for _, el := range bv {
			k := elemKey(el)
			if !seen[k] {
				av = append(av, el)
				seen[k] = true
				changed = true
			}
		}
		return av, changed
	default:
		if isEmptyJSON(a) && !isEmptyJSON(b) {
			return b, true
		}
		return a, false
	}
}

// elemKey identifies a list element: its "id" when it is a record with one,
// otherwise its whole content.
func elemKey(el any) string {
	if m, ok := el.(map[string]any); ok {
		if id, ok := m["id"]; ok && id != nil && id != "" {
			buf, _ := json.Marshal(id)
			return "id:" + string(buf)
		}
	}
	buf, _ := json.Marshal(el)
	return "v:" + string(buf)
}

func isEmptyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case float64:
		return t == 0
	case bool:
		return !t
	}
	return false
}

// DocVersions: earlier states of a section, newest first. Team only.
// GET /api/v1/platform/docs/:key/versions[?scope=me]
func (h *PlatformHandler) DocVersions(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	key := c.Param("key")
	list, err := h.repo.DocVersions(c.Request.Context(), platformScopeFor(c, key, c.Query("scope")), key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list})
}

// DocVersion: one earlier state with its value.
// GET /api/v1/platform/docs/:key/versions/:version
func (h *PlatformHandler) DocVersion(c *gin.Context) {
	if isResident(c) {
		forbidden(c, "team_only")
		return
	}
	v, err := strconv.Atoi(c.Param("version"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad version"})
		return
	}
	key := c.Param("key")
	out, err := h.repo.DocVersion(c.Request.Context(), platformScopeFor(c, key, c.Query("scope")), key, v)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if out == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, out)
}
