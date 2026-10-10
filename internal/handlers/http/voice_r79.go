package http

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/ai"
	"github.com/bnursik/business_surgery_backend/internal/voicecmd"
	"github.com/gin-gonic/gin"
)

// R79: the board's hands-free voice assistant («Джарвис»).
//
// The browser listens for the wake word itself (a small Russian model in a
// Web Worker, web/vendor/kws-*/bskws.js): no sound leaves the device until
// «Джарвис» or «Ассистент» is heard. Then the command (until 1.5 s of
// silence) comes here as a short recording, together with the browser's own
// rough transcript of it:
//
//	POST /ai/voice     multipart: audio, context (voiceContext() JSON), local
//	POST /ai/command   {text, context}: the same without audio (typed)
//	POST /ai/notekind  {items:[…], diag:[…], tools:[…]}: sort board notes
//
// Whisper on the server (asr_cmd.go) transcribes it, the board's own words
// are corrected (voicecmd.Correct), rules understand most commands at once
// (voicecmd.Parse) and only the rest goes to the free AI chain (light model).
// Without any AI the phrase becomes a note.

const voiceAudioMax = 8 << 20

// cmdAI: the AI chain for a command the rules did not understand.
var cmdAI = func(h *PlatformAI, ctx context.Context, text string, raw json.RawMessage) (map[string]any, error) {
	today := time.Now().In(time.FixedZone("Almaty", 5*3600)).Format("02.01.2006, Monday")
	ans, err := h.AI.JSON(ai.Light(ctx), commandSystem, "today: "+today+"\nСостояние платформы:\n"+string(raw)+"\n\nФраза трекера: «"+text+"»")
	if err != nil {
		return nil, err
	}
	js := ai.JSONFrom(ans)
	var out map[string]any
	if js == "" || json.Unmarshal([]byte(js), &out) != nil {
		return map[string]any{"actions": []any{}, "say": "Не понял команду"}, nil
	}
	return out, nil
}

// runCommand: rules first (on Whisper's text, then on the browser's), then
// the AI, then a note.
func (h *PlatformAI) runCommand(ctx context.Context, text, local string, raw json.RawMessage) gin.H {
	var vc voicecmd.Context
	_ = json.Unmarshal(raw, &vc)
	now := time.Now().In(time.FixedZone("Almaty", 5*3600))
	if r, ok := voicecmd.Parse(text, vc, now); ok {
		return gin.H{"text": r.Text, "actions": r.Actions, "say": r.Say, "via": "rules", "rule": r.Rule}
	}
	if local != "" && voicecmd.Norm(local) != voicecmd.Norm(text) {
		if r, ok := voicecmd.Parse(local, vc, now); ok && len(r.Actions) > 0 {
			return gin.H{"text": r.Text, "actions": r.Actions, "say": r.Say, "via": "rules-local", "rule": r.Rule}
		}
	}
	clean := voicecmd.StripWake(text)
	if clean == "" {
		return gin.H{"text": "", "actions": []any{}, "say": "Не расслышал"}
	}
	if h.AI != nil {
		out, err := cmdAI(h, ctx, clean, raw)
		if err == nil {
			out["text"] = clean
			out["via"] = "ai"
			return gin.H(out)
		}
		log.Printf("voice: ai: %v", err)
	}
	// no AI: the phrase is kept as a note (sorted: numbers stay numbers)
	nk := voicecmd.ClassifyNote(clean, vc.DiagLib, vc.ToolLib)
	return gin.H{"text": clean, "via": "note", "kind": nk,
		"actions": []any{map[string]any{"op": "add_node", "type": "note", "title": voicecmd.Cap(clean)}},
		"say":     "Записал заметку"}
}

// Voice: POST /ai/voice.
func (h *PlatformAI) Voice(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	longBody(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, voiceAudioMax)
	f, hdr, err := c.Request.FormFile("audio")
	local := strings.TrimSpace(c.PostForm("local"))
	raw := json.RawMessage(c.PostForm("context"))
	if len(raw) == 0 || !json.Valid(raw) {
		raw = json.RawMessage("{}")
	}
	var audio []byte
	mime := "audio/wav"
	if err == nil {
		audio, _ = io.ReadAll(f)
		f.Close()
		if ct := hdr.Header.Get("Content-Type"); ct != "" {
			mime = ct
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	text, asrErr := "", error(nil)
	t0 := time.Now()
	if len(audio) > 0 && h.AI != nil && h.AI.ASR != nil {
		text, asrErr = h.AI.ASR.TranscribeCommand(ctx, audio, mime)
		if asrErr != nil {
			log.Printf("voice: asr: %v", asrErr)
		}
	}
	asrMs := time.Since(t0).Milliseconds()
	src := "whisper"
	if strings.TrimSpace(text) == "" {
		text, src = local, "local"
	}
	if strings.TrimSpace(text) == "" {
		msg := "Не расслышал команду"
		if asrErr != nil {
			msg = "Распознавание на сервере не ответило: " + asrErr.Error()
		}
		c.JSON(http.StatusOK, gin.H{"text": "", "actions": []any{}, "say": msg, "asr": src, "asr_ms": asrMs})
		return
	}
	out := h.runCommand(ctx, text, local, raw)
	out["heard"] = text
	out["asr"] = src
	out["asr_ms"] = asrMs
	c.JSON(http.StatusOK, out)
}

type noteKindReq struct {
	Items []string `json:"items"`
	Diag  []string `json:"diag"`
	Tools []string `json:"tools"`
}

// NoteKind: POST /ai/notekind: sorts up to 200 notes at once.
func (h *PlatformAI) NoteKind(c *gin.Context) {
	if !teamOnly(c) {
		return
	}
	var r noteKindReq
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	if len(r.Items) > 200 {
		r.Items = r.Items[:200]
	}
	out := make([]voicecmd.NoteKind, len(r.Items))
	for i, t := range r.Items {
		out[i] = voicecmd.ClassifyNote(t, r.Diag, r.Tools)
	}
	c.JSON(http.StatusOK, gin.H{"kinds": out})
}
