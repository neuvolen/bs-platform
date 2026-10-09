package http

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/club"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R69 at start: once, the payments since the cutover are applied by the
// ledger (club_alloc.go) and the meetings counters rebuilt from the log
// (club_meetcount.go). First a dry run in the log (names and amounts only),
// then, after R69ApplyDelay, the same for real. Every later start only
// recounts the meetings from the log (a counter cannot drift).

// R69ApplyDelay: between the dry run and the real run.
var R69ApplyDelay = time.Minute // the dry run was reviewed in the logs (09.10.2026): a minute, so frequent deploys do not keep postponing it

type r69Repo interface {
	Master(ctx context.Context) (string, error)
	AllocBackfill(ctx context.Context, dry bool) ([]pg.AllocChange, []string, error)
	AllocBackfillDone(ctx context.Context) bool
	MeetBackfill(ctx context.Context, dry bool) ([]pg.MeetChange, []string, error)
	MeetBackfillDone(ctx context.Context) bool
	RecountMeetings(ctx context.Context) ([]pg.MeetChange, error)
}

func r69LogAlloc(tag string, ch []pg.AllocChange, notes []string) {
	for _, n := range notes {
		log.Printf("r69 %s: %s", tag, n)
	}
	for _, c := range ch {
		log.Printf("r69 %s: %s: долг %s → %s ₸, штрафы к оплате %s → %s ₸", tag, c.Resident,
			club.FmtMoney(c.DebtBefore), club.FmtMoney(c.DebtAfter), club.FmtMoney(c.FinesBefore), club.FmtMoney(c.FinesAfter))
		for _, r := range c.Rows {
			log.Printf("r69 %s:   %s", tag, r)
		}
	}
	log.Printf("r69 %s: %d residents", tag, len(ch))
}

func r69LogMeet(tag string, ch []pg.MeetChange, notes []string) {
	for _, n := range notes {
		log.Printf("r69 %s: %s", tag, n)
	}
	for _, c := range ch {
		mark := ""
		if c.Before != c.After {
			mark = " (изменено)"
		}
		days := ""
		if len(c.Days) > 0 {
			days = " | " + strings.Join(c.Days, " ")
		}
		log.Printf("r69 %s: %s: встречи %d/%d → %d/%d%s; %s%s", tag, c.Resident, c.Before, c.Granted, c.After, c.Granted, mark, c.Why, days)
	}
}

// ClubR69AtStart runs it; refresh rebuilds the platform's club data.
func ClubR69AtStart(ctx context.Context, repo r69Repo, refresh func()) {
	if m, err := repo.Master(ctx); err != nil || m != "server" {
		return
	}
	pending := false
	if !repo.MeetBackfillDone(ctx) {
		ch, notes, err := repo.MeetBackfill(ctx, true)
		if err != nil {
			log.Printf("r69 meetings dry run: %v", err)
		} else {
			r69LogMeet("meetings dry run", ch, notes)
			pending = true
		}
	} else if ch, err := repo.RecountMeetings(ctx); err != nil {
		log.Printf("r69 meetings recount: %v", err)
	} else if len(ch) > 0 {
		r69LogMeet("meetings recount", ch, nil)
		if refresh != nil {
			refresh()
		}
	}
	if !repo.AllocBackfillDone(ctx) {
		ch, notes, err := repo.AllocBackfill(ctx, true)
		if err != nil {
			log.Printf("r69 payments dry run: %v", err)
		} else {
			r69LogAlloc("payments dry run", ch, notes)
			pending = true
		}
	}
	if !pending {
		return
	}
	log.Printf("r69: applying in %s", R69ApplyDelay)
	go func() {
		time.Sleep(R69ApplyDelay)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := r69Apply(ctx, repo); err != nil {
			log.Printf("r69 apply: %v", err)
		}
		if refresh != nil {
			refresh()
		}
	}()
}

func r69Apply(ctx context.Context, repo r69Repo) error {
	if !repo.MeetBackfillDone(ctx) {
		ch, notes, err := repo.MeetBackfill(ctx, false)
		if err != nil {
			return fmt.Errorf("meetings: %w", err)
		}
		r69LogMeet("meetings applied", ch, notes)
	}
	if !repo.AllocBackfillDone(ctx) {
		ch, notes, err := repo.AllocBackfill(ctx, false)
		if err != nil {
			return fmt.Errorf("payments: %w", err)
		}
		r69LogAlloc("payments applied", ch, notes)
	}
	return nil
}
