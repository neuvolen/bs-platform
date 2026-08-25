package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ModeratorUsersHandler struct {
	svc tracking.Service
}

func NewModeratorUsersHandler(svc tracking.Service) *ModeratorUsersHandler {
	return &ModeratorUsersHandler{svc: svc}
}

type feedbackCreateRequest struct {
	Text   string   `json:"text"`
	Tags   []string `json:"tags"`
	Target struct {
		Type       string  `json:"type"` // "step" | "diary"
		UserStepID *string `json:"userStepId,omitempty"`
		DiaryID    *string `json:"diaryId,omitempty"`
	} `json:"target"`
}

// Progress godoc
// @Summary      Participant progress
// @Description  Возвращает суммарный прогресс участника: кол-во активных проблем, выполненные/все шаги и дату последней активности.
// @Tags         Moderator, Participants
// @Security     BearerAuth
// @Param        id   path      string  true  "User ID (UUID)"
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      400  {object}  map[string]any  "invalid_user_id"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      404  {object}  map[string]any  "not_found"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/users/{id}/progress [get]
func (h *ModeratorUsersHandler) Progress(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	p, err := h.svc.GetUserProgress(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	var last string
	if p.LastActivityAt != nil {
		last = p.LastActivityAt.UTC().Format(time.RFC3339)
	}

	c.JSON(http.StatusOK, gin.H{
		"userId":             p.UserID,
		"activeDiseases":     p.ActiveDiseases,
		"completedSteps":     p.CompletedSteps,
		"totalSteps":         p.TotalSteps,
		"overallProgressPct": p.OverallProgressPct,
		"lastActivityAt":     last,
	})
}

// Diseases godoc
// @Summary      Participant diseases (problems) list
// @Description  Список болезней/проблем участника с прогрессом по шагам плана лечения. Поддерживает пагинацию и фильтр по статусу.
// @Tags         Moderator, Participants
// @Security     BearerAuth
// @Param        id      path      string  true   "User ID (UUID)"
// @Param        status  query     string  false  "Status filter" Enums(active,paused,resolved)
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      400  {object}  map[string]any  "invalid_user_id"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      404  {object}  map[string]any  "not_found"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/users/{id}/diseases [get]
func (h *ModeratorUsersHandler) Diseases(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	status := strings.TrimSpace(c.Query("status"))

	limit := 20
	offset := 0

	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := strings.TrimSpace(c.Query("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}

	page, err := h.svc.ListUserDiseases(c.Request.Context(), userID, status, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	items := make([]gin.H, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, gin.H{
			"userDiseaseId":   it.UserDiseaseID,
			"diseaseId":       it.DiseaseID,
			"diseaseName":     it.DiseaseName,
			"organName":       it.OrganName,
			"categoryName":    it.CategoryName,
			"status":          it.Status,
			"startedAt":       it.StartedAt.UTC().Format(time.RFC3339),
			"updatedAt":       it.UpdatedAt.UTC().Format(time.RFC3339),
			"completedSteps":  it.CompletedSteps,
			"totalSteps":      it.TotalSteps,
			"progressPercent": it.ProgressPercent,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"items":  items,
	})
}

// Activity godoc
// @Summary      Participant activity log
// @Description  Возвращает ленту активности участника (события) с пагинацией.
// @Tags         Moderator, Participants
// @Security     BearerAuth
// @Param        id      path      string  true   "User ID (UUID)"
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      400  {object}  map[string]any  "invalid_user_id"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      404  {object}  map[string]any  "not_found"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/users/{id}/activity [get]
func (h *ModeratorUsersHandler) Activity(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	limit := 20
	offset := 0

	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := strings.TrimSpace(c.Query("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}

	page, err := h.svc.ListUserActivity(c.Request.Context(), userID, limit, offset)
	if err != nil {
		writeError(c, err)
		return
	}

	items := make([]gin.H, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, gin.H{
			"id":        it.ID,
			"type":      it.Type,
			"payload":   it.Payload,
			"createdAt": it.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"items":  items,
	})
}

// TreatmentOverview godoc
// @Summary      Participant treatment overview (diseases + steps + current stage + overall progress)
// @Description  Возвращает болезни участника, шаги по каждой болезни со статусами, текущий шаг (этап) и общий прогресс.
// @Tags         Moderator, Participants
// @Security     BearerAuth
// @Param        id      path      string  true   "User ID (UUID)"
// @Param        status  query     string  false  "Status filter" Enums(active,resolved,archived)
// @Produce      json
// @Success      200  {object}  map[string]any  "OK"
// @Failure      400  {object}  map[string]any  "invalid_user_id"
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      404  {object}  map[string]any  "not_found"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/moderator/users/{id}/treatment [get]
func (h *ModeratorUsersHandler) TreatmentOverview(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	// 1) overall progress
	p, err := h.svc.GetUserProgress(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	var last string
	if p.LastActivityAt != nil {
		last = p.LastActivityAt.UTC().Format(time.RFC3339)
	} else {
		last = ""
	}

	// 2) diseases list (default active in service)
	status := strings.TrimSpace(c.Query("status"))

	diseasesPage, err := h.svc.ListUserDiseases(c.Request.Context(), userID, status, 200, 0)
	if err != nil {
		writeError(c, err)
		return
	}

	diseases := make([]gin.H, 0, len(diseasesPage.Items))

	for _, d := range diseasesPage.Items {
		stepsPage, err := h.svc.ListUserDiseaseSteps(c.Request.Context(), d.UserDiseaseID, 200, 0)
		if err != nil {
			writeError(c, err)
			return
		}

		// current step = первый pending (по created_at ASC, как в repo)
		currentIdx := -1
		for i := range stepsPage.Items {
			if strings.ToLower(strings.TrimSpace(stepsPage.Items[i].State)) == "pending" {
				currentIdx = i
				break
			}
		}

		steps := make([]gin.H, 0, len(stepsPage.Items))

		var currentStep gin.H
		if currentIdx >= 0 {
			cs := stepsPage.Items[currentIdx]
			currentStep = gin.H{
				"userStepId":  cs.ID,
				"stepId":      cs.StepID,
				"title":       cs.StepTitle,
				"description": cs.StepDiscription,
				"state":       cs.State,
				"stepNumber":  currentIdx + 1,
				"totalSteps":  len(stepsPage.Items),
				"uiStatus":    "in_progress",
				"isCompleted": false,
				"isCurrent":   true,
				"completedAt": cs.CompletedAt,
				"createdAt":   cs.CreatedAt.UTC().Format(time.RFC3339),
				"updatedAt":   cs.UpdatedAt.UTC().Format(time.RFC3339),
			}
		} else {
			currentStep = gin.H{}
		}

		for i, s := range stepsPage.Items {
			state := strings.ToLower(strings.TrimSpace(s.State))

			uiStatus := "not_done"
			isCompleted := false

			if state == "active" {
				uiStatus = "in_progress"
				isCompleted = false
			}

			if state == "completed" {
				uiStatus = "done"
				isCompleted = true
			}
			if state == "skipped" {
				// если нужно — можно считать как отдельный статус
				uiStatus = "skipped"
				isCompleted = false
			}

			isCurrent := currentIdx == i && state == "pending"

			steps = append(steps, gin.H{
				"id":            s.ID,
				"userDiseaseId": s.UserDiseaseID,
				"stepId":        s.StepID,
				"title":         s.StepTitle,
				"description":   s.StepDiscription,
				"state":         s.State,
				"uiStatus":      uiStatus, // done / in_progress / not_done / skipped
				"isCompleted":   isCompleted,
				"isCurrent":     isCurrent,
				"stepNumber":    i + 1,
				"completedAt":   s.CompletedAt,
				"createdAt":     s.CreatedAt.UTC().Format(time.RFC3339),
				"updatedAt":     s.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}

		diseases = append(diseases, gin.H{
			"userDiseaseId":   d.UserDiseaseID,
			"diseaseId":       d.DiseaseID,
			"diseaseName":     d.DiseaseName,
			"organName":       d.OrganName,
			"categoryName":    d.CategoryName,
			"status":          d.Status,
			"startedAt":       d.StartedAt.UTC().Format(time.RFC3339),
			"updatedAt":       d.UpdatedAt.UTC().Format(time.RFC3339),
			"completedSteps":  d.CompletedSteps,
			"totalSteps":      d.TotalSteps,
			"progressPercent": d.ProgressPercent,

			"currentStep": currentStep,

			"steps": gin.H{
				"total": stepsPage.Total,
				"items": steps,
			},
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"userId": userID,

		"overall": gin.H{
			"activeDiseases":     p.ActiveDiseases,
			"completedSteps":     p.CompletedSteps,
			"totalSteps":         p.TotalSteps,
			"overallProgressPct": p.OverallProgressPct,
			"lastActivityAt":     last,
		},

		"diseases": gin.H{
			"total": diseasesPage.Total,
			"items": diseases,
		},
	})
}

// CreateFeedback godoc
// @Summary      Create feedback for participant
// @Description  Создаёт обратную связь участнику. Запись сохраняется в activity_logs.payload (kind='feedback'). Можно привязать к шагу (target.type=step + userStepId) или к записи дневника (target.type=diary + diaryId).
// @Tags         Moderator, Participants
// @Security     BearerAuth
// @Accept       json
// @Param        id    path      string               true  "User ID (UUID)"
// @Param        body  body      feedbackCreateRequest true  "Feedback"
// @Produce      json
// @Success      201  {object}  map[string]any
// @Failure      400  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      404  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/moderator/users/{id}/feedback [post]
func (h *ModeratorUsersHandler) CreateFeedback(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	actorIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	actorID, _ := actorIDAny.(string)
	actorID = strings.TrimSpace(actorID)
	if _, err := uuid.Parse(actorID); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req feedbackCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	text := strings.TrimSpace(req.Text)
	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}
	if len(text) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is too long (max 5000)"})
		return
	}

	// normalize tags
	tags := make([]string, 0, len(req.Tags))
	seen := make(map[string]struct{}, len(req.Tags))
	for _, t := range req.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) > 32 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tag is too long (max 32)"})
			return
		}
		key := strings.ToLower(t)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tags = append(tags, t)
		if len(tags) >= 20 {
			break
		}
	}

	payload := map[string]any{
		"text": text,
		"tags": tags,
	}

	tt := strings.TrimSpace(strings.ToLower(req.Target.Type))
	if tt != "" && tt != "step" && tt != "diary" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target.type"})
		return
	}

	if tt == "step" {
		if req.Target.UserStepID == nil || strings.TrimSpace(*req.Target.UserStepID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target.userStepId is required for step target"})
			return
		}
		userStepID := strings.TrimSpace(*req.Target.UserStepID)
		if _, err := uuid.Parse(userStepID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target.userStepId"})
			return
		}
		payload["targetType"] = "step"
		payload["userStepId"] = userStepID
	}

	if tt == "diary" {
		if req.Target.DiaryID == nil || strings.TrimSpace(*req.Target.DiaryID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target.diaryId is required for diary target"})
			return
		}
		diaryID := strings.TrimSpace(*req.Target.DiaryID)
		if _, err := uuid.Parse(diaryID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target.diaryId"})
			return
		}
		payload["targetType"] = "diary"
		payload["diaryId"] = diaryID
	}

	item, err := h.svc.CreateFeedbackEntry(c.Request.Context(), userID, actorID, payload)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":        item.ID,
		"type":      item.Type,
		"payload":   item.Payload,
		"createdAt": item.CreatedAt.UTC().Format(time.RFC3339),
	})
}
