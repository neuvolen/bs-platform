package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Автопостинг «Полезного» в Threads.
//
// Каждый день в THREADS_HOUR (по Алматы, по умолчанию 10:00) сервер берёт
// верхний материал из «Полезного» (club doc bs_useful), который ещё не
// публиковался в Threads, и публикует его. Токен (THREADS_TOKEN, долгосрочный)
// сервер сам продлевает раз в 50 дней и хранит у себя. Состояние для платформы
// лежит в club doc bs_threads_status.

const (
	threadsAPI     = "https://graph.threads.net"
	threadsMaxText = 500
)

// threadsWait: Threads asks to wait a moment between creating and publishing a post.
var threadsWait = 2 * time.Second

type threadsState struct {
	Token     string `json:"token"`
	Refreshed string `json:"refreshed"`
}

type threadsStatus struct {
	Connected bool   `json:"connected"`
	Username  string `json:"username,omitempty"`
	Hour      int    `json:"hour"`
	Last      string `json:"last,omitempty"`
	LastTitle string `json:"lastTitle,omitempty"`
	Error     string `json:"error,omitempty"`
	Queue     int    `json:"queue"`
	Updated   string `json:"updated"`
}

func (h *PlatformAI) threadsBase() string {
	if b := strings.TrimSpace(os.Getenv("THREADS_API_BASE")); b != "" {
		return strings.TrimRight(b, "/")
	}
	return threadsAPI
}

func threadsHour() int {
	if n, err := strconv.Atoi(os.Getenv("THREADS_HOUR")); err == nil && n >= 0 && n < 24 {
		return n
	}
	return 10
}

// threadsToken: the stored (refreshed) token, else the one from Railway.
func (h *PlatformAI) threadsToken(ctx context.Context) string {
	env := strings.TrimSpace(os.Getenv("THREADS_TOKEN"))
	if d, err := h.repo.GetDoc(ctx, "server", "threads"); err == nil && d != nil {
		var st threadsState
		if json.Unmarshal([]byte(d.Value), &st) == nil && st.Token != "" {
			return st.Token
		}
	}
	return env
}

func (h *PlatformAI) threadsCall(ctx context.Context, method, path string, q url.Values) (map[string]any, error) {
	u := h.threadsBase() + path + "?" + q.Encode()
	req, _ := http.NewRequestWithContext(ctx, method, u, nil)
	res, err := h.AI.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if res.StatusCode >= 300 {
		// the code tells a dead token (190) from the limits (4, 17, 32, 613): content_threads.go
		ae := &threadsAPIError{Status: res.StatusCode, Msg: strings.TrimSpace(string(b))}
		if e, ok := out["error"].(map[string]any); ok {
			if m, _ := e["message"].(string); m != "" {
				ae.Msg = m
			}
			ae.Type, _ = e["type"].(string)
			if c, ok := e["code"].(float64); ok {
				ae.Code = int(c)
			}
			if c, ok := e["error_subcode"].(float64); ok {
				ae.Subcode = int(c)
			}
		}
		if len([]rune(ae.Msg)) > 300 {
			ae.Msg = string([]rune(ae.Msg)[:300])
		}
		return out, ae
	}
	return out, nil
}

// refreshThreadsToken prolongs a long-lived token (valid 60 days) every 50 days.
func (h *PlatformAI) refreshThreadsToken(ctx context.Context, tok string) string {
	var st threadsState
	base := 0
	if d, err := h.repo.GetDoc(ctx, "server", "threads"); err == nil && d != nil {
		base = d.Version
		_ = json.Unmarshal([]byte(d.Value), &st)
	}
	if t, err := time.Parse(time.RFC3339, st.Refreshed); err == nil && time.Since(t) < 50*24*time.Hour && st.Token == tok {
		return tok
	}
	out, err := h.threadsCall(ctx, "GET", "/refresh_access_token", url.Values{"grant_type": {"th_refresh_token"}, "access_token": {tok}})
	if err != nil {
		log.Printf("threads refresh: %v", err)
		return tok
	}
	if nt, _ := out["access_token"].(string); nt != "" {
		val, _ := json.Marshal(threadsState{Token: nt, Refreshed: time.Now().UTC().Format(time.RFC3339)})
		_, _ = h.repo.PutDoc(ctx, "server", "threads", base, string(val), false, "server:threads")
		return nt
	}
	return tok
}

func (h *PlatformAI) saveThreadsStatus(ctx context.Context, st threadsStatus) {
	st.Hour = threadsHour()
	st.Updated = time.Now().UTC().Format(time.RFC3339)
	val, _ := json.Marshal(st)
	for try := 0; try < 3; try++ {
		base := 0
		if d, err := h.repo.GetDoc(ctx, "club", "bs_threads_status"); err == nil && d != nil {
			base = d.Version
		}
		if _, err := h.repo.PutDoc(ctx, "club", "bs_threads_status", base, string(val), false, "server:threads"); err == nil {
			return
		}
	}
}

// threadsText: title + text, cut to Threads' 500 characters on a sentence or word.
func threadsText(title, txt string) string {
	t := strings.TrimSpace(title)
	body := strings.TrimSpace(txt)
	s := body
	if t != "" && !strings.HasPrefix(body, t) {
		s = t + "\n\n" + body
	}
	r := []rune(s)
	if len(r) <= threadsMaxText {
		return s
	}
	cut := string(r[:threadsMaxText-1])
	if i := strings.LastIndexAny(cut, ".!?\n"); i > threadsMaxText/2 {
		return strings.TrimSpace(cut[:i+1])
	}
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

// PublishThreadsText publishes a ready text (the content queue) and returns
// the post id and its link. The text must fit Threads' 500 characters.
func (h *PlatformAI) PublishThreadsText(ctx context.Context, title, text string) (postID, link string, err error) {
	text = strings.TrimSpace(text)
	if n := len([]rune(text)); n > threadsMaxText {
		return "", "", fmt.Errorf("Текст длиннее %d знаков (%d): сократите его на платформе", threadsMaxText, n)
	}
	if text == "" {
		return "", "", errors.New("Пустой текст")
	}
	tok := h.threadsToken(ctx)
	if tok == "" {
		h.saveThreadsStatus(ctx, threadsStatus{Error: "Нет THREADS_TOKEN в переменных Railway"})
		return "", "", errors.New("Нет THREADS_TOKEN в переменных Railway")
	}
	tok = h.refreshThreadsToken(ctx, tok)
	me, err := h.threadsCall(ctx, "GET", "/v1.0/me", url.Values{"fields": {"id,username"}, "access_token": {tok}})
	if err != nil {
		h.saveThreadsStatus(ctx, threadsStatus{Error: err.Error()})
		return "", "", err
	}
	uid, _ := me["id"].(string)
	username, _ := me["username"].(string)
	cr, err := h.threadsCall(ctx, "POST", "/v1.0/"+uid+"/threads", url.Values{"media_type": {"TEXT"}, "text": {text}, "access_token": {tok}})
	if err != nil {
		h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Error: err.Error()})
		return "", "", err
	}
	cid, _ := cr["id"].(string)
	time.Sleep(threadsWait)
	pub, err := h.threadsCall(ctx, "POST", "/v1.0/"+uid+"/threads_publish", url.Values{"creation_id": {cid}, "access_token": {tok}})
	if err != nil {
		h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Error: err.Error()})
		return "", "", err
	}
	postID, _ = pub["id"].(string)
	if pl, err := h.threadsCall(ctx, "GET", "/v1.0/"+postID, url.Values{"fields": {"permalink"}, "access_token": {tok}}); err == nil {
		link, _ = pl["permalink"].(string)
	}
	if link == "" && username != "" {
		link = "https://www.threads.net/@" + username
	}
	h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Last: time.Now().UTC().Format(time.RFC3339), LastTitle: title})
	return postID, link, nil
}

// PublishThreadsReply publishes a reply to a post (the next part of a series).
func (h *PlatformAI) PublishThreadsReply(ctx context.Context, replyTo, text string) (string, error) {
	text = strings.TrimSpace(text)
	if n := len([]rune(text)); n > threadsMaxText {
		return "", fmt.Errorf("Текст длиннее %d знаков (%d)", threadsMaxText, n)
	}
	if text == "" || replyTo == "" {
		return "", errors.New("Пустой ответ")
	}
	tok := h.threadsToken(ctx)
	if tok == "" {
		return "", errors.New("Нет THREADS_TOKEN в переменных Railway")
	}
	me, err := h.threadsCall(ctx, "GET", "/v1.0/me", url.Values{"fields": {"id"}, "access_token": {tok}})
	if err != nil {
		return "", err
	}
	uid, _ := me["id"].(string)
	cr, err := h.threadsCall(ctx, "POST", "/v1.0/"+uid+"/threads", url.Values{"media_type": {"TEXT"}, "text": {text}, "reply_to_id": {replyTo}, "access_token": {tok}})
	if err != nil {
		return "", err
	}
	cid, _ := cr["id"].(string)
	time.Sleep(threadsWait)
	pub, err := h.threadsCall(ctx, "POST", "/v1.0/"+uid+"/threads_publish", url.Values{"creation_id": {cid}, "access_token": {tok}})
	if err != nil {
		return "", err
	}
	id, _ := pub["id"].(string)
	return id, nil
}

// PublishNextThreads posts the next «Полезное» item. Returns its title.
func (h *PlatformAI) PublishNextThreads(ctx context.Context) (string, error) {
	tok := h.threadsToken(ctx)
	if tok == "" {
		h.saveThreadsStatus(ctx, threadsStatus{Error: "Нет THREADS_TOKEN в переменных Railway"})
		return "", errors.New("no THREADS_TOKEN")
	}
	tok = h.refreshThreadsToken(ctx, tok)
	me, err := h.threadsCall(ctx, "GET", "/v1.0/me", url.Values{"fields": {"id,username"}, "access_token": {tok}})
	if err != nil {
		h.saveThreadsStatus(ctx, threadsStatus{Error: err.Error()})
		return "", err
	}
	uid, _ := me["id"].(string)
	username, _ := me["username"].(string)

	d, err := h.repo.GetDoc(ctx, "club", "bs_useful")
	if err != nil || d == nil || d.Deleted {
		h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Error: "В «Полезном» нет материалов"})
		return "", errors.New("no useful items")
	}
	var doc map[string]any
	if json.Unmarshal([]byte(d.Value), &doc) != nil {
		return "", errors.New("bs_useful unreadable")
	}
	items, _ := doc["items"].([]any)
	idx, queue := -1, 0
	for i, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if th, _ := m["threads"].(string); th == "" {
			if idx < 0 {
				idx = i
			}
			queue++
		}
	}
	if idx < 0 {
		h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Queue: 0, Error: "Очередь пуста: добавьте материалы в «Полезное»"})
		return "", nil
	}
	m := items[idx].(map[string]any)
	title, _ := m["t"].(string)
	txt, _ := m["txt"].(string)
	text := threadsText(title, txt)
	cr, err := h.threadsCall(ctx, "POST", "/v1.0/"+uid+"/threads", url.Values{"media_type": {"TEXT"}, "text": {text}, "access_token": {tok}})
	if err != nil {
		h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Queue: queue, Error: err.Error()})
		return "", err
	}
	cid, _ := cr["id"].(string)
	time.Sleep(threadsWait)
	pub, err := h.threadsCall(ctx, "POST", "/v1.0/"+uid+"/threads_publish", url.Values{"creation_id": {cid}, "access_token": {tok}})
	if err != nil {
		h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Queue: queue, Error: err.Error()})
		return "", err
	}
	postID, _ := pub["id"].(string)
	now := time.Now().UTC().Format(time.RFC3339)
	// mark the item, re-reading the doc so edits made meanwhile are kept
	for try := 0; try < 4; try++ {
		d2, err := h.repo.GetDoc(ctx, "club", "bs_useful")
		if err != nil || d2 == nil {
			break
		}
		var doc2 map[string]any
		if json.Unmarshal([]byte(d2.Value), &doc2) != nil {
			break
		}
		its, _ := doc2["items"].([]any)
		for _, it := range its {
			mm, _ := it.(map[string]any)
			if mm == nil {
				continue
			}
			if t2, _ := mm["t"].(string); t2 == title {
				if th, _ := mm["threads"].(string); th == "" {
					mm["threads"], mm["threadsId"] = now, postID
					break
				}
			}
		}
		val, _ := json.Marshal(doc2)
		if _, err := h.repo.PutDoc(ctx, "club", "bs_useful", d2.Version, string(val), false, "server:threads"); err == nil {
			break
		}
	}
	h.saveThreadsStatus(ctx, threadsStatus{Connected: true, Username: username, Last: now, LastTitle: title, Queue: queue - 1})
	return title, nil
}

// ThreadsLoop publishes once a day at THREADS_HOUR (Almaty), unless the
// content queue (bs_content) has a Threads post that day: then the content
// engine publishes it at its own time and «Полезное» waits.
func (h *PlatformAI) ThreadsLoop(ctx context.Context) {
	loc := time.FixedZone("Almaty", 5*3600)
	for {
		a := time.Now().In(loc)
		next := time.Date(a.Year(), a.Month(), a.Day(), threadsHour(), 0, 0, 0, loc)
		if !next.After(a) {
			next = next.AddDate(0, 0, 1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(a)):
		}
		if strings.TrimSpace(os.Getenv("THREADS_TOKEN")) == "" {
			continue
		}
		if f := h.queueOwns.Load(); f != nil && *f != nil && (*f)(ctx, next) {
			log.Printf("threads: today's post comes from the content queue")
			continue
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		t, err := h.PublishNextThreads(c)
		cancel()
		log.Printf("threads: posted %q err=%v", t, err)
	}
}

// ThreadsNow: POST /threads/publish (team) publishes the next item at once.
func (h *PlatformAI) ThreadsNow(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	t, err := h.PublishNextThreads(ctx)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"error": err.Error()})
		return
	}
	if t == "" {
		c.JSON(http.StatusOK, gin.H{"error": "Очередь пуста: добавьте материалы в «Полезное»"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "posted": t, "title": t})
}
