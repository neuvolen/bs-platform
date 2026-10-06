package http

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/bot"
)

// R47: диалог лида с ботом на карточке CRM.
//
// Каждое сообщение лида (текст, медиа, /start, нажатая кнопка) и каждый ответ
// бота ему (текст, фото, файл, правка сообщения проверки) пишутся в документ
// входящих bs_bot_inbox, поле dlg: по чату последние dlgPerChat сообщений в
// обе стороны со временем. Документ уходит команде синхронизацией, карточка
// лида показывает переписку во вкладке «Бот». Сообщения команде (админам) не
// пишутся. Размер ограничен: dlgChats самых свежих чатов, текст до dlgTextMax
// знаков. Карточки лидов до 05.10 (до R40b) ответа бота не хранили: платформа
// так и пишет, без «ошибки».

const (
	dlgPerChat = 12
	dlgChats   = 100
	dlgTextMax = 300
	dlgBtnMax  = 6
)

// DlgMsg: one message of the dialog.
type DlgMsg struct {
	At int64    `json:"at"`          // unix seconds
	D  string   `json:"d"`           // "in" (the lead) or "out" (the bot)
	T  string   `json:"t"`           // the text, short
	B  []DlgBtn `json:"b,omitempty"` // the bot's buttons under the message
}

// DlgBtn: a button of the bot's message (D: callback_data, to name a press).
type DlgBtn struct {
	T string `json:"t"`
	D string `json:"d,omitempty"`
}

var dlgTagRe = regexp.MustCompile(`<[^>]{1,80}>`)

func dlgText(s string) string {
	s = html.UnescapeString(dlgTagRe.ReplaceAllString(s, ""))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > dlgTextMax {
		s = string(r[:dlgTextMax-1]) + "…"
	}
	return s
}

func dlgButtons(keys map[string]any) []DlgBtn {
	if keys == nil {
		return nil
	}
	var out []DlgBtn
	add := func(b map[string]any) {
		if len(out) >= dlgBtnMax {
			return
		}
		t := dlgText(fmt.Sprint(b["text"]))
		if t == "" || t == "<nil>" {
			return
		}
		d, _ := b["callback_data"].(string)
		out = append(out, DlgBtn{T: t, D: d})
	}
	switch rows := keys["inline_keyboard"].(type) {
	case [][]map[string]any:
		for _, r := range rows {
			for _, b := range r {
				add(b)
			}
		}
	case []any:
		for _, r := range rows {
			if rr, ok := r.([]any); ok {
				for _, b := range rr {
					if m, ok := b.(map[string]any); ok {
						add(m)
					}
				}
			} else if rr, ok := r.([]map[string]any); ok {
				for _, b := range rr {
					add(b)
				}
			}
		}
	}
	return out
}

func (f *LeadFunnel) isAdminChat(chat int64) bool {
	for _, a := range f.admins {
		if a == chat {
			return true
		}
	}
	return false
}

// noteDialog appends one message to the chat's dialog.
func (f *LeadFunnel) noteDialog(ctx context.Context, chat int64, dir, text string, btns []DlgBtn) {
	f.noteDialogAt(ctx, chat, 0, dir, text, btns)
}

// noteDialogAt: at (unix seconds) is when it happened (0: now); the message
// takes its place by time (the inbox step may run after the bot's answer).
func (f *LeadFunnel) noteDialogAt(ctx context.Context, chat, at int64, dir, text string, btns []DlgBtn) {
	if chat <= 0 || f.isAdminChat(chat) {
		return
	}
	text = dlgText(text)
	if text == "" && len(btns) == 0 {
		return
	}
	now := f.now()
	dated := at > 0 // a lead's message with Telegram's date: it came before the bot's answer of the same second
	if at <= 0 || at > now.Unix() {
		at = now.Unix()
	}
	msg := map[string]any{"at": at, "d": dir, "t": text}
	if len(btns) > 0 {
		bl := make([]any, 0, len(btns))
		for _, b := range btns {
			m := map[string]any{"t": b.T}
			if b.D != "" {
				m["d"] = b.D
			}
			bl = append(bl, m)
		}
		msg["b"] = bl
	}
	key := strconv.FormatInt(chat, 10)
	err := f.mutate(ctx, inboxDoc, func(doc map[string]any) bool {
		dl, _ := doc["dlg"].(map[string]any)
		if dl == nil {
			dl = map[string]any{}
		}
		c, _ := dl[key].(map[string]any)
		if c == nil {
			c = map[string]any{}
		}
		ms, _ := c["m"].([]any)
		pos := len(ms)
		for pos > 0 {
			if pm, _ := ms[pos-1].(map[string]any); pm != nil && (anyInt(pm["at"]) > at ||
				(dated && anyInt(pm["at"]) == at && dir == "in" && pm["d"] == "out")) {
				pos--
				continue
			}
			break
		}
		ms = append(ms, nil)
		copy(ms[pos+1:], ms[pos:])
		ms[pos] = msg
		if len(ms) > dlgPerChat {
			ms = ms[len(ms)-dlgPerChat:]
		}
		c["m"], c["at"] = ms, now.Unix()
		dl[key] = c
		if len(dl) > dlgChats { // the oldest chats go
			type ka struct {
				k  string
				at int64
			}
			var all []ka
			for k, v := range dl {
				m, _ := v.(map[string]any)
				all = append(all, ka{k, anyInt(m["at"])})
			}
			sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
			for _, x := range all[:len(all)-dlgChats] {
				delete(dl, x.k)
			}
		}
		doc["dlg"] = dl
		return true
	})
	if err != nil {
		log.Printf("funnel: dialog %d: %v", chat, err)
	}
}

// dlgButtonLabel: the text of the button with this callback_data in the
// chat's recent bot messages ("" when unknown).
func (f *LeadFunnel) dlgButtonLabel(ctx context.Context, chat int64, data string) string {
	var doc map[string]any
	if d, err := f.docs.GetDoc(ctx, "club", inboxDoc); err == nil && d != nil && !d.Deleted {
		_ = json.Unmarshal([]byte(d.Value), &doc)
	}
	dl, _ := doc["dlg"].(map[string]any)
	c, _ := dl[strconv.FormatInt(chat, 10)].(map[string]any)
	ms, _ := c["m"].([]any)
	for i := len(ms) - 1; i >= 0; i-- {
		m, _ := ms[i].(map[string]any)
		bl, _ := m["b"].([]any)
		for _, b := range bl {
			if bm, _ := b.(map[string]any); bm != nil && bm["d"] == data {
				return fmt.Sprint(bm["t"])
			}
		}
	}
	return ""
}

// noteCallback: the lead pressed a button.
func (f *LeadFunnel) noteCallback(ctx context.Context, cb bot.CallbackUpdate) {
	if f.isAdminChat(cb.ChatID) {
		return
	}
	label := f.dlgButtonLabel(ctx, cb.ChatID, cb.Data)
	if label == "" {
		label = "кнопка"
	}
	f.noteDialog(ctx, cb.ChatID, "in", "▸ "+label, nil)
}

// inboundDialogText: what the lead sent, for the dialog.
func inboundDialogText(in bot.Inbound) string {
	if in.Start {
		if in.Param != "" {
			return "/start (" + startSource(in.Param) + ")"
		}
		return "/start"
	}
	return inboundText(in)
}

// RecordDialog makes every message of the bot to a lead go to the dialog
// (the bot service's send, photo, file and edit). Call once after the fields are set.
func (f *LeadFunnel) RecordDialog() {
	if f.dlgOn {
		return
	}
	f.dlgOn = true
	if send := f.send; send != nil {
		f.send = func(ctx context.Context, chat int64, text string, keys map[string]any) error {
			err := send(ctx, chat, text, keys)
			if err == nil {
				f.noteDialog(ctx, chat, "out", text, dlgButtons(keys))
			}
			return err
		}
	}
	if photo := f.Photo; photo != nil {
		f.Photo = func(ctx context.Context, chat int64, key string, p []byte, caption string, keys map[string]any) error {
			err := photo(ctx, chat, key, p, caption, keys)
			if err == nil {
				f.noteDialog(ctx, chat, "out", "[фото] "+caption, dlgButtons(keys))
			}
			return err
		}
	}
	if doc := f.Doc; doc != nil {
		f.Doc = func(ctx context.Context, chat int64, key, name string, data []byte, fileID, caption string, keys map[string]any) error {
			err := doc(ctx, chat, key, name, data, fileID, caption, keys)
			if err == nil {
				f.noteDialog(ctx, chat, "out", "[файл "+name+"] "+caption, dlgButtons(keys))
			}
			return err
		}
	}
	if edit := f.Edit; edit != nil {
		f.Edit = func(ctx context.Context, chat, msgID int64, text string, keys map[string]any) error {
			err := edit(ctx, chat, msgID, text, keys)
			if err == nil {
				f.noteDialog(ctx, chat, "out", text, dlgButtons(keys))
			}
			return err
		}
	}
}

// LeadDialog: the stored dialog of a chat (oldest first), for tests and tools.
func LeadDialog(ctx context.Context, docs funnelDocs, chat int64) []DlgMsg {
	d, err := docs.GetDoc(ctx, "club", inboxDoc)
	if err != nil || d == nil || d.Deleted {
		return nil
	}
	var doc struct {
		Dlg map[string]struct {
			M []DlgMsg `json:"m"`
		} `json:"dlg"`
	}
	_ = json.Unmarshal([]byte(d.Value), &doc)
	return doc.Dlg[strconv.FormatInt(chat, 10)].M
}
