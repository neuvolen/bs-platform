package http

import (
	"net/http"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/gin-gonic/gin"
)

// swagger:model MeResponse
type MeResponse struct {
	ID        string `json:"id" example:"b3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6f"`
	Email     string `json:"email" example:"user@example.com"`
	Name      string `json:"name" example:"John"`
	Surname   string `json:"surname" example:"Doe"`
	Role      string `json:"role" example:"participant"`
	CreatedAt string `json:"createdAt" example:"2025-12-23T12:34:56Z"`
	UpdatedAt string `json:"updatedAt" example:"2025-12-23T12:34:56Z"`
}

type UsersHandler struct {
	svc users.UsersService
}

// swagger:model UpdateMeRequest
type UpdateMeRequest struct {
	Name    string `json:"name" example:"John"`
	Surname string `json:"surname" example:"Doe"`
}

func NewUsersHandler(svc users.UsersService) *UsersHandler {
	return &UsersHandler{svc: svc}
}

// Me godoc
// @Summary      Get current user
// @Description  Returns profile of currently authenticated user.
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Success      200 {object} MeResponse
// @Failure      401 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/me [get]
func (h *UsersHandler) Me(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	userID, _ := userIDAny.(string)
	u, err := h.svc.GetMe(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":        u.ID.String(),
		"email":     u.Email,
		"name":      u.Name,
		"surname":   u.Surname,
		"role":      u.Role,
		"createdAt": u.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": u.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// UpdateMe godoc
// @Summary      Update current user profile
// @Description  Allows authenticated user to update their own profile (name, surname).
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Param        request body UpdateMeRequest true "Profile update payload"
// @Success      200 {object} MeResponse
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/me [put]
func (h *UsersHandler) UpdateMe(c *gin.Context) {
	userIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID := userIDAny.(string)

	var req UpdateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}

	u, err := h.svc.UpdateMe(c.Request.Context(), userID, req.Name, req.Surname)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":        u.ID.String(),
		"email":     u.Email,
		"name":      u.Name,
		"surname":   u.Surname,
		"role":      u.Role,
		"createdAt": u.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": u.UpdatedAt.UTC().Format(time.RFC3339),
	})
}
