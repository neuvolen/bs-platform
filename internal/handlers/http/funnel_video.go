package http

// R55: «Видео в воронке» (Маркетинг → SMM → Воронка).
//
// A small library of videos the bot sends to leads at chosen steps of the
// funnel. The files live in Postgres (platform_files, «fv-…»), the list in
// the server doc bs_funnel_videos:
//
//	{videos:[{id, title, topic, step, caption, on, file, name, size, w, h, dur,
//	          tgFileId, source, createdAt, sent, lastSentAt}], removed:[seed files]}
//
// Steps (fvSteps): start (20 minutes after /start, the 99 checklists given),
// d1 … d14 (with that day's warm-up touch), booked (booked the
// express-разбор), offer (after the разбор, with the club offer). A lead gets
// one video per step, once (the CRM card keeps fv:{step: time} and a line in
// the log); the first switched-on video of a step wins. Videos of the start
// and warm-up steps carry the express-разбор button.
//
// The first upload to Telegram gives a file_id; it is kept in the list, so
// the file goes up once and every next lead gets it at once.
//
// Seeds: web/funnel_video/*.mp4 with videos.json (package funnelvideo) are
// copied in once at start; the platform's changes win after that.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

const (
	fvDoc      = "bs_funnel_videos"
	fvMaxBytes = 20 << 20 // Telegram takes up to 50 MB from a bot; 20 keeps Postgres light
	fvMaxItems = 40
	// fvStartDelay: the start video comes a little after the welcome, not on top of it.
	fvStartDelay = 20 * time.Minute
	fvStartLate  = 24 * time.Hour // a lead who started earlier does not get it any more
)

type fvStep struct {
	Key, Name, Hint string
	CTA             bool // the express-разбор button under the video
}

var fvSteps = []fvStep{
	{"start", "Сразу после старта", "через 20 минут после /start, когда чек-листы уже выданы", true},
	{"d1", "Прогрев, день 1", "вместе с касанием 1 дня", true},
	{"d3", "Прогрев, день 3", "вместе с касанием 3 дня", true},
	{"d7", "Прогрев, день 7", "вместе с касанием 7 дня (приглашение на разбор)", true},
	{"d10", "Прогрев, день 10", "вместе с касанием 10 дня (кейс)", true},
	{"d14", "Прогрев, день 14", "вместе с последним касанием", true},
	{"booked", "Записался на разбор", "сразу после подтверждения записи: что будет на разборе", false},
	{"offer", "После разбора", "вместе с PDF итогов и предложением клуба", false},
}

func fvStepBy(k string) *fvStep {
	for i := range fvSteps {
		if fvSteps[i].Key == k {
			return &fvSteps[i]
		}
	}
	return nil
}

// fvWarmStep: the step of the warm-up touch number stage (0-based).
func fvWarmStep(stage int) string {
	steps := warmSteps()
	if stage < 0 || stage >= len(steps) {
		return ""
	}
	return "d" + strconv.Itoa(int(steps[stage].day))
}

type fvItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Topic      string `json:"topic,omitempty"`
	Step       string `json:"step"`
	Caption    string `json:"caption"`
	On         bool   `json:"on"`
	File       string `json:"file"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	W          int    `json:"w,omitempty"`
	H          int    `json:"h,omitempty"`
	Dur        int    `json:"dur,omitempty"`
	TgFileID   string `json:"tgFileId,omitempty"`
	Source     string `json:"source"` // seed:<file> | upload
	CreatedAt  string `json:"createdAt"`
	By         string `json:"by,omitempty"`
	Sent       int    `json:"sent"`
	LastSentAt string `json:"lastSentAt,omitempty"`
}

type fvLib struct {
	Videos  []*fvItem `json:"videos"`
	Removed []string  `json:"removed,omitempty"`
}

type fvFiles interface {
	PutFile(ctx context.Context, f pg.PlatformFile, by string) error
	GetFile(ctx context.Context, id string) (*pg.PlatformFile, error)
	DeleteFile(ctx context.Context, id string) error
}

// FunnelVideos: the library (nil-safe where the funnel calls it).
type FunnelVideos struct {
	docs   funnelDocs
	files  fvFiles
	secret []byte
	now    func() time.Time
	mu     sync.Mutex // one upload to Telegram at a time per process
}

func NewFunnelVideos(docs funnelDocs, files fvFiles, secret []byte) *FunnelVideos {
	return &FunnelVideos{docs: docs, files: files, secret: secret, now: time.Now}
}

func (v *FunnelVideos) load(ctx context.Context) (*fvLib, error) {
	lib := &fvLib{}
	d, err := v.docs.GetDoc(ctx, "server", fvDoc)
	if err != nil {
		return nil, err
	}
	if d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), lib)
	}
	return lib, nil
}

// update: read, change, write (retried on a conflict).
func (v *FunnelVideos) update(ctx context.Context, fn func(l *fvLib) bool) error {
	for try := 0; try < 6; try++ {
		lib := &fvLib{}
		base := 0
		d, err := v.docs.GetDoc(ctx, "server", fvDoc)
		if err != nil {
			return err
		}
		if d != nil {
			base = d.Version
			if !d.Deleted {
				_ = json.Unmarshal([]byte(d.Value), lib)
			}
		}
		if !fn(lib) {
			return nil
		}
		b, _ := json.Marshal(lib)
		if _, err := v.docs.PutDoc(ctx, "server", fvDoc, base, string(b), false, "server:funnel-video"); err == nil {
			return nil
		}
		time.Sleep(time.Duration(20*(try+1)) * time.Millisecond)
	}
	return pg.ErrPlatformConflict
}

// ForStep: the video a lead gets at the step (nil: none).
func (v *FunnelVideos) ForStep(ctx context.Context, step string) *fvItem {
	if v == nil {
		return nil
	}
	lib, err := v.load(ctx)
	if err != nil {
		return nil
	}
	for _, it := range lib.Videos {
		if it.On && it.Step == step && it.File != "" {
			cp := *it
			return &cp
		}
	}
	return nil
}

// Steps: the steps that have a video now.
func (v *FunnelVideos) Steps(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if v == nil {
		return out
	}
	lib, err := v.load(ctx)
	if err != nil {
		return out
	}
	for _, it := range lib.Videos {
		if it.On && it.Step != "" && it.File != "" {
			out[it.Step] = true
		}
	}
	return out
}

func newFVID() string {
	return "fv" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func fileHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

type fvManifest struct {
	Videos []struct {
		File    string `json:"file"`
		Title   string `json:"title"`
		Topic   string `json:"topic"`
		Step    string `json:"step"`
		Caption string `json:"caption"`
		W       int    `json:"w"`
		H       int    `json:"h"`
		Dur     int    `json:"dur"`
	} `json:"videos"`
}

// Seed copies the shipped videos in once (fsys: web/funnel_video). A video
// without a line in videos.json comes in switched off, without a step.
func (v *FunnelVideos) Seed(ctx context.Context, fsys fs.FS) (int, error) {
	if v == nil || fsys == nil {
		return 0, nil
	}
	var man fvManifest
	if b, err := fs.ReadFile(fsys, "videos.json"); err == nil {
		if err := json.Unmarshal(b, &man); err != nil {
			return 0, fmt.Errorf("videos.json: %w", err)
		}
	}
	names, _ := fs.Glob(fsys, "*.mp4")
	lib, err := v.load(ctx)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, it := range lib.Videos {
		have[it.Source] = true
	}
	for _, r := range lib.Removed {
		have["seed:"+r] = true
	}
	added := 0
	for _, name := range names {
		if have["seed:"+name] {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil || len(data) == 0 || len(data) > fvMaxBytes {
			log.Printf("funnel video: seed %s skipped (%d bytes, %v)", name, len(data), err)
			continue
		}
		it := &fvItem{ID: newFVID(), Title: strings.TrimSuffix(name, path.Ext(name)), Name: name, Size: int64(len(data)),
			Source: "seed:" + name, CreatedAt: v.now().UTC().Format(time.RFC3339), By: "repo"}
		for _, m := range man.Videos {
			if m.File == name {
				it.Title, it.Topic, it.Caption, it.W, it.H, it.Dur = m.Title, m.Topic, noLongDash(m.Caption), m.W, m.H, m.Dur
				if fvStepBy(m.Step) != nil {
					it.Step, it.On = m.Step, true
				}
			}
		}
		it.File = "fv-" + fileHash(data)
		if f, _ := v.files.GetFile(ctx, it.File); f == nil {
			if err := v.files.PutFile(ctx, pg.PlatformFile{ID: it.File, Name: name, Mime: "video/mp4", Data: data}, "server:funnel-video"); err != nil {
				return added, err
			}
		}
		err = v.update(ctx, func(l *fvLib) bool {
			for _, x := range l.Videos {
				if x.Source == it.Source {
					return false
				}
			}
			if it.On { // a step already taken by the team's video: this one waits switched off
				for _, x := range l.Videos {
					if x.On && x.Step == it.Step {
						it.On = false
					}
				}
			}
			l.Videos = append(l.Videos, it)
			return true
		})
		if err != nil {
			return added, err
		}
		added++
		log.Printf("funnel video: %s added from the repo (step %q, on %v, %d KB)", name, it.Step, it.On, it.Size>>10)
	}
	return added, nil
}

// ── sending ──

// sendStepVideo sends the step's video to a lead once; true when it went.
// first: the lead's first name for {имя}.
func (f *LeadFunnel) sendStepVideo(ctx context.Context, tg int64, step, first string) bool {
	if f.Videos == nil || f.Video == nil || tg == 0 {
		return false
	}
	it := f.Videos.ForStep(ctx, step)
	if it == nil {
		return false
	}
	// once per lead and step (the CRM card)
	already, found := false, false
	_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
		leads, _ := crm["leads"].([]any)
		lead := findLeadByTg(leads, tg)
		if lead == nil {
			return false
		}
		found = true
		fv, _ := lead["fv"].(map[string]any)
		if fv != nil && fv[step] != nil {
			already = true
			return false
		}
		if fv == nil {
			fv = map[string]any{}
		}
		fv[step] = f.now().UTC().Format(time.RFC3339)
		lead["fv"] = fv
		addLog(lead, f.now(), "Бот прислал видео «"+it.Title+"» ("+fvStepBy(step).Name+")")
		return true
	})
	if !found || already {
		return false
	}
	caption := fvCaption(it.Caption, first)
	if len([]rune(caption)) > 1000 {
		caption = string([]rune(caption)[:1000])
	}
	var keys map[string]any
	if s := fvStepBy(step); s != nil && s.CTA {
		keys = kb(row(f.appBtn("📅 Записаться на экспресс-разбор", "razbor")), row(f.appBtn("📘 99 гайдов", "checklists")))
	}
	if err := f.Videos.send(ctx, f.Video, tg, it, caption, keys); err != nil {
		log.Printf("funnel video: %s → %d: %v", it.ID, tg, err)
		// not sent: the mark goes, the next pass may try again
		_ = f.mutate(ctx, "bs_crm", func(crm map[string]any) bool {
			leads, _ := crm["leads"].([]any)
			if lead := findLeadByTg(leads, tg); lead != nil {
				if fv, _ := lead["fv"].(map[string]any); fv != nil {
					delete(fv, step)
					return true
				}
			}
			return false
		})
		return false
	}
	if f.dlgOn {
		f.noteDialog(ctx, tg, "out", "[видео «"+it.Title+"»] "+caption, dlgButtons(keys))
	}
	return true
}

// fvCaption: {имя} filled; without a name «{имя}, » goes and the text
// starts with a capital letter.
func fvCaption(t, first string) string {
	if first != "" {
		return strings.TrimSpace(strings.ReplaceAll(t, "{имя}", first))
	}
	t = strings.ReplaceAll(strings.ReplaceAll(t, "{имя}, ", ""), "{имя}", "")
	t = strings.TrimSpace(t)
	if r := []rune(t); len(r) > 0 {
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		t = string(r)
	}
	return t
}

// VideoSender: the bot's sendVideo (bot.Service.SendVideoKB).
type VideoSender func(ctx context.Context, chatID int64, key, name string, data []byte, fileID, caption string, kb map[string]any, m bot.VideoMeta) (string, error)

// send: by the kept Telegram file_id, else the file from Postgres (and the
// new file_id is kept).
func (v *FunnelVideos) send(ctx context.Context, send VideoSender, tg int64, it *fvItem, caption string, keys map[string]any) error {
	meta := bot.VideoMeta{W: it.W, H: it.H, Dur: it.Dur}
	if it.TgFileID != "" {
		if _, err := send(ctx, tg, "fv:"+it.ID, it.Name, nil, it.TgFileID, caption, keys, meta); err == nil {
			v.noteSent(ctx, it.ID, "")
			return nil
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	f, err := v.files.GetFile(ctx, it.File)
	if err != nil {
		return err
	}
	if f == nil {
		return errors.New("файла видео нет в хранилище")
	}
	id, err := send(ctx, tg, "fv:"+it.ID, it.Name, f.Data, "", caption, keys, meta)
	if err != nil {
		return err
	}
	v.noteSent(ctx, it.ID, id)
	return nil
}

func (v *FunnelVideos) noteSent(ctx context.Context, id, tgFileID string) {
	_ = v.update(ctx, func(l *fvLib) bool {
		for _, x := range l.Videos {
			if x.ID == id {
				x.Sent++
				x.LastSentAt = v.now().UTC().Format(time.RFC3339)
				if tgFileID != "" {
					x.TgFileID = tgFileID
				}
				return true
			}
		}
		return false
	})
}

// startVideoDue: a lead of the bot who started 20 minutes to a day ago and
// has not had the start video.
func startVideoDue(m map[string]any, now time.Time) bool {
	if m["funnel"] != "bot" || m["warmStop"] == true || !warmCols[fmt.Sprint(m["col"])] {
		return false
	}
	if fv, _ := m["fv"].(map[string]any); fv != nil && fv["start"] != nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, fmt.Sprint(m["startAt"]))
	if err != nil {
		return false
	}
	age := now.Sub(t)
	return age >= fvStartDelay && age < fvStartLate
}

// ── the platform ──

func (v *FunnelVideos) sign(id string) string {
	m := hmac.New(sha256.New, v.secret)
	m.Write([]byte("fv:" + id))
	return hex.EncodeToString(m.Sum(nil))[:20]
}

func (v *FunnelVideos) fileURL(id string) string {
	return "/api/v1/public/fv/" + id + "-" + v.sign(id) + ".mp4"
}

func (v *FunnelVideos) view(it *fvItem) gin.H {
	st := fvStepBy(it.Step)
	stepName := ""
	if st != nil {
		stepName = st.Name
	}
	return gin.H{"id": it.ID, "title": it.Title, "topic": it.Topic, "step": it.Step, "stepName": stepName, "caption": it.Caption,
		"on": it.On, "size": it.Size, "w": it.W, "h": it.H, "dur": it.Dur, "source": it.Source, "createdAt": it.CreatedAt,
		"sent": it.Sent, "lastSentAt": it.LastSentAt, "cached": it.TgFileID != "", "url": v.fileURL(it.ID)}
}

// List: GET /api/v1/platform/funnel/videos
func (v *FunnelVideos) List(c *gin.Context) {
	lib, err := v.load(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	steps := []gin.H{}
	for _, s := range fvSteps {
		steps = append(steps, gin.H{"key": s.Key, "name": s.Name, "hint": s.Hint, "cta": s.CTA})
	}
	vids := []gin.H{}
	for _, it := range lib.Videos {
		vids = append(vids, v.view(it))
	}
	c.JSON(http.StatusOK, gin.H{"steps": steps, "videos": vids, "maxMB": fvMaxBytes >> 20})
}

var fvNameRe = regexp.MustCompile(`[^\p{L}\p{N}._ -]+`)

// Upload: POST /api/v1/platform/funnel/videos (multipart: file, title, step, caption, w, h, dur)
func (v *FunnelVideos) Upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, fvMaxBytes+(1<<20))
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_file", "message": fmt.Sprintf("Нужен файл MP4 до %d МБ", fvMaxBytes>>20)})
		return
	}
	if fh.Size > fvMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "too_big", "message": fmt.Sprintf("Видео больше %d МБ: сожмите до 720×1280", fvMaxBytes>>20)})
		return
	}
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_file"})
		return
	}
	data, _ := io.ReadAll(io.LimitReader(src, fvMaxBytes+1))
	src.Close()
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_mp4", "message": "Нужен файл MP4 (H.264): Telegram покажет его как видео"})
		return
	}
	name := strings.TrimSpace(fvNameRe.ReplaceAllString(path.Base(fh.Filename), ""))
	if name == "" || !strings.HasSuffix(strings.ToLower(name), ".mp4") {
		name = "video.mp4"
	}
	title := strings.TrimSpace(noLongDash(c.PostForm("title")))
	if title == "" {
		title = strings.TrimSuffix(name, path.Ext(name))
	}
	step := c.PostForm("step")
	if fvStepBy(step) == nil {
		step = ""
	}
	atoi := func(k string) int { n, _ := strconv.Atoi(c.PostForm(k)); return n }
	it := &fvItem{ID: newFVID(), Title: clip(title, 120), Topic: clip(strings.TrimSpace(c.PostForm("topic")), 200), Step: step,
		Caption: clip(strings.TrimSpace(noLongDash(c.PostForm("caption"))), 1000), On: step != "", Name: name, Size: int64(len(data)),
		W: atoi("w"), H: atoi("h"), Dur: atoi("dur"), Source: "upload", CreatedAt: v.now().UTC().Format(time.RFC3339), By: platformUser(c),
		File: "fv-" + fileHash(data)}
	ctx := c.Request.Context()
	if f, _ := v.files.GetFile(ctx, it.File); f == nil {
		if err := v.files.PutFile(ctx, pg.PlatformFile{ID: it.File, Name: name, Mime: "video/mp4", Data: data}, platformUser(c)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
			return
		}
	}
	full := false
	err = v.update(ctx, func(l *fvLib) bool {
		if len(l.Videos) >= fvMaxItems {
			full = true
			return false
		}
		if it.On {
			for _, x := range l.Videos {
				if x.On && x.Step == it.Step {
					x.On = false // the new video takes the step
				}
			}
		}
		l.Videos = append(l.Videos, it)
		return true
	})
	if full {
		c.JSON(http.StatusConflict, gin.H{"error": "full", "message": fmt.Sprintf("В библиотеке уже %d видео: удалите старые", fvMaxItems)})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	c.JSON(http.StatusOK, v.view(it))
}

// Put: PUT /api/v1/platform/funnel/videos/:id {title, topic, caption, step, on}
func (v *FunnelVideos) Put(c *gin.Context) {
	var in struct {
		Title   *string `json:"title"`
		Topic   *string `json:"topic"`
		Caption *string `json:"caption"`
		Step    *string `json:"step"`
		On      *bool   `json:"on"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	if in.Step != nil && *in.Step != "" && fvStepBy(*in.Step) == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_step"})
		return
	}
	id := c.Param("id")
	var out *fvItem
	err := v.update(c.Request.Context(), func(l *fvLib) bool {
		var it *fvItem
		for _, x := range l.Videos {
			if x.ID == id {
				it = x
			}
		}
		if it == nil {
			return false
		}
		if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
			it.Title = clip(strings.TrimSpace(noLongDash(*in.Title)), 120)
		}
		if in.Topic != nil {
			it.Topic = clip(strings.TrimSpace(*in.Topic), 200)
		}
		if in.Caption != nil {
			it.Caption = clip(strings.TrimSpace(noLongDash(*in.Caption)), 1000)
		}
		if in.Step != nil {
			it.Step = *in.Step
		}
		if in.On != nil {
			it.On = *in.On
		}
		if it.Step == "" {
			it.On = false
		}
		if it.On { // one video per step: the others of the step wait
			for _, x := range l.Videos {
				if x != it && x.On && x.Step == it.Step {
					x.On = false
				}
			}
		}
		out = it
		return true
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	if out == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	v.List(c)
}

// Delete: DELETE /api/v1/platform/funnel/videos/:id (a seed is not brought back).
func (v *FunnelVideos) Delete(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	var gone *fvItem
	err := v.update(ctx, func(l *fvLib) bool {
		for i, x := range l.Videos {
			if x.ID == id {
				gone = x
				l.Videos = append(l.Videos[:i], l.Videos[i+1:]...)
				if strings.HasPrefix(x.Source, "seed:") {
					l.Removed = append(l.Removed, strings.TrimPrefix(x.Source, "seed:"))
				}
				return true
			}
		}
		return false
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage"})
		return
	}
	if gone == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	// the file goes unless another entry shares it
	if lib, err := v.load(ctx); err == nil {
		shared := false
		for _, x := range lib.Videos {
			if x.File == gone.File {
				shared = true
			}
		}
		if !shared {
			_ = v.files.DeleteFile(ctx, gone.File)
		}
	}
	v.List(c)
}

// PublicFile: GET /api/v1/public/fv/<id>-<sig>.mp4 (the preview in the platform; Range works).
func (v *FunnelVideos) PublicFile(c *gin.Context) {
	name := strings.TrimSuffix(c.Param("name"), ".mp4")
	i := strings.LastIndex(name, "-")
	if i <= 0 || !hmac.Equal([]byte(name[i+1:]), []byte(v.sign(name[:i]))) {
		c.String(http.StatusNotFound, "Нет такого видео")
		return
	}
	id := name[:i]
	ctx := c.Request.Context()
	lib, err := v.load(ctx)
	if err != nil {
		c.String(http.StatusServiceUnavailable, "Хранилище недоступно")
		return
	}
	for _, x := range lib.Videos {
		if x.ID != id {
			continue
		}
		f, err := v.files.GetFile(ctx, x.File)
		if err != nil || f == nil {
			c.String(http.StatusNotFound, "Нет такого видео")
			return
		}
		c.Header("Content-Type", "video/mp4")
		c.Header("Cache-Control", "private, max-age=3600")
		http.ServeContent(c.Writer, c.Request, x.Name, time.Time{}, bytes.NewReader(f.Data))
		return
	}
	c.String(http.StatusNotFound, "Нет такого видео")
}
