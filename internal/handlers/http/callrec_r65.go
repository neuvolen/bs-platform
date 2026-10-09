package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R65: «Саму запись онлайн-разбора хранить не нужно». Как только есть
// расшифровка, саммари и его PDF, аудио удаляется (расшифровка и саммари
// остаются). Если саммари не получилось, запись ждёт «Обработать снова»
// 7 дней и тоже удаляется. Первый проход после запуска убирает записи, у
// которых саммари уже было, и пишет в лог, сколько МБ освобождено.

const callRecKeep = 7 * 24 * time.Hour

// callSumReady: расшифровка, саммари и его PDF на месте.
func (h *PlatformAI) callSumReady(ctx context.Context, meta map[string]any, hasText bool) bool {
	if !hasText {
		hasText = strings.TrimSpace(csS(meta["transcript"])) != ""
	}
	if !hasText || len(csM(meta["summary"])) == 0 {
		return false
	}
	pdf := csS(meta["summaryPdf"])
	return pdf != "" && platformIDRe.MatchString(pdf) && h.repo.FileExists(ctx, pdf)
}

// dropCallRec удаляет аудио разбора и отмечает это в meta (recDeleted).
// Возвращает освобождённые байты; meta сохраняет вызывающий.
func (h *PlatformAI) dropCallRec(ctx context.Context, id string, meta map[string]any, why string) (int64, error) {
	fid := csS(meta["audio"])
	if fid == "" || meta["text"] == true {
		return 0, nil
	}
	var size int64
	if platformIDRe.MatchString(fid) {
		size, _ = h.repo.FileSize(ctx, fid)
		if err := h.repo.DeleteFile(ctx, fid); err != nil {
			return 0, err
		}
	}
	delete(meta, "audio")
	if csS(meta["file"]) == fid {
		delete(meta, "file")
	}
	meta["recDeleted"] = map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "why": why, "mb": float64(size*10/1048576) / 10}
	log.Printf("callrec %s: recording %s deleted (%s), %.1f MB freed", id, fid, why, float64(size)/1048576)
	return size, nil
}

// callRecAfterSummary: runCallJob зовёт сразу после саммари и PDF.
func (h *PlatformAI) callRecAfterSummary(ctx context.Context, j *pg.AIJob, meta map[string]any) {
	if !h.callSumReady(ctx, meta, false) {
		return
	}
	if _, err := h.dropCallRec(ctx, j.ID, meta, "summary"); err != nil {
		log.Printf("callrec %s: not deleted: %v", j.ID, err)
	}
}

// CallRecLoop: первый проход через first (разовая уборка старых записей), дальше каждые every.
func (h *PlatformAI) CallRecLoop(ctx context.Context, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Minute)
		h.CallRecSweep(c, time.Now())
		cancel()
		t.Reset(every)
	}
}

// CallRecSweep: один проход (тесты зовут его с заданным временем).
// Возвращает число удалённых записей и освобождённые байты.
func (h *PlatformAI) CallRecSweep(ctx context.Context, now time.Time) (int, int64) {
	jobs, err := h.repo.CallJobsWithAudio(ctx, 2000)
	if err != nil {
		log.Printf("callrec sweep: %v", err)
		return 0, 0
	}
	n, freed := 0, int64(0)
	for _, x := range jobs {
		if x.Status == "queued" || x.Status == "running" {
			continue
		}
		n1, b1 := h.sweepOne(ctx, x, now)
		n += n1
		freed += b1
	}
	if n > 0 || len(jobs) > 0 {
		log.Printf("callrec sweep: %d recordings deleted, %.1f MB freed (%d checked)", n, float64(freed)/1048576, len(jobs))
	}
	return n, freed
}

func (h *PlatformAI) sweepOne(ctx context.Context, x pg.CallRecJob, now time.Time) (int, int64) {
	defer csLock(x.ID)()
	j, meta := h.jobMeta(ctx, x.ID) // свежая версия под замком
	if j == nil || j.Status == "queued" || j.Status == "running" || csS(meta["audio"]) == "" {
		return 0, 0
	}
	why := ""
	switch {
	case h.callSumReady(ctx, meta, x.HasText):
		why = "summary"
	case now.Sub(j.CreatedAt) > callRecKeep && now.Sub(j.UpdatedAt) > callRecKeep:
		why = "7 days without summary"
	default:
		return 0, 0
	}
	size, err := h.dropCallRec(ctx, j.ID, meta, why)
	if err != nil {
		log.Printf("callrec %s: not deleted: %v", j.ID, err)
		return 0, 0
	}
	b, _ := json.Marshal(meta)
	if err := h.repo.UpdateAIJob(ctx, j.ID, j.Status, j.Error, b); err != nil {
		log.Printf("callrec %s: job not saved: %v", j.ID, err)
	}
	if j.BoardID != "" && platformIDRe.MatchString(j.BoardID) {
		if err := h.patchBoardCallRec(ctx, j.BoardID, j.ID, meta["recDeleted"]); err != nil {
			log.Printf("callrec %s: board %s: %v", j.ID, j.BoardID, err)
		}
	}
	return 1, size
}

// patchBoardCallRec: карточка разбора на доске без записи и с отметкой recDeleted.
func (h *PlatformAI) patchBoardCallRec(ctx context.Context, boardID, id string, mark any) error {
	for try := 0; try < 6; try++ {
		b, err := h.repo.GetBoard(ctx, boardID)
		if err != nil || b == nil || b.Deleted {
			return err
		}
		var data map[string]any
		if json.Unmarshal(b.Data, &data) != nil || data == nil {
			return fmt.Errorf("board data")
		}
		calls, _ := data["calls"].([]any)
		hit := false
		for i, c := range calls {
			if m, ok := c.(map[string]any); ok && m["id"] == id {
				delete(m, "audio")
				if m["file"] != nil && m["file"] != "" {
					delete(m, "file")
				}
				m["recDeleted"] = mark
				calls[i], hit = m, true
			}
		}
		if !hit {
			return nil
		}
		data["calls"] = calls
		raw, _ := json.Marshal(data)
		if _, err = h.repo.PutBoard(ctx, boardID, b.Version, raw, "server:call-rec"); err == nil {
			return nil
		}
		if !errors.Is(err, pg.ErrPlatformConflict) {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return pg.ErrPlatformConflict
}
