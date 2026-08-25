package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/gin-gonic/gin"
)

type MeDiaryHandler struct {
	svc tracking.Service
}

func NewMeDiaryHandler(svc tracking.Service) *MeDiaryHandler {
	return &MeDiaryHandler{svc: svc}
}

type diaryCreateRequest struct {
	Text string   `json:"text"`
	Mood string   `json:"mood"`
	Tags []string `json:"tags"`
}

// CreateDiary godoc
// @Summary      Create diary entry
// @Description  Создаёт запись дневника текущего пользователя. Тело сохраняется в activity_logs.payload (kind='diary').
// @Tags         users, diary
// @Security     BearerAuth
// @Accept       json
// @Param        body  body      diaryCreateRequest  true  "Diary entry"
// @Produce      json
// @Success      201  {object}  map[string]any
// @Failure      400  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/me/diary [post]
func (h *MeDiaryHandler) CreateDiary(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

	var req diaryCreateRequest
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

	mood := strings.TrimSpace(req.Mood)
	if len(mood) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mood is too long (max 64)"})
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
		"mood": mood,
		"tags": tags,
	}

	item, err := h.svc.CreateDiaryEntry(c.Request.Context(), userID, payload)
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

// ListDiary godoc
// @Summary      List my diary entries
// @Description  Возвращает записи дневника текущего пользователя (kind='diary'). Поддерживает пагинацию.
// @Tags         users, diary
// @Security     BearerAuth
// @Param        limit   query     int     false  "Limit (default 20, max 200)"
// @Param        offset  query     int     false  "Offset (default 0)"
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/me/diary [get]
func (h *MeDiaryHandler) ListDiary(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID, _ := userIDAny.(string)

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

	page, err := h.svc.ListUserDiary(c.Request.Context(), userID, limit, offset)
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
