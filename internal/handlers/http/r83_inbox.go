package http

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/bot"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
	"github.com/gin-gonic/gin"
)

// R83: «Быстрая заметка». The owner used Telegram's own notes; now one big
// input on the phone (bottom bar, or a long press on the installed app's
// icon) keeps a thought, a voice note (transcribed on the server, free
// Whisper) or a photo in the team's inbox. A message forwarded to the bot by
// the team lands in the same inbox. Later each note goes to a task, an idea
// or a board with one tap.
//
// Storage: server/bs_qnotes (never synced to a page: the inbox is read
// through this API, team only). Photos are platform files.

const (
	qnDocKey = "bs_qnotes"
	qnMax    = 600
)

type QNote struct {
	ID     string   `json:"id"`
	Text   string   `json:"text"`
	At     string   `json:"at"`
	By     string   `json:"by,omitempty"`
	ByName string   `json:"byName,omitempty"`
	Src    string   `json:"src"` // app | telegram
	From   string   `json:"from,omitempty"`
	Photos []string `json:"photos,omitempty"`
	Voice  bool     `json:"voice,omitempty"`
	Status string   `json:"status"` // new | done
	To     string   `json:"to,omitempty"`
	ToName string   `json:"toName,omitempty"`
	DoneAt string   `json:"doneAt,omitempty"`
}

type qnDoc struct {
	Notes []QNote `json:"notes"`
}

type QuickInbox struct {
	Docs  funnelDocs
	Repo  *pg.PlatformRepo
	AI    *PlatformAI
	Names func(tg int64) string
	Now   func() time.Time
}

func NewQuickInbox(repo *pg.PlatformRepo, a *PlatformAI) *QuickInbox {
	q := &QuickInbox{AI: a, Repo: repo}
	if repo != nil {
		q.Docs = repo
	}
	return q
}

func (q *QuickInbox) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *QuickInbox) team(c *gin.Context) bool {
	r := platformRole(c)
	if (r != "admin" && r != "moderator") || assistOf(c) != nil {
		forbidden(c, "team_only")
		return false
	}
	return true
}

func qnID() string { return "q" + newID()[:14] }

// Add keeps one note (newest first); the oldest done ones go first when full.
func (q *QuickInbox) Add(ctx context.Context, n QNote) (QNote, error) {
	if n.ID == "" {
		n.ID = qnID()
	}
	if n.At == "" {
		n.At = q.now().UTC().Format(time.RFC3339)
	}
	if n.Status == "" {
		n.Status = "new"
	}
	n.Text = clip(strings.TrimSpace(noLongDash(n.Text)), 8000)
	err := r83Mutate(ctx, q.Docs, "server", qnDocKey, firstNonBlank(n.By, "server:inbox"), func(d *qnDoc) bool {
		d.Notes = append([]QNote{n}, d.Notes...)
		for len(d.Notes) > qnMax {
			cut := -1
			for i := len(d.Notes) - 1; i >= 0; i-- {
				if d.Notes[i].Status == "done" {
					cut = i
					break
				}
			}
			if cut < 0 {
				cut = len(d.Notes) - 1
			}
			d.Notes = append(d.Notes[:cut], d.Notes[cut+1:]...)
		}
		return true
	})
	return n, err
}

func (q *QuickInbox) whoName(c *gin.Context) string {
	if q.Names != nil {
		if n := q.Names(platformTgID(c)); n != "" {
			return n
		}
	}
	return ""
}

func (q *QuickInbox) List(c *gin.Context) {
	if !q.team(c) {
		return
	}
	d, err := r83Read[qnDoc](c.Request.Context(), q.Docs, "server", qnDocKey)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	if d.Notes == nil {
		d.Notes = []QNote{}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"notes": d.Notes})
}

func (q *QuickInbox) Create(c *gin.Context) {
	if !q.team(c) {
		return
	}
	var r struct {
		Text   string   `json:"text"`
		Photos []string `json:"photos"`
		Voice  bool     `json:"voice"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	var photos []string
	for _, p := range r.Photos {
		if platformIDRe.MatchString(p) && len(photos) < 6 {
			photos = append(photos, p)
		}
	}
	if strings.TrimSpace(r.Text) == "" && len(photos) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty", "detail": "Пустая заметка"})
		return
	}
	n, err := q.Add(c.Request.Context(), QNote{Text: r.Text, Photos: photos, Voice: r.Voice, Src: "app", By: platformUser(c), ByName: q.whoName(c)})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again", "detail": "Не сохранилось, попробуйте ещё раз"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"note": n})
}

func (q *QuickInbox) Update(c *gin.Context) {
	if !q.team(c) {
		return
	}
	var r struct {
		Status *string `json:"status"`
		To     *string `json:"to"`
		ToName *string `json:"toName"`
		Text   *string `json:"text"`
	}
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	id := c.Param("id")
	var got *QNote
	err := r83Mutate(c.Request.Context(), q.Docs, "server", qnDocKey, platformUser(c), func(d *qnDoc) bool {
		got = nil
		for i := range d.Notes {
			n := &d.Notes[i]
			if n.ID != id {
				continue
			}
			if r.Text != nil {
				n.Text = clip(strings.TrimSpace(noLongDash(*r.Text)), 8000)
			}
			if r.To != nil {
				n.To = clip(*r.To, 20)
			}
			if r.ToName != nil {
				n.ToName = clip(*r.ToName, 120)
			}
			if r.Status != nil && (*r.Status == "new" || *r.Status == "done") {
				n.Status = *r.Status
				if n.Status == "done" {
					n.DoneAt = q.now().UTC().Format(time.RFC3339)
				} else {
					n.DoneAt, n.To, n.ToName = "", "", ""
				}
			}
			cp := *n
			got = &cp
			return true
		}
		return false
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	if got == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"note": got})
}

func (q *QuickInbox) Delete(c *gin.Context) {
	if !q.team(c) {
		return
	}
	id := c.Param("id")
	err := r83Mutate(c.Request.Context(), q.Docs, "server", qnDocKey, platformUser(c), func(d *qnDoc) bool {
		for i := range d.Notes {
			if d.Notes[i].ID == id {
				d.Notes = append(d.Notes[:i], d.Notes[i+1:]...)
				return true
			}
		}
		return false
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "try_again"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Voice: POST /inbox/voice (multipart "audio"): the dictation as text. Free:
// the server's own Whisper. Nothing is kept.
func (q *QuickInbox) Voice(c *gin.Context) {
	if !q.team(c) {
		return
	}
	longBody(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, voiceAudioMax)
	f, hdr, err := c.Request.FormFile("audio")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no_audio"})
		return
	}
	audio, _ := io.ReadAll(f)
	f.Close()
	mime := hdr.Header.Get("Content-Type")
	if mime == "" {
		mime = "audio/webm"
	}
	text, err := q.transcribe(c.Request.Context(), audio, mime)
	if err != nil || strings.TrimSpace(text) == "" {
		msg := "Не расслышал, попробуйте ещё раз ближе к микрофону"
		if err != nil {
			log.Printf("inbox voice: %v", err)
			msg = "Распознавание сейчас недоступно. Напишите текстом или отправьте голосовое боту"
		}
		c.JSON(http.StatusOK, gin.H{"text": "", "detail": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"text": strings.TrimSpace(text)})
}

func (q *QuickInbox) transcribe(ctx context.Context, audio []byte, mime string) (string, error) {
	if q.AI == nil || q.AI.AI == nil || len(audio) == 0 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if q.AI.AI.ASR != nil {
		if t, err := q.AI.AI.ASR.Transcribe(ctx, audio, mime); err == nil && strings.TrimSpace(t) != "" {
			return t, nil
		} else if err != nil {
			log.Printf("inbox: local asr: %v", err)
		}
	}
	return q.AI.AI.Transcribe(ctx, audio, mime)
}

// FromBot: a team member's message to the bot (forwarded or written) goes
// to the inbox. bot.InboxHook.
func (q *QuickInbox) FromBot(ctx context.Context, m bot.InboxNote) (string, bool) {
	n := QNote{Src: "telegram", By: "tg:" + strconv.FormatInt(m.FromID, 10), ByName: m.FromName, From: m.Forward}
	text := strings.TrimSpace(m.Text)
	if len(m.Voice) > 0 {
		t, err := q.transcribe(ctx, m.Voice, firstNonBlank(m.VoiceMime, "audio/ogg"))
		if err != nil {
			log.Printf("inbox: voice from the bot: %v", err)
		}
		n.Voice = true
		if strings.TrimSpace(t) != "" {
			text = strings.TrimSpace(text + "\n" + strings.TrimSpace(t))
		} else if text == "" {
			text = "🎙 Голосовое (не распознано, послушайте в Telegram)"
		}
	}
	if len(m.Photo) > 0 && q.Repo != nil {
		f := pg.PlatformFile{ID: newID(), Name: "telegram-photo.jpg", Mime: firstNonBlank(m.PhotoMime, "image/jpeg"), Data: m.Photo}
		if err := q.Repo.PutFile(ctx, f, n.By); err == nil {
			n.Photos = []string{f.ID}
		} else {
			log.Printf("inbox: photo from the bot: %v", err)
		}
	}
	if text == "" && len(n.Photos) == 0 {
		return "", false
	}
	n.Text = text
	if _, err := q.Add(ctx, n); err != nil {
		return "Не сохранилось, перешлите ещё раз через минуту.", true
	}
	return "📥 Сохранено в «Быстрые заметки» на платформе. Разберите их потом в задачи, идеи или на доску.", true
}

func (q *QuickInbox) Register(g *gin.RouterGroup) {
	g.GET("/inbox", q.List)
	g.POST("/inbox", q.Create)
	g.POST("/inbox/voice", q.Voice)
	g.POST("/inbox/:id", q.Update)
	g.DELETE("/inbox/:id", q.Delete)
}
