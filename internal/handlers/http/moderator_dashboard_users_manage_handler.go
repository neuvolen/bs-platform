package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/dashboardusers"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ModeratorDashboardUsersManageHandler struct {
	svc dashboardusers.Service
}

func NewModeratorDashboardUsersManageHandler(
	svc dashboardusers.Service,
) *ModeratorDashboardUsersManageHandler {
	return &ModeratorDashboardUsersManageHandler{svc: svc}
}

// DTO
type updateDashboardUserReq struct {
	Name    string `json:"name"`
	Surname string `json:"surname"`
}

// UpdateDashboardUser godoc
// @Summary      Update participant profile
// @Description  Updates participant name and surname from moderator dashboard
// @Tags         moderator-dashboard
// @Security     BearerAuth
// @Param        id    path     string  true  "User ID (uuid)"
// @Param        body  body     updateDashboardUserReq  true  "Update payload"
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]any
// @Failure      400  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      404  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/moderator/dashboard/users/{id} [put]
func (h *ModeratorDashboardUsersManageHandler) UpdateDashboardUser(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	var req updateDashboardUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Surname = strings.TrimSpace(req.Surname)

	if req.Name == "" || req.Surname == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_and_surname_required"})
		return
	}

	out, err := h.svc.UpdateParticipant(
		c.Request.Context(),
		userID,
		dashboardusers.UpdateParticipantInput{
			Name:    req.Name,
			Surname: req.Surname,
		},
	)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":        out.ID,
		"email":     out.Email,
		"name":      out.Name,
		"surname":   out.Surname,
		"role":      out.Role,
		"createdAt": out.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": out.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// DeleteDashboardUser godoc
// @Summary      Delete participant
// @Description  Deletes participant from moderator dashboard
// @Tags         moderator-dashboard
// @Security     BearerAuth
// @Param        id   path     string  true  "User ID (uuid)"
// @Success      204
// @Failure      400  {object}  map[string]any
// @Failure      401  {object}  map[string]any
// @Failure      403  {object}  map[string]any
// @Failure      404  {object}  map[string]any
// @Failure      500  {object}  map[string]any
// @Router       /api/v1/moderator/dashboard/users/{id} [delete]
func (h *ModeratorDashboardUsersManageHandler) DeleteDashboardUser(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	if err := h.svc.DeleteParticipant(c.Request.Context(), userID); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}
