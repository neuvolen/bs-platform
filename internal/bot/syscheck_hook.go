package bot

import (
	"context"
	"strings"
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

type fullStatusKey struct{}

// WantFullStatus: R51: the check was asked as «/status подробно»: the
// SystemCheck adds the details (provider lines, the APIs' answers).
func WantFullStatus(ctx context.Context) bool {
	v, _ := ctx.Value(fullStatusKey{}).(bool)
	return v
}

// statusFull: «/status подробно» (or «full», «детали»).
func statusFull(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "подроб") || strings.Contains(low, "full") || strings.Contains(low, "детал")
}

// systemStatus answers /status of an admin; false when no check is set.
func (s *Service) systemStatus(ctx context.Context, chat int64, full ...bool) bool {
	f := s.sysCheck.Load()
	if f == nil {
		return false
	}
	_ = s.SendMessage(ctx, chat, "🩺 Проверяю систему, это займёт несколько секунд…")
	if len(full) > 0 && full[0] {
		ctx = context.WithValue(ctx, fullStatusKey{}, true)
	}
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = s.SendMessage(ctx, chat, (*f)(c))
	return true
}
