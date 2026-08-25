package pg

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/jackc/pgx/v5"
)

type TrackingRepo struct {
	db *DB
}

func NewTrackingRepo(db *DB) *TrackingRepo {
	return &TrackingRepo{db: db}
}

func (r *TrackingRepo) GetDashboardStats(ctx context.Context) (tracking.DashboardStats, error) {
	const qParticipants = `
SELECT COUNT(*)
FROM users
WHERE role = 'participant';
`
	var totalParticipants int
	if err := r.db.Pool.QueryRow(ctx, qParticipants).Scan(&totalParticipants); err != nil {
		return tracking.DashboardStats{}, err
	}

	const qActive = `
SELECT COUNT(*)
FROM user_diseases
WHERE status = 'active';
`
	var activeProblems int
	if err := r.db.Pool.QueryRow(ctx, qActive).Scan(&activeProblems); err != nil {
		return tracking.DashboardStats{}, err
	}

	const qAvg = `
WITH participants AS (
  SELECT id
  FROM users
  WHERE role = 'participant'
),
per_user AS (
  SELECT
    p.id AS user_id,
    COALESCE(t.total_steps, 0)     AS total_steps,
    COALESCE(c.completed_steps, 0) AS completed_steps
  FROM participants p
  LEFT JOIN (
    SELECT
      ud.user_id,
      COUNT(ts.id) AS total_steps
    FROM user_diseases ud
    LEFT JOIN treatment_plans tp ON tp.disease_id = ud.disease_id
    LEFT JOIN treatment_steps ts ON ts.plan_id = tp.id
    WHERE ud.status = 'active'
    GROUP BY ud.user_id
  ) t ON t.user_id = p.id
  LEFT JOIN (
    SELECT
      ud.user_id,
      COUNT(us.id) AS completed_steps
    FROM user_diseases ud
    JOIN user_steps us ON us.user_disease_id = ud.id AND us.state = 'completed'
    WHERE ud.status = 'active'
    GROUP BY ud.user_id
  ) c ON c.user_id = p.id
),
per_user_pct AS (
  SELECT
    CASE
      WHEN total_steps = 0 THEN 0
      ELSE FLOOR(100.0 * completed_steps / total_steps)
    END AS pct
  FROM per_user
)
SELECT COALESCE(ROUND(AVG(pct))::int, 0)
FROM per_user_pct;
`
	var avgProgress int
	if err := r.db.Pool.QueryRow(ctx, qAvg).Scan(&avgProgress); err != nil {
		return tracking.DashboardStats{}, err
	}

	return tracking.DashboardStats{
		TotalParticipants:  totalParticipants,
		AvgProgressPercent: avgProgress,
		ActiveProblems:     activeProblems,
	}, nil
}

func (r *TrackingRepo) ListDashboardUsers(ctx context.Context, q string, limit, offset int) (tracking.DashboardUsersPage, error) {
	q = strings.TrimSpace(strings.ToLower(q))

	const qCount = `
SELECT COUNT(*)
FROM users u
WHERE u.role = 'participant'
  AND (
    $1 = ''
    OR LOWER(u.email) LIKE '%' || $1 || '%'
    OR LOWER(u.name) LIKE '%' || $1 || '%'
    OR LOWER(u.surname) LIKE '%' || $1 || '%'
  );
`
	var total int
	if err := r.db.Pool.QueryRow(ctx, qCount, q).Scan(&total); err != nil {
		return tracking.DashboardUsersPage{}, err
	}

	const qList = `
WITH base AS (
  SELECT u.id, u.email, u.name, u.surname
  FROM users u
  WHERE u.role = 'participant'
    AND (
      $1 = ''
      OR LOWER(u.email) LIKE '%' || $1 || '%'
      OR LOWER(u.name) LIKE '%' || $1 || '%'
      OR LOWER(u.surname) LIKE '%' || $1 || '%'
    )
  ORDER BY u.created_at DESC
  LIMIT $2 OFFSET $3
),
active_diseases AS (
  SELECT ud.user_id, COUNT(*) AS active_diseases
  FROM user_diseases ud
  WHERE ud.status = 'active'
  GROUP BY ud.user_id
),
total_steps AS (
  SELECT
    ud.user_id,
    COUNT(ts.id) AS total_steps
  FROM user_diseases ud
  LEFT JOIN treatment_plans tp ON tp.disease_id = ud.disease_id
  LEFT JOIN treatment_steps ts ON ts.plan_id = tp.id
  WHERE ud.status = 'active'
  GROUP BY ud.user_id
),
completed_steps AS (
  SELECT
    ud.user_id,
    COUNT(us.id) AS completed_steps
  FROM user_diseases ud
  JOIN user_steps us ON us.user_disease_id = ud.id AND us.state = 'completed'
  WHERE ud.status = 'active'
  GROUP BY ud.user_id
),
last_activity AS (
  SELECT al.user_id, MAX(al.created_at) AS last_activity_at
  FROM activity_logs al
  GROUP BY al.user_id
)
SELECT
  b.id,
  b.email,
  b.name,
  b.surname,
  la.last_activity_at,
  COALESCE(ad.active_diseases, 0)   AS active_diseases,
  COALESCE(cs.completed_steps, 0)  AS completed_steps,
  COALESCE(ts.total_steps, 0)      AS total_steps
FROM base b
LEFT JOIN last_activity la ON la.user_id = b.id
LEFT JOIN active_diseases ad ON ad.user_id = b.id
LEFT JOIN completed_steps cs ON cs.user_id = b.id
LEFT JOIN total_steps ts ON ts.user_id = b.id
ORDER BY la.last_activity_at DESC NULLS LAST, b.surname, b.name;
`

	rows, err := r.db.Pool.Query(ctx, qList, q, limit, offset)
	if err != nil {
		return tracking.DashboardUsersPage{}, err
	}
	defer rows.Close()

	items := make([]tracking.DashboardUserItem, 0, limit)

	for rows.Next() {
		var (
			id             string
			email          string
			name           string
			surname        string
			lastActivity   *time.Time
			activeDiseases int
			completed      int
			totalSteps     int
		)

		if err := rows.Scan(&id, &email, &name, &surname, &lastActivity, &activeDiseases, &completed, &totalSteps); err != nil {
			return tracking.DashboardUsersPage{}, err
		}

		pct := 0
		if totalSteps > 0 {
			pct = int(math.Floor(100.0 * float64(completed) / float64(totalSteps)))
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
		}

		items = append(items, tracking.DashboardUserItem{
			ID:                 id,
			Email:              email,
			Name:               name,
			Surname:            surname,
			LastActivityAt:     lastActivity,
			ActiveDiseases:     activeDiseases,
			CompletedSteps:     completed,
			TotalSteps:         totalSteps,
			OverallProgressPct: pct,
		})
	}

	if err := rows.Err(); err != nil {
		return tracking.DashboardUsersPage{}, err
	}

	return tracking.DashboardUsersPage{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	}, nil
}

func (r *TrackingRepo) GetUserProgress(ctx context.Context, userID string) (tracking.UserProgress, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return tracking.UserProgress{}, users.ErrInvalidArgument
	}

	const qUser = `
SELECT 1
FROM users
WHERE id = $1::uuid;
`
	var one int
	if err := r.db.Pool.QueryRow(ctx, qUser, userID).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tracking.UserProgress{}, users.ErrNotFound
		}
		return tracking.UserProgress{}, err
	}

	const qAgg = `
WITH ad AS (
  SELECT COUNT(*) AS active_diseases
  FROM user_diseases
  WHERE user_id = $1::uuid AND status = 'active'
),
ts AS (
  SELECT COUNT(tst.id) AS total_steps
  FROM user_diseases ud
  LEFT JOIN treatment_plans tp ON tp.disease_id = ud.disease_id
  LEFT JOIN treatment_steps tst ON tst.plan_id = tp.id
  WHERE ud.user_id = $1::uuid AND ud.status = 'active'
),
cs AS (
  SELECT COUNT(us.id) AS completed_steps
  FROM user_diseases ud
  JOIN user_steps us ON us.user_disease_id = ud.id AND us.state = 'completed'
  WHERE ud.user_id = $1::uuid AND ud.status = 'active'
),
la AS (
  SELECT MAX(created_at) AS last_activity_at
  FROM activity_logs
  WHERE user_id = $1::uuid
)
SELECT
  (SELECT active_diseases FROM ad) AS active_diseases,
  (SELECT total_steps FROM ts) AS total_steps,
  (SELECT completed_steps FROM cs) AS completed_steps,
  (SELECT last_activity_at FROM la) AS last_activity_at;
`

	var (
		activeDiseases int
		totalSteps     int
		completedSteps int
		lastActivity   *time.Time
	)

	if err := r.db.Pool.QueryRow(ctx, qAgg, userID).Scan(&activeDiseases, &totalSteps, &completedSteps, &lastActivity); err != nil {
		return tracking.UserProgress{}, err
	}

	pct := 0
	if totalSteps > 0 {
		pct = int(math.Floor(100.0 * float64(completedSteps) / float64(totalSteps)))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
	}

	return tracking.UserProgress{
		UserID:             userID,
		ActiveDiseases:     activeDiseases,
		CompletedSteps:     completedSteps,
		TotalSteps:         totalSteps,
		OverallProgressPct: pct,
		LastActivityAt:     lastActivity,
	}, nil
}

func (r *TrackingRepo) ListUserDiseases(ctx context.Context, userID, status string, limit, offset int) (tracking.UserDiseasesPage, error) {
	userID = strings.TrimSpace(userID)
	status = strings.TrimSpace(strings.ToLower(status))

	if userID == "" {
		return tracking.UserDiseasesPage{}, users.ErrInvalidArgument
	}
	if status == "" {
		status = "active"
	}

	const qCount = `
SELECT COUNT(*)
FROM user_diseases ud
WHERE ud.user_id = $1::uuid
  AND ($2 = '' OR ud.status = $2);
`
	var total int
	if err := r.db.Pool.QueryRow(ctx, qCount, userID, status).Scan(&total); err != nil {
		return tracking.UserDiseasesPage{}, err
	}

	const qList = `
WITH base AS (
  SELECT
    ud.id,
    ud.disease_id,
    ud.status,
    ud.assigned_at,
    ud.updated_at
  FROM user_diseases ud
  WHERE ud.user_id = $1::uuid
    AND ($2 = '' OR ud.status = $2)
  ORDER BY ud.updated_at DESC
  LIMIT $3 OFFSET $4
),
steps_total AS (
  SELECT
    b.id AS user_disease_id,
    COUNT(ts.id) AS total_steps
  FROM base b
  JOIN user_diseases ud ON ud.id = b.id
  LEFT JOIN treatment_plans tp ON tp.disease_id = ud.disease_id
  LEFT JOIN treatment_steps ts ON ts.plan_id = tp.id
  GROUP BY b.id
),
steps_completed AS (
  SELECT
    us.user_disease_id,
    COUNT(*) AS completed_steps
  FROM user_steps us
  WHERE us.state = 'completed'
    AND us.user_disease_id IN (SELECT id FROM base)
  GROUP BY us.user_disease_id
)
SELECT
  b.id AS user_disease_id,
  b.disease_id,
  d.title AS disease_title,
  o.title AS organ_title,
  c.title AS category_title,
  b.status,
  b.assigned_at,
  b.updated_at,
  COALESCE(sc.completed_steps, 0) AS completed_steps,
  COALESCE(st.total_steps, 0) AS total_steps
FROM base b
JOIN diseases d ON d.id = b.disease_id
LEFT JOIN organs o ON o.id = d.organ_id
LEFT JOIN disease_categories c ON c.id = d.category_id
LEFT JOIN steps_total st ON st.user_disease_id = b.id
LEFT JOIN steps_completed sc ON sc.user_disease_id = b.id
ORDER BY b.updated_at DESC;
`

	rows, err := r.db.Pool.Query(ctx, qList, userID, status, limit, offset)
	if err != nil {
		return tracking.UserDiseasesPage{}, err
	}
	defer rows.Close()

	items := make([]tracking.UserDiseaseItem, 0, limit)

	for rows.Next() {
		var it tracking.UserDiseaseItem
		if err := rows.Scan(
			&it.UserDiseaseID,
			&it.DiseaseID,
			&it.DiseaseName,
			&it.OrganName,
			&it.CategoryName,
			&it.Status,
			&it.StartedAt,
			&it.UpdatedAt,
			&it.CompletedSteps,
			&it.TotalSteps,
		); err != nil {
			return tracking.UserDiseasesPage{}, err
		}

		pct := 0
		if it.TotalSteps > 0 {
			pct = int(math.Floor(100.0 * float64(it.CompletedSteps) / float64(it.TotalSteps)))
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
		}
		it.ProgressPercent = pct

		items = append(items, it)
	}

	if err := rows.Err(); err != nil {
		return tracking.UserDiseasesPage{}, err
	}

	return tracking.UserDiseasesPage{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	}, nil
}

func (r *TrackingRepo) ListUserActivity(ctx context.Context, userID string, limit, offset int) (tracking.ActivityPage, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return tracking.ActivityPage{}, users.ErrInvalidArgument
	}

	const qCount = `
SELECT COUNT(*)
FROM activity_logs
WHERE user_id = $1::uuid;
`
	var total int
	if err := r.db.Pool.QueryRow(ctx, qCount, userID).Scan(&total); err != nil {
		return tracking.ActivityPage{}, err
	}

	const qList = `
SELECT id, kind, payload, created_at
FROM activity_logs
WHERE user_id = $1::uuid
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
`

	rows, err := r.db.Pool.Query(ctx, qList, userID, limit, offset)
	if err != nil {
		return tracking.ActivityPage{}, err
	}
	defer rows.Close()

	items := make([]tracking.ActivityItem, 0, limit)

	for rows.Next() {
		var (
			id        string
			typ       string
			payloadB  []byte
			createdAt time.Time
		)
		if err := rows.Scan(&id, &typ, &payloadB, &createdAt); err != nil {
			return tracking.ActivityPage{}, err
		}

		var payload any
		if len(payloadB) > 0 {
			_ = json.Unmarshal(payloadB, &payload)
		}

		items = append(items, tracking.ActivityItem{
			ID:        id,
			Type:      typ,
			Payload:   payload,
			CreatedAt: createdAt,
		})
	}

	if err := rows.Err(); err != nil {
		return tracking.ActivityPage{}, err
	}

	return tracking.ActivityPage{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	}, nil
}
