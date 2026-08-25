package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/rbac"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AdminUsersHandler struct {
	users users.UserRepository
	rbac  rbac.Service
}

func NewAdminUsersHandler(usersRepo users.UserRepository, rbacSvc rbac.Service) *AdminUsersHandler {
	return &AdminUsersHandler{users: usersRepo, rbac: rbacSvc}
}

// swagger:model AdminUserItem
type AdminUserItem struct {
	ID      string   `json:"id" example:"cbedcee0-1673-432d-b5b9-2a35aabae68c"`
	Email   string   `json:"email" example:"user@example.com"`
	Name    string   `json:"name" example:"John"`
	Surname string   `json:"surname" example:"Doe"`
	Roles   []string `json:"roles" example:"participant,moderator"`
}

// swagger:model AdminUsersListResponse
type AdminUsersListResponse struct {
	Total  int             `json:"total" example:"42"`
	Limit  int             `json:"limit" example:"20"`
	Offset int             `json:"offset" example:"0"`
	Items  []AdminUserItem `json:"items"`
}

// swagger:model AdminUserPermissionsResponse
type AdminUserPermissionsResponse struct {
	UserID      string   `json:"userId" example:"cbedcee0-1673-432d-b5b9-2a35aabae68c"`
	Permissions []string `json:"permissions" example:"rbac:manage,catalog:read"`
}

// ListUsers godoc
// @Summary      List users (admin)
// @Description  Returns users list with roles. Supports pagination and text search by email, name, surname.
// @Tags         admin-users
// @Produce      json
// @Security     BearerAuth
// @Param        q       query    string false "Search query"
// @Param        limit   query    int    false "Limit (default 20, max 200)"
// @Param        offset  query    int    false "Offset (default 0)"
// @Success      200 {object} AdminUsersListResponse
// @Failure      401 {object} map[string]string "unauthorized"
// @Failure      403 {object} map[string]string "forbidden"
// @Failure      500 {object} map[string]string
// @Router       /api/v1/admin/users [get]
func (h *AdminUsersHandler) ListUsers(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))

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

	us, total, err := h.users.List(c.Request.Context(), q, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	items := make([]AdminUserItem, 0, len(us))
	for _, u := range us {
		roles, rErr := h.rbac.GetRoles(c.Request.Context(), u.ID.String())
		if rErr != nil {
			writeError(c, rErr)
			return
		}

		items = append(items, AdminUserItem{
			ID:      u.ID.String(),
			Email:   u.Email,
			Name:    u.Name,
			Surname: u.Surname,
			Roles:   roles,
		})
	}

	c.JSON(http.StatusOK, AdminUsersListResponse{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	})
}

// GetUserPermissions godoc
// @Summary      Get user permissions (admin)
// @Description  Returns permissions assigned to a user via RBAC tables (roles -> permissions). Useful for debugging RBAC.
// @Tags         admin-users
// @Produce      json
// @Security     BearerAuth
// @Param        id  path string true "User ID (UUID)"
// @Success      200 {object} AdminUserPermissionsResponse
// @Failure      400 {object} map[string]string "invalid_user_id"
// @Failure      401 {object} map[string]string "unauthorized"
// @Failure      403 {object} map[string]string "forbidden"
// @Failure      404 {object} map[string]string "not found"
// @Failure      500 {object} map[string]string
// @Router       /api/v1/admin/users/{id}/permissions [get]
func (h *AdminUsersHandler) GetUserPermissions(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	perms, err := h.rbac.GetPermissions(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, AdminUserPermissionsResponse{
		UserID:      userID,
		Permissions: perms,
	})
}
