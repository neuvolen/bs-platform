package http

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/gin-gonic/gin"
)

// R34a: «Ключ Claude (Anthropic)» in the platform settings, for the owner
// who cannot open Railway. Admin only.
//
//	GET    /ai/key        {source: env|settings|"", last4, model, heavyModel, speech}
//	PUT    /ai/key        {key}  saves it (sealed, keys.go) and uses it at once
//	DELETE /ai/key        forgets the saved key
//	POST   /ai/key/test   {key?} a tiny call with that key (or the current one)
//
// ANTHROPIC_API_KEY in Railway wins over the saved key. The key never goes
// back to the page or into the logs: only its last four characters.

const aiKeyDoc = "ai_claude_key" // platform_docs, scope "server": never synced to a page

// aiKeySecret: what seals the key: AI_KEYS_SECRET, else JWT_SECRET.
func (h *PlatformAI) aiKeySecret() []byte {
	if s := strings.TrimSpace(os.Getenv("AI_KEYS_SECRET")); s != "" {
		return []byte(s)
	}
	return h.KeySecret
}

func adminOnly(c *gin.Context) bool {
	if platformRole(c) != "admin" {
		forbidden(c, "admin_only")
		return false
	}
	return true
}

// LoadAIKey reads the saved key into the client (at start).
func (h *PlatformAI) LoadAIKey(ctx context.Context) {
	if h.repo == nil || h.AI == nil {
		return
	}
	d, err := h.repo.GetDoc(ctx, "server", aiKeyDoc)
	if err != nil || d == nil || d.Deleted || d.Value == "" {
		return
	}
	key, err := ai.Open(h.aiKeySecret(), d.Value)
	if err != nil {
		log.Printf("ai key: the saved Claude key cannot be read: %v", err)
		h.keys().SetProblem("ключ из настроек не расшифровывается (сменился JWT_SECRET или AI_KEYS_SECRET): вставьте его заново")
		return
	}
	h.keys().Set(key)
}

func (h *PlatformAI) keys() *ai.KeyBox {
	if h.AI.Keys == nil {
		h.AI.Keys = &ai.KeyBox{}
	}
	return h.AI.Keys
}

func (h *PlatformAI) keyView() gin.H {
	src := h.AI.KeySource()
	out := gin.H{"source": src, "envSet": h.AI.Anthropic != "", "saved": h.keys().Get() != ""}
	if e := h.AI.Env; e != nil { // R37: variable names only, never values
		out["envName"], out["envRelated"] = e.Name, e.Related
	}
	if p := h.keys().Problem(); p != "" {
		out["problem"] = p
	}
	switch src {
	case "env":
		out["last4"] = ai.Last4(h.AI.Anthropic)
	case "settings":
		out["last4"] = ai.Last4(h.keys().Get())
	}
	st := h.AI.Status()
	for _, k := range []string{"model", "heavyModel", "speech", "speech_model", "paused", "message"} {
		if v, ok := st[k]; ok {
			out[k] = v
		}
	}
	return out
}

// AIKey: GET /ai/key
func (h *PlatformAI) AIKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	c.JSON(http.StatusOK, h.keyView())
}

// PutAIKey: PUT /ai/key {key}
func (h *PlatformAI) PutAIKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	key, _ := ai.CleanKey(in.Key) // R37: quotes, spaces, «Bearer » pasted with it
	if !ai.ValidKeyShape(key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_key", "message": "Это не похоже на ключ Anthropic: скопируйте его целиком из console.anthropic.com (начинается с sk-ant-)"})
		return
	}
	if h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_db"})
		return
	}
	sealed, err := ai.Seal(h.aiKeySecret(), key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seal_failed"})
		return
	}
	if !h.putKeyDoc(c.Request.Context(), sealed, false, platformUser(c)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	h.keys().Set(key)
	h.AI.SetQuotaUntil("claude", time.Time{}) // a new key: the balance pause is over
	log.Printf("ai key: a Claude key %s saved by %s", ai.Last4(key), platformUser(c))
	c.JSON(http.StatusOK, h.keyView())
}

// DeleteAIKey: DELETE /ai/key
func (h *PlatformAI) DeleteAIKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	if h.repo != nil && !h.putKeyDoc(c.Request.Context(), "", true, platformUser(c)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store_failed"})
		return
	}
	h.keys().Set("")
	log.Printf("ai key: the saved Claude key removed by %s", platformUser(c))
	c.JSON(http.StatusOK, h.keyView())
}

func (h *PlatformAI) putKeyDoc(ctx context.Context, val string, deleted bool, by string) bool {
	for try := 0; try < 4; try++ {
		ver := 0
		if d, err := h.repo.GetDoc(ctx, "server", aiKeyDoc); err == nil && d != nil {
			ver = d.Version
		}
		if ver == 0 && deleted {
			return true
		}
		if _, err := h.repo.PutDoc(ctx, "server", aiKeyDoc, ver, val, deleted, by); err == nil {
			return true
		}
	}
	return false
}

// TestAIKey: POST /ai/key/test {key?}: OK or the reason, in words.
func (h *PlatformAI) TestAIKey(c *gin.Context) {
	if !adminOnly(c) {
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	_ = c.ShouldBindJSON(&in)
	key, _ := ai.CleanKey(in.Key)
	if key != "" && !ai.ValidKeyShape(key) {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": "Это не похоже на ключ Anthropic: скопируйте его целиком из console.anthropic.com"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()
	model, err := h.AI.Ping(ctx, key)
	if err != nil {
		msg := ai.UserMessage(err)
		if ai.IsQuota(err) {
			msg = ai.QuotaMessage
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "model": model, "message": "Ключ работает: отвечает " + model})
}
