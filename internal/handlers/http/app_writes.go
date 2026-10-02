package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// Club writes go to the server first (moving the club off the sheet, step 2).
//
// A payment, fine, meeting or resident change from the app or the platform
// is kept on the server and put into its own tables at once, then passed on
// to the script so the sheet stays in step (the script also does the rest:
// calendar, messages, its recalculations). When the script does not take it,
// the write waits on the server and is tried again, in order, until it does:
// nothing the team entered is lost while the script is slow or down.
//
//   - the script refuses it (an error): the server's change is undone and
//     the app shows the script's answer, as before;
//   - the script says it is a double: the sheet had it already, the server's
//     own copy is undone;
//   - no answer in time: the script may have written it. The write waits for
//     the next import: if the sheet shows it, it is done, otherwise it is
//     sent again.
//
// Only the team's writes change the server's tables; anyone else's are
// passed on as before (the script checks who may do what).

// ClubWritesRepo is where the writes are kept.
type ClubWritesRepo interface {
	NewWrite(ctx context.Context, w pg.ClubWrite, apply bool) (*pg.ClubWrite, error)
	WriteSent(ctx context.Context, id int64, result string) error
	WriteRejected(ctx context.Context, id int64, why string, undo bool) error
	WriteDouble(ctx context.Context, id int64, why string) error
	WriteFailed(ctx context.Context, id int64, why string, unknown bool, next time.Time) error
	OpenWrites(ctx context.Context) ([]pg.ClubWrite, error)
	SeenInSheet(ctx context.Context, w *pg.ClubWrite) (*bool, error)
	LastImportAt(ctx context.Context) (*time.Time, error)
}

type ClubWrites struct {
	repo ClubWritesRepo
	gw   *AppGateway
	now  func() time.Time
	// OnChange is called when a write was sent, queued or refused.
	OnChange func()
	// Tables is called when the server's club tables changed (a write applied
	// or undone): the platform's club sections follow at once.
	Tables func()

	send chan struct{} // one sender at a time: writes reach the sheet in order
	wake chan struct{}
}

func NewClubWrites(repo ClubWritesRepo, gw *AppGateway) *ClubWrites {
	return &ClubWrites{repo: repo, gw: gw, now: time.Now, send: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
}

// lockWait: how long the app waits for an earlier write still on its way
// before its own write is left to wait on the server.
const lockWait = 10 * time.Second

func (w *ClubWrites) lock(ctx context.Context, wait time.Duration) bool {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case w.send <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (w *ClubWrites) unlock() { <-w.send }

// queuedAnswer is what the app gets for a write that waits on the server.
var queuedAnswer = []byte(`{"ok":true,"queued":true,"message":"Сохранено на сервере. Таблица обновится, как только ответит."}`)

// writeParams: the call's parameters as the script gets them, without the
// signature (a new one is made for every try).
func writeParams(q url.Values) map[string]string {
	p := map[string]string{}
	for k, v := range q {
		if k == "action" || k == "_tg" || strings.HasPrefix(k, "_srv") || len(v) == 0 {
			continue
		}
		p[k] = v[0]
	}
	return p
}

// retryIn: 30 s, 1, 2, 5, 10 min, then every 30 min.
func retryIn(tries int) time.Duration {
	steps := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	if tries-1 < len(steps) && tries >= 1 {
		return steps[tries-1]
	}
	return 30 * time.Minute
}

// Do runs one write: kept and applied on the server, then sent to the
// script. It returns what the app gets.
func (w *ClubWrites) Do(ctx context.Context, source string, u *platformTgUser, action string, q url.Values, apply bool) []byte {
	who := fullName(u)
	if n, team := w.gw.Admins[u.ID]; team && n != "" {
		who = n
	}
	rec, err := w.repo.NewWrite(ctx, pg.ClubWrite{Source: source, TgID: u.ID, Who: who, Action: action, Params: writeParams(q), At: w.now()}, apply)
	if err != nil {
		// The server's storage failed: the write still goes to the sheet, as before.
		log.Printf("club write %s: not kept: %v", action, err)
		body, gerr := w.gw.get(ctx, q)
		if gerr != nil {
			return mustJSON(map[string]string{"error": gerr.Error()})
		}
		return body
	}
	if rec.ApplyError != "" {
		log.Printf("club write %d %s: server tables: %s", rec.ID, action, rec.ApplyError)
	}
	if rec.Applied {
		w.tablesChanged()
	}
	if !w.lock(ctx, lockWait) {
		w.kick() // another write is on its way: this one follows it
		w.changed()
		return queuedAnswer
	}
	defer w.unlock()
	// Earlier writes still wait: they go first, now (the script may be back).
	if open, err := w.repo.OpenWrites(ctx); err == nil && len(open) > 0 && open[0].ID < rec.ID {
		if _, err := w.flushLocked(ctx, true, rec.ID); err != nil {
			log.Printf("club writes: %v", err)
		}
		if open, err := w.repo.OpenWrites(ctx); err != nil || len(open) == 0 || open[0].ID < rec.ID {
			w.kick()
			w.changed()
			return queuedAnswer
		}
	}
	body, _ := w.deliver(ctx, rec)
	if body == nil {
		return queuedAnswer
	}
	return body
}

// deliver sends one write to the script and records the outcome. body is nil
// when the script did not take it (the write waits); ok is false then too.
func (w *ClubWrites) deliver(ctx context.Context, rec *pg.ClubWrite) ([]byte, bool) {
	defer w.changed()
	in := url.Values{}
	for k, v := range rec.Params {
		in.Set(k, v)
	}
	cid := rec.Params["chatId"]
	if cid == "" {
		cid = strconv.FormatInt(rec.TgID, 10)
	}
	q := w.gw.signed(in, rec.Action, cid)
	body, err := w.gw.get(ctx, q)
	w.gw.note(err == nil, rec.TgID)
	if err != nil {
		unknown := errors.Is(err, errScriptNoAnswer)
		next := w.now().Add(retryIn(rec.Tries + 1))
		if e := w.repo.WriteFailed(context.WithoutCancel(ctx), rec.ID, err.Error(), unknown, next); e != nil {
			log.Printf("club write %d: %v", rec.ID, e)
		}
		return nil, false
	}
	var r map[string]any
	_ = json.Unmarshal(body, &r)
	bg := context.WithoutCancel(ctx)
	switch {
	case r["error"] != nil && r["error"] != "":
		e, _ := r["error"].(string)
		if err := w.repo.WriteRejected(bg, rec.ID, e, !rec.MaybeSent); err != nil {
			log.Printf("club write %d: undo: %v", rec.ID, err)
		}
		w.tablesChanged()
	case r["deduplicated"] == true || r["duplicate"] == true || r["alreadyDone"] == true:
		if err := w.repo.WriteDouble(bg, rec.ID, "дубль: "+string(body)); err != nil {
			log.Printf("club write %d: undo double: %v", rec.ID, err)
		}
		w.tablesChanged()
	default:
		if err := w.repo.WriteSent(bg, rec.ID, string(body)); err != nil {
			log.Printf("club write %d: %v", rec.ID, err)
		}
	}
	w.gw.dropBundles()
	return body, true
}

func (w *ClubWrites) kick() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *ClubWrites) tablesChanged() {
	if w.Tables != nil {
		go w.Tables()
	}
}

func (w *ClubWrites) changed() {
	if w.OnChange != nil {
		go w.OnChange()
	}
}

// Loop sends the waiting writes until ctx ends.
func (w *ClubWrites) Loop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.wake:
		}
		if n, err := w.Flush(ctx); err != nil {
			log.Printf("club writes: %v", err)
		} else if n > 0 {
			log.Printf("club writes: %d reached the sheet", n)
		}
	}
}

// unknownWait: a write without an answer waits this long for an import to
// tell whether the sheet has it; after that it is sent again regardless.
const unknownWait = 3 * time.Hour

// Flush sends the waiting writes in order, stopping at the first that the
// script does not take. It returns how many were done.
func (w *ClubWrites) Flush(ctx context.Context) (int, error) {
	if !w.lock(ctx, time.Minute) {
		return 0, nil // a write is on its way; the next round sends the rest
	}
	defer w.unlock()
	return w.flushLocked(ctx, false, 0)
}

// flushLocked sends the waiting writes older than before (0: all). now: do
// not wait for the next try's time (a new write came, the script may be back).
func (w *ClubWrites) flushLocked(ctx context.Context, now bool, before int64) (int, error) {
	open, err := w.repo.OpenWrites(ctx)
	if err != nil || len(open) == 0 {
		return 0, err
	}
	imp, err := w.repo.LastImportAt(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for i := range open {
		rec := &open[i]
		if before > 0 && rec.ID >= before {
			return done, nil
		}
		t := w.now()
		if rec.NextTryAt.After(t) && !(now && rec.Status == pg.WritePending) {
			return done, nil
		}
		if rec.Status == pg.WriteUnknown {
			fresh := imp != nil && rec.TriedAt != nil && imp.After(*rec.TriedAt)
			if !fresh && rec.TriedAt != nil && t.Sub(*rec.TriedAt) < unknownWait {
				return done, nil // wait for the import to tell
			}
			if fresh {
				if seen, err := w.repo.SeenInSheet(ctx, rec); err == nil && seen != nil && *seen {
					if err := w.repo.WriteSent(ctx, rec.ID, "есть в таблице"); err != nil {
						return done, err
					}
					done++
					w.changed()
					continue
				}
			}
		}
		if _, ok := w.deliver(ctx, rec); !ok {
			return done, nil
		}
		done++
	}
	return done, nil
}

// errScriptNoAnswer: the call left but no answer came; the script may have
// done it.
var errScriptNoAnswer = errors.New("no clear answer from the script")

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
