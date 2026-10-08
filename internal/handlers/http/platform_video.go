package http

// R54: Маркетинг → SMM → «Видео»: монтаж Reels на сервере (internal/video).
//
// Клипы приходят кусками по 8 МБ (PUT …/uploads/:id?off=N) в VIDEO_DIR:
// /data/bs-video, если у сервиса есть том Railway на /data, иначе временная
// папка контейнера (до следующего деплоя). Готовый ролик до 80 МБ и обложка
// дополнительно кладутся в platform_files (Postgres), поэтому ссылка
// «в план SMM» переживает деплой. Ролик и обложка отдаются по подписанной
// ссылке /api/v1/platform/video/f/<id>-<подпись>.mp4: её понимает <video> и
// её можно отдать в план без входа.
//
// ffmpeg: системный, если есть в образе (Railpack: RAILPACK_DEPLOY_APT_PACKAGES=ffmpeg),
// иначе статическая сборка, которую сервер скачивает сам при старте
// (та же, что у распознавания разборов, ai.LocalASR.FFmpeg). Нужны фильтры
// ass (libass), loudnorm, sidechaincompress и кодек libx264: проверяются при
// старте, итог в логе «video: ffmpeg …» и в «Видео» на странице.
//
//	VIDEO_DIR       папка загрузок и роликов
//	VIDEO_FFMPEG    путь к своему ffmpeg
//	VIDEO_PRELOAD=0 не готовить ffmpeg при старте
//	VIDEO_TIMEOUT   минуты на одну сборку (30)

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/datadir"
	"github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/bnursik/business_surgery_backend/internal/tplpdf"
	"github.com/bnursik/business_surgery_backend/internal/video"
	"github.com/gin-gonic/gin"
)

type PlatformVideo struct {
	M      *video.Manager
	repo   *pg.PlatformRepo
	asr    *ai.LocalASR
	secret []byte

	mu      sync.Mutex
	ffPath  string
	ffSrc   string // system | static | env
	ffFeat  video.Features
	ffErr   string
	ffState string // "" | preparing | ready | error
}

func videoDir() string {
	if d := strings.TrimSpace(os.Getenv("VIDEO_DIR")); d != "" {
		return d
	}
	return datadir.Path("bs-video") // R55: the Railway volume bs-data at /data (or BS_DATA_DIR)
}

// videoMaxBytes (R55): the cap of the video folder, VIDEO_MAX_MB (2500 by
// default: the 5 GB volume also holds the Whisper model, about 0.6 GB).
func videoMaxBytes() int64 {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VIDEO_MAX_MB"))); err == nil && n > 0 {
		return int64(n) << 20
	}
	return 2500 << 20
}

// NewPlatformVideo: nil when the folder cannot be made (the page says so).
func NewPlatformVideo(repo *pg.PlatformRepo, asr *ai.LocalASR, secret []byte) *PlatformVideo {
	m, err := video.NewManager(videoDir())
	if err != nil {
		log.Printf("video: folder %s: %v", videoDir(), err)
		return nil
	}
	m.MaxBytes = videoMaxBytes()
	log.Printf("data: video folder %s (persistent %v, writable %v, cap %d MB, used %d MB, disk free %d MB)",
		m.Dir, datadir.Persistent(m.Dir), datadir.Writable(m.Dir), m.MaxBytes>>20, m.Used()>>20, m.Free()>>20)
	if n, err := strconv.Atoi(os.Getenv("VIDEO_TIMEOUT")); err == nil && n > 0 {
		m.Timeout = time.Duration(n) * time.Minute
	}
	v := &PlatformVideo{M: m, repo: repo, asr: asr, secret: secret}
	m.Tools = v.tools
	if repo != nil {
		m.Store = v.store
	}
	return v
}

// Start: the worker, and ffmpeg found or downloaded now so the log says
// what the server has.
func (v *PlatformVideo) Start(ctx context.Context) {
	v.M.Start(ctx)
	if os.Getenv("VIDEO_PRELOAD") == "0" {
		return
	}
	go func() {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if _, err := v.ffmpeg(pctx); err != nil {
			log.Printf("video: ffmpeg not ready: %v", err)
		}
	}()
}

// ffmpeg: the binary and its features (once; a failure is retried next time).
func (v *PlatformVideo) ffmpeg(ctx context.Context) (string, error) {
	v.mu.Lock()
	if v.ffState == "ready" {
		p := v.ffPath
		v.mu.Unlock()
		return p, nil
	}
	v.ffState = "preparing"
	v.mu.Unlock()
	path, src, err := "", "", error(nil)
	if p := strings.TrimSpace(os.Getenv("VIDEO_FFMPEG")); p != "" {
		path, src = p, "env"
	} else if p, e := exec.LookPath("ffmpeg"); e == nil {
		path, src = p, "system"
	} else if v.asr != nil {
		path, src = "", "static"
		path, err = v.asr.FFmpeg(ctx)
	} else {
		err = errors.New("в образе нет ffmpeg, а скачивание выключено вместе с ASR_LOCAL=0")
	}
	var feat video.Features
	if err == nil {
		feat, err = video.Check(ctx, video.Exec{Bin: path})
		if err == nil && len(feat.Missing()) > 0 {
			err = fmt.Errorf("в этой сборке ffmpeg нет: %s", strings.Join(feat.Missing(), ", "))
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ffPath, v.ffSrc, v.ffFeat = path, src, feat
	if err != nil {
		v.ffState, v.ffErr = "error", err.Error()
		return "", err
	}
	v.ffState, v.ffErr = "ready", ""
	log.Printf("video: ffmpeg %s (%s): ass, libx264, loudnorm, sidechaincompress ok; files in %s", path, src, v.M.Dir)
	return path, nil
}

func (v *PlatformVideo) tools(ctx context.Context) (video.Tools, error) {
	p, err := v.ffmpeg(ctx)
	if err != nil {
		return video.Tools{}, fmt.Errorf("ffmpeg не готов: %v", err)
	}
	t := video.Tools{FF: video.Exec{Bin: p}, Font: tplpdf.FontHeavy(), Logo: tplpdf.LogoWhite()}
	if v.asr != nil {
		a := v.asr
		t.ASR = func(ctx context.Context, wav string) ([]video.Seg, error) {
			ss, err := a.TranscribeTimed(ctx, wav)
			out := make([]video.Seg, 0, len(ss))
			for _, s := range ss {
				out = append(out, video.Seg{Start: s.Start, End: s.End, Text: s.Text})
			}
			return out, err
		}
	} else {
		t.ASRNote = "распознавание речи на сервере выключено (ASR_LOCAL=0)"
	}
	return t, nil
}

// store keeps a result of up to 80 MB in platform_files.
func (v *PlatformVideo) store(ctx context.Context, j *video.Job, out, cover string) (string, string) {
	st, err := os.Stat(out)
	if err != nil || st.Size() > platformFileMax {
		return "", ""
	}
	day := time.Now().In(time.FixedZone("ALMT", 5*3600)).Format("2006-01-02")
	put := func(path, name, mt string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		f := pg.PlatformFile{ID: newID(), Name: name, Mime: mt, Data: b}
		if err := v.repo.PutFile(ctx, f, j.By); err != nil {
			log.Printf("video: keep %s: %v", name, err)
			return ""
		}
		return f.ID
	}
	fid := put(out, "reels-"+day+".mp4", "video/mp4")
	if fid == "" {
		return "", ""
	}
	return fid, put(cover, "reels-"+day+".png", "image/png")
}

// ── links ──

func (v *PlatformVideo) sig(key string) string {
	h := hmac.New(sha256.New, v.secret)
	h.Write([]byte("r54video:" + key))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// link: j<job> (on disk) or p<file> (Postgres) + signature.
func (v *PlatformVideo) link(key, ext string) string {
	return "/api/v1/platform/video/f/" + key + "-" + v.sig(key) + "." + ext
}

type videoJobView struct {
	video.Job
	Video    string `json:"video,omitempty"`
	Cover    string `json:"cover,omitempty"`
	Kept     bool   `json:"kept"` // survives a deploy
	Template string `json:"templateName,omitempty"`
}

func (v *PlatformVideo) view(j video.Job) videoJobView {
	o := videoJobView{Job: j}
	if t, ok := video.TemplateByID(j.Opts.Template); ok {
		o.Template = t.Name
	}
	if j.Status != "done" {
		return o
	}
	if j.FileID != "" {
		o.Video, o.Kept = v.link("p"+j.FileID, "mp4"), true
		if j.CoverID != "" {
			o.Cover = v.link("p"+j.CoverID, "png")
		}
	} else {
		o.Video, o.Cover = v.link("j"+j.ID, "mp4"), v.link("j"+j.ID, "png")
	}
	return o
}

// ── handlers ──

func (v *PlatformVideo) Status(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	v.mu.Lock()
	ff := gin.H{"state": v.ffState, "source": v.ffSrc, "error": v.ffErr}
	v.mu.Unlock()
	asr := gin.H{"on": v.asr != nil}
	if v.asr != nil {
		asr["model"], asr["state"] = v.asr.Model, v.asr.State()
	}
	jobs := []videoJobView{}
	for _, j := range v.M.Jobs() {
		jobs = append(jobs, v.view(j))
	}
	persistent := datadir.Persistent(v.M.Dir)
	c.JSON(http.StatusOK, gin.H{
		"ffmpeg": ff, "asr": asr, "templates": video.Templates, "cta": video.DefaultCTA, "jobs": jobs,
		"limits": gin.H{"fileMB": video.MaxUpload >> 20, "chunkMB": 8, "inputSec": int(v.M.MaxIn), "clips": video.MaxClips,
			"timeoutMin": int(v.M.Timeout.Minutes()), "keep": video.KeepJobs, "uploadHours": int(video.UploadMaxAge.Hours()), "keptMB": platformFileMax >> 20},
		"persistent": persistent, "freeMB": v.M.Free() >> 20, "usedMB": v.M.Used() >> 20, "capMB": v.M.MaxBytes >> 20,
	})
}

func videoErr(c *gin.Context, err error) {
	var ue video.UserError
	if errors.As(err, &ue) {
		c.JSON(http.StatusBadRequest, gin.H{"error": ue.Msg})
		return
	}
	log.Printf("video: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Сервер не смог сохранить файл: " + err.Error()})
}

func (v *PlatformVideo) NewUpload(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var req struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Size int64  `json:"size"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный запрос"})
		return
	}
	u, err := v.M.NewUpload(req.Name, req.Kind, req.Size)
	if err != nil {
		videoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

func (v *PlatformVideo) Chunk(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	id := c.Param("id")
	off, err := strconv.ParseInt(c.Query("off"), 10, 64)
	if !video.ValidID(id) || err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный запрос"})
		return
	}
	longBody(c)
	u, err := v.M.Chunk(id, off, http.MaxBytesReader(c.Writer, c.Request.Body, video.MaxChunk+1))
	if errors.Is(err, video.ErrGap) {
		c.JSON(http.StatusConflict, gin.H{"error": "gap", "got": u.Got})
		return
	}
	if err != nil {
		videoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": u.ID, "got": u.Got, "size": u.Size})
}

func (v *PlatformVideo) DeleteUpload(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	if id := c.Param("id"); video.ValidID(id) {
		v.M.DeleteUpload(id)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (v *PlatformVideo) Submit(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var req struct {
		Clips []string      `json:"clips"`
		Music string        `json:"music"`
		Opts  video.Options `json:"opts"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Неверный запрос"})
		return
	}
	j, err := v.M.Submit(req.Clips, req.Music, req.Opts, platformUser(c))
	if err != nil {
		videoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, v.view(*j))
}

func (v *PlatformVideo) Job(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	j := v.M.Job(c.Param("id"))
	if j == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ролик не найден"})
		return
	}
	c.JSON(http.StatusOK, v.view(*j))
}

func (v *PlatformVideo) Retry(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	j, err := v.M.Retry(c.Param("id"))
	if err != nil {
		videoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, v.view(*j))
}

func (v *PlatformVideo) Delete(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	if id := c.Param("id"); video.ValidID(id) {
		v.M.Delete(id)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

var videoFileRe = regexp.MustCompile(`^([jp])([a-f0-9]{16,32})-([a-f0-9]{24})\.(mp4|png)$`)

// File: the Reels or its cover by a signed link (no login: <video> and the SMM plan use it).
func (v *PlatformVideo) File(c *gin.Context) {
	m := videoFileRe.FindStringSubmatch(c.Param("name"))
	if m == nil || !hmac.Equal([]byte(m[3]), []byte(v.sig(m[1]+m[2]))) {
		c.Status(http.StatusNotFound)
		return
	}
	kind, id, ext := m[1], m[2], m[4]
	var path string
	if kind == "j" {
		path = v.M.OutPath(id)
		if ext == "png" {
			path = v.M.CoverPath(id)
		}
	} else {
		// from Postgres, once to the disk cache (the player asks by ranges)
		path = filepath.Join(v.M.Dir, "cache", id+"."+ext)
		if _, err := os.Stat(path); err != nil {
			if v.repo == nil {
				c.Status(http.StatusNotFound)
				return
			}
			f, err := v.repo.GetFile(c.Request.Context(), id)
			if err != nil || f == nil {
				c.Status(http.StatusNotFound)
				return
			}
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			tmp := path + ".part"
			if os.WriteFile(tmp, f.Data, 0o644) != nil || os.Rename(tmp, path) != nil {
				c.Data(http.StatusOK, f.Mime, f.Data)
				return
			}
		}
	}
	if _, err := os.Stat(path); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Файл ролика удалён: соберите заново"})
		return
	}
	longBody(c)
	mt := "video/mp4"
	if ext == "png" {
		mt = "image/png"
	}
	c.Header("Content-Type", mt)
	c.Header("Cache-Control", "private, max-age=604800, immutable")
	if c.Query("dl") == "1" {
		name := "reels." + ext
		if d := c.Query("name"); d != "" && len(d) < 80 {
			name = d + "." + ext
		}
		c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	http.ServeFile(c.Writer, c.Request, path)
}

// RegisterVideo: the routes of «Видео» (g: the team's group). Nothing without
// storage (tests build the module without a database).
func RegisterVideo(r *gin.Engine, g *gin.RouterGroup, m *PlatformModule) {
	if m == nil || m.h == nil || m.h.repo == nil {
		return
	}
	var asr *ai.LocalASR
	if m.AI != nil && m.AI.AI != nil {
		asr = m.AI.AI.ASR
	}
	v := NewPlatformVideo(m.h.repo, asr, m.secret)
	if v == nil {
		return
	}
	v.Start(context.Background())
	v.Routes(r, g)
	if m.AI != nil && m.AI.Premium != nil {
		// R62: the same ffmpeg brings the voice files to one loudness
		m.AI.Premium.SetFFmpeg(v.ffmpeg)
	}
}

// Routes: the public signed files and the team's API.
func (v *PlatformVideo) Routes(r *gin.Engine, g *gin.RouterGroup) {
	r.GET("/api/v1/platform/video/f/:name", v.File)
	r.HEAD("/api/v1/platform/video/f/:name", v.File)
	g.GET("/video", v.Status)
	g.POST("/video/uploads", v.NewUpload)
	g.PUT("/video/uploads/:id", v.Chunk)
	g.DELETE("/video/uploads/:id", v.DeleteUpload)
	g.POST("/video/jobs", v.Submit)
	g.GET("/video/jobs/:id", v.Job)
	g.POST("/video/jobs/:id/retry", v.Retry)
	g.DELETE("/video/jobs/:id", v.Delete)
}
