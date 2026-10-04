package bot

import (
	"context"
	"time"
)

// SystemCheck: R36: the server's self-check as text («🩺 Проверка системы»),
// for the team's /status. Set by the app (handlers/http/syscheck.go).
type SystemCheck func(ctx context.Context) string

func (s *Service) SetSystemCheck(f SystemCheck) {
	if f == nil {
		s.sysCheck.Store(nil)
		return
	}
	s.sysCheck.Store(&f)
}

// systemStatus answers /status of an admin; false when no check is set.
func (s *Service) systemStatus(ctx context.Context, chat int64) bool {
	f := s.sysCheck.Load()
	if f == nil {
		return false
	}
	_ = s.SendMessage(ctx, chat, "🩺 Проверяю систему, это займёт несколько секунд…")
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = s.SendMessage(ctx, chat, (*f)(c))
	return true
}
