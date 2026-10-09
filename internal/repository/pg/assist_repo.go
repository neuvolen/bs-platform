package pg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Доступ ассистента, личные ссылки входа и сессии платформы
// (migrations/0072a_assist_access.sql). Токены сюда приходят только хешем.

// ErrAssistNotFound: приглашения, ссылки или сессии нет (или она уже не действует).
var ErrAssistNotFound = errors.New("not found")

type PlatformAssistant struct {
	ID            int64      `json:"id"`
	ResidentTg    int64      `json:"residentTg"`
	ResidentName  string     `json:"residentName"`
	AssistantTg   int64      `json:"-"`
	AssistantName string     `json:"name"`
	Label         string     `json:"label"`
	Preset        string     `json:"preset"`
	InviteExpires *time.Time `json:"inviteExpires,omitempty"`
	CreatedBy     string     `json:"-"`
	CreatedAt     time.Time  `json:"createdAt"`
	AcceptedAt    *time.Time `json:"acceptedAt,omitempty"`
	LastUsed      *time.Time `json:"lastUsed,omitempty"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	Telegram      bool       `json:"telegram"`
}

// Active: принял приглашение (или вход без Telegram) и не отозван.
func (a *PlatformAssistant) Active() bool { return a.RevokedAt == nil && a.AcceptedAt != nil }

const assistCols = `id, resident_tg, resident_name, assistant_tg, assistant_name, label, preset,
	invite_expires, created_by, created_at, accepted_at, last_used, revoked_at`

func scanAssistant(row pgx.Row) (*PlatformAssistant, error) {
	var a PlatformAssistant
	if err := row.Scan(&a.ID, &a.ResidentTg, &a.ResidentName, &a.AssistantTg, &a.AssistantName, &a.Label, &a.Preset,
		&a.InviteExpires, &a.CreatedBy, &a.CreatedAt, &a.AcceptedAt, &a.LastUsed, &a.RevokedAt); err != nil {
		return nil, err
	}
	a.Telegram = a.AssistantTg != 0
	return &a, nil
}

func (r *PlatformRepo) queryAssistants(ctx context.Context, q string, args ...any) ([]PlatformAssistant, error) {
	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformAssistant{}
	for rows.Next() {
		a, err := scanAssistant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// CreateAssistInvite: приглашение ассистента (ещё никто не принял).
func (r *PlatformRepo) CreateAssistInvite(ctx context.Context, residentTg int64, residentName, label, preset, hash string, expires time.Time, by string) (*PlatformAssistant, error) {
	return scanAssistant(r.db.Pool.QueryRow(ctx, `
		INSERT INTO platform_assistants (resident_tg, resident_name, label, preset, invite_hash, invite_expires, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+assistCols,
		residentTg, residentName, label, preset, hash, expires, by))
}

// CreateAssistantNoTelegram: ассистент, который входит личной ссылкой (команда).
func (r *PlatformRepo) CreateAssistantNoTelegram(ctx context.Context, residentTg int64, residentName, name, preset, by string) (*PlatformAssistant, error) {
	return scanAssistant(r.db.Pool.QueryRow(ctx, `
		INSERT INTO platform_assistants (resident_tg, resident_name, assistant_name, label, preset, created_by, accepted_at)
		VALUES ($1, $2, $3, $3, $4, $5, now()) RETURNING `+assistCols,
		residentTg, residentName, name, preset, by))
}

// InviteByHash: действующее приглашение (не принято, не отозвано, не истекло).
func (r *PlatformRepo) InviteByHash(ctx context.Context, hash string) (*PlatformAssistant, error) {
	a, err := scanAssistant(r.db.Pool.QueryRow(ctx, `SELECT `+assistCols+` FROM platform_assistants
		WHERE invite_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND invite_expires > now()`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistNotFound
	}
	return a, err
}

// AcceptInvite: приглашение принято этим Telegram. Ссылка одноразовая: хеш
// стирается в той же записи, второй раз ею не войти. Прежний доступ того же
// человека к тому же резиденту отзывается (остаётся один).
func (r *PlatformRepo) AcceptInvite(ctx context.Context, hash string, assistantTg int64, name string) (*PlatformAssistant, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a, err := scanAssistant(tx.QueryRow(ctx, `
		UPDATE platform_assistants SET assistant_tg = $2, assistant_name = $3, accepted_at = now(), last_used = now(),
		       invite_hash = NULL
		WHERE invite_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND invite_expires > now()
		RETURNING `+assistCols, hash, assistantTg, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_assistants SET revoked_at = now(), revoked_by = 'replaced'
		WHERE resident_tg = $1 AND assistant_tg = $2 AND id <> $3 AND revoked_at IS NULL`, a.ResidentTg, assistantTg, a.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_sessions SET revoked_at = now()
		WHERE assistant_id IN (SELECT id FROM platform_assistants WHERE resident_tg = $1 AND assistant_tg = $2 AND id <> $3)
		  AND revoked_at IS NULL`, a.ResidentTg, assistantTg, a.ID); err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

func (r *PlatformRepo) AssistantByID(ctx context.Context, id int64) (*PlatformAssistant, error) {
	a, err := scanAssistant(r.db.Pool.QueryRow(ctx, `SELECT `+assistCols+` FROM platform_assistants WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistNotFound
	}
	return a, err
}

// AssistantsOf: ассистенты резидента (действующие и приглашения в пути; отозванные за 30 дней).
func (r *PlatformRepo) AssistantsOf(ctx context.Context, residentTg int64) ([]PlatformAssistant, error) {
	return r.queryAssistants(ctx, `SELECT `+assistCols+` FROM platform_assistants
		WHERE resident_tg = $1 AND (revoked_at IS NULL OR revoked_at > now() - interval '30 days')
		  AND (accepted_at IS NOT NULL OR revoked_at IS NOT NULL OR invite_expires > now())
		ORDER BY revoked_at IS NOT NULL, created_at DESC LIMIT 50`, residentTg)
}

// ServedBy: резиденты, которых обслуживает этот Telegram (действующие доступы).
func (r *PlatformRepo) ServedBy(ctx context.Context, assistantTg int64) ([]PlatformAssistant, error) {
	if assistantTg == 0 {
		return []PlatformAssistant{}, nil
	}
	return r.queryAssistants(ctx, `SELECT `+assistCols+` FROM platform_assistants
		WHERE assistant_tg = $1 AND accepted_at IS NOT NULL AND revoked_at IS NULL
		ORDER BY last_used DESC NULLS LAST, id DESC`, assistantTg)
}

func (r *PlatformRepo) SetAssistPreset(ctx context.Context, id int64, preset string) error {
	t, err := r.db.Pool.Exec(ctx, `UPDATE platform_assistants SET preset = $2 WHERE id = $1 AND revoked_at IS NULL`, id, preset)
	if err == nil && t.RowsAffected() == 0 {
		return ErrAssistNotFound
	}
	return err
}

// RevokeAssistant: доступ закрыт, его сессии и личные ссылки тоже.
func (r *PlatformRepo) RevokeAssistant(ctx context.Context, id int64, by string) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	t, err := tx.Exec(ctx, `UPDATE platform_assistants SET revoked_at = now(), revoked_by = $2, invite_hash = NULL
		WHERE id = $1 AND revoked_at IS NULL`, id, by)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return ErrAssistNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_sessions SET revoked_at = now() WHERE assistant_id = $1 AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_login_links SET revoked_at = now(), revoked_by = $2
		WHERE assistant_id = $1 AND revoked_at IS NULL`, id, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PlatformRepo) TouchAssistant(ctx context.Context, id int64) {
	_, _ = r.db.Pool.Exec(ctx, `UPDATE platform_assistants SET last_used = now() WHERE id = $1`, id)
}

// ── Личные ссылки входа ──

type PlatformLoginLink struct {
	ID           int64      `json:"id"`
	ResidentTg   int64      `json:"-"`
	ResidentName string     `json:"residentName"`
	AssistantID  int64      `json:"assistantId,omitempty"`
	CreatedBy    string     `json:"-"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	LastUsed     *time.Time `json:"lastUsed,omitempty"`
	Uses         int        `json:"uses"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
}

const linkCols = `id, resident_tg, resident_name, assistant_id, created_by, created_at, expires_at, last_used, uses, revoked_at`

func scanLink(row pgx.Row) (*PlatformLoginLink, error) {
	var l PlatformLoginLink
	if err := row.Scan(&l.ID, &l.ResidentTg, &l.ResidentName, &l.AssistantID, &l.CreatedBy, &l.CreatedAt, &l.ExpiresAt, &l.LastUsed, &l.Uses, &l.RevokedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *PlatformRepo) CreateLoginLink(ctx context.Context, hash string, residentTg int64, residentName string, assistantID int64, expires time.Time, by string) (*PlatformLoginLink, error) {
	return scanLink(r.db.Pool.QueryRow(ctx, `
		INSERT INTO platform_login_links (token_hash, resident_tg, resident_name, assistant_id, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+linkCols, hash, residentTg, residentName, assistantID, expires, by))
}

// LinkByHash: действующая ссылка (не отозвана, не истекла).
func (r *PlatformRepo) LinkByHash(ctx context.Context, hash string) (*PlatformLoginLink, error) {
	l, err := scanLink(r.db.Pool.QueryRow(ctx, `SELECT `+linkCols+` FROM platform_login_links
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistNotFound
	}
	return l, err
}

func (r *PlatformRepo) UseLink(ctx context.Context, id int64) {
	_, _ = r.db.Pool.Exec(ctx, `UPDATE platform_login_links SET uses = uses + 1, last_used = now() WHERE id = $1`, id)
}

func (r *PlatformRepo) LinksOf(ctx context.Context, residentTg int64) ([]PlatformLoginLink, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT `+linkCols+` FROM platform_login_links
		WHERE resident_tg = $1 AND revoked_at IS NULL AND expires_at > now() ORDER BY created_at DESC LIMIT 50`, residentTg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformLoginLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// RevokeLink: ссылка больше не открывает платформу, её сессии закрыты.
func (r *PlatformRepo) RevokeLink(ctx context.Context, id int64, by string) (*PlatformLoginLink, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	l, err := scanLink(tx.QueryRow(ctx, `UPDATE platform_login_links SET revoked_at = now(), revoked_by = $2
		WHERE id = $1 AND revoked_at IS NULL RETURNING `+linkCols, id, by))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_sessions SET revoked_at = now() WHERE link_id = $1 AND revoked_at IS NULL`, id); err != nil {
		return nil, err
	}
	return l, tx.Commit(ctx)
}

// ── Сессии ──

type PlatformSession struct {
	ID          string     `json:"id"`
	Sub         string     `json:"-"`
	Actor       string     `json:"-"`
	ActorName   string     `json:"name"`
	Kind        string     `json:"kind"`
	AssistantID int64      `json:"assistantId,omitempty"`
	LinkID      int64      `json:"linkId,omitempty"`
	Device      string     `json:"device"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastSeen    time.Time  `json:"lastSeen"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

const sessCols = `id, sub, actor, actor_name, kind, assistant_id, link_id, device, created_at, last_seen, expires_at, revoked_at`

func scanSession(row pgx.Row) (*PlatformSession, error) {
	var s PlatformSession
	if err := row.Scan(&s.ID, &s.Sub, &s.Actor, &s.ActorName, &s.Kind, &s.AssistantID, &s.LinkID, &s.Device, &s.CreatedAt, &s.LastSeen, &s.ExpiresAt, &s.RevokedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *PlatformRepo) CreateSession(ctx context.Context, s PlatformSession) error {
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO platform_sessions (id, sub, actor, actor_name, kind, assistant_id, link_id, device, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.ID, s.Sub, s.Actor, s.ActorName, s.Kind, s.AssistantID, s.LinkID, s.Device, s.ExpiresAt)
	return err
}

// SessionByID: любая сессия (вызывающий смотрит revoked и срок).
func (r *PlatformRepo) SessionByID(ctx context.Context, id string) (*PlatformSession, error) {
	s, err := scanSession(r.db.Pool.QueryRow(ctx, `SELECT `+sessCols+` FROM platform_sessions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistNotFound
	}
	return s, err
}

func (r *PlatformRepo) TouchSession(ctx context.Context, id string) {
	_, _ = r.db.Pool.Exec(ctx, `UPDATE platform_sessions SET last_seen = now() WHERE id = $1`, id)
}

// SessionsWhere: действующие сессии кабинета sub, по виду входа (kinds пусто: все).
func (r *PlatformRepo) SessionsOf(ctx context.Context, sub string, kinds []string) ([]PlatformSession, error) {
	q := `SELECT ` + sessCols + ` FROM platform_sessions
		WHERE sub = $1 AND revoked_at IS NULL AND expires_at > now()`
	args := []any{sub}
	if len(kinds) > 0 {
		q += ` AND kind = ANY($2)`
		args = append(args, kinds)
	}
	q += ` ORDER BY last_seen DESC LIMIT 100`
	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformSession{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// RevokeSession: только сессия этого кабинета (sub пусто: любая, для команды).
func (r *PlatformRepo) RevokeSession(ctx context.Context, id, sub string) error {
	q := `UPDATE platform_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`
	args := []any{id}
	if sub != "" {
		q += ` AND sub = $2`
		args = append(args, sub)
	}
	t, err := r.db.Pool.Exec(ctx, q, args...)
	if err == nil && t.RowsAffected() == 0 {
		return ErrAssistNotFound
	}
	return err
}

// RevokeOtherSessions: «Выйти на других устройствах»: свои входы кабинета
// (Telegram и ссылки, не ассистенты) кроме текущего, и старые токены без id
// сессии (отсечка по времени выпуска).
func (r *PlatformRepo) RevokeOtherSessions(ctx context.Context, sub, keep string) (int64, error) {
	t, err := r.db.Pool.Exec(ctx, `UPDATE platform_sessions SET revoked_at = now()
		WHERE sub = $1 AND id <> $2 AND kind <> 'assistant' AND revoked_at IS NULL`, sub, keep)
	if err != nil {
		return 0, err
	}
	if _, err := r.db.Pool.Exec(ctx, `INSERT INTO platform_session_cutoff (sub, at) VALUES ($1, now())
		ON CONFLICT (sub) DO UPDATE SET at = now()`, sub); err != nil {
		return 0, err
	}
	return t.RowsAffected(), nil
}

// SessionCutoff: токены этого кабинета без id сессии, выпущенные раньше, не действуют.
func (r *PlatformRepo) SessionCutoff(ctx context.Context, sub string) (time.Time, error) {
	var at time.Time
	err := r.db.Pool.QueryRow(ctx, `SELECT at FROM platform_session_cutoff WHERE sub = $1`, sub).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return at, err
}

// ── Журнал действий ассистентов ──

type PlatformAssistLog struct {
	ID          int64     `json:"id"`
	AssistantID int64     `json:"assistantId"`
	ActorName   string    `json:"name"`
	Action      string    `json:"action"`
	Detail      string    `json:"detail,omitempty"`
	N           int       `json:"n"`
	FirstAt     time.Time `json:"firstAt"`
	At          time.Time `json:"at"`
}

// AssistLog: запись в журнал. Одинаковое действие того же ассистента в
// течение 15 минут не плодит строки (доска сохраняется на каждую правку):
// растёт счётчик и время последнего раза.
func (r *PlatformRepo) AssistLog(ctx context.Context, residentTg, assistantID int64, actor, action, detail string) error {
	t, err := r.db.Pool.Exec(ctx, `UPDATE platform_assist_log SET n = n + 1, at = now()
		WHERE id = (SELECT id FROM platform_assist_log WHERE resident_tg = $1 AND assistant_id = $2 AND action = $3 AND detail = $4
		            AND at > now() - interval '15 minutes' ORDER BY at DESC LIMIT 1)`, residentTg, assistantID, action, detail)
	if err != nil {
		return err
	}
	if t.RowsAffected() > 0 {
		return nil
	}
	_, err = r.db.Pool.Exec(ctx, `INSERT INTO platform_assist_log (resident_tg, assistant_id, actor_name, action, detail)
		VALUES ($1, $2, $3, $4, $5)`, residentTg, assistantID, actor, action, detail)
	return err
}

func (r *PlatformRepo) AssistLogOf(ctx context.Context, residentTg int64, limit int) ([]PlatformAssistLog, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, assistant_id, actor_name, action, detail, n, first_at, at
		FROM platform_assist_log WHERE resident_tg = $1 ORDER BY at DESC LIMIT $2`, residentTg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformAssistLog{}
	for rows.Next() {
		var l PlatformAssistLog
		if err := rows.Scan(&l.ID, &l.AssistantID, &l.ActorName, &l.Action, &l.Detail, &l.N, &l.FirstAt, &l.At); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ── Резидент по имени (карточка команды) ──

// ResidentForAccess: tg id резидента клуба по имени для входа на платформу.
// У резидента без Telegram (Chat ID пуст) это -id его строки в club_residents:
// кабинет живёт под этим номером, пока у него нет Telegram.
func (r *PlatformRepo) ResidentForAccess(ctx context.Context, name string) (tg int64, exact string, telegram bool, err error) {
	var id int64
	var tgID *int64
	var former bool
	norm := normalizeName(name)
	err = r.db.Pool.QueryRow(ctx, `SELECT id, name, tg_id, former FROM club_residents
		WHERE `+sqlNormName("name")+` = $1 ORDER BY former, id DESC LIMIT 1`, norm).Scan(&id, &exact, &tgID, &former)
	if errors.Is(err, pgx.ErrNoRows) {
		// клуб ещё на таблице: список платформы знает резидентов с Telegram
		err = r.db.Pool.QueryRow(ctx, `SELECT tg_id, name FROM platform_residents
			WHERE `+sqlNormName("name")+` = $1 AND active ORDER BY tg_id LIMIT 1`, norm).Scan(&tg, &exact)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", false, ErrAssistNotFound
		}
		return tg, exact, err == nil, err
	}
	if err != nil {
		return 0, "", false, err
	}
	if former {
		return 0, exact, false, ErrAssistNotFound
	}
	if tgID != nil && *tgID > 0 {
		return *tgID, strings.TrimSpace(exact), true, nil
	}
	return -id, strings.TrimSpace(exact), false, nil
}

// normalizeName: «Пётр  Иванов», «петр иванов» и «Пётр Иванов» одно имя.
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	return strings.Join(strings.Fields(s), " ")
}

func sqlNormName(col string) string {
	return `translate(regexp_replace(lower(btrim(` + col + `)), '\s+', ' ', 'g'), 'ё', 'е')`
}
