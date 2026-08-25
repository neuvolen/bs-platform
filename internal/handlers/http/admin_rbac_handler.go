package http

import (
	"net/http"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/rbac"
	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// swagger:model AdminUserRolesResponse
type AdminUserRolesResponse struct {
	UserID string   `json:"userId" example:"cbedcee0-1673-432d-b5b9-2a35aabae68c"`
	Roles  []string `json:"roles" example:"participant,moderator"`
}

type AdminRBACHandler struct {
	svc rbac.Service
}

func NewAdminRBACHandler(svc rbac.Service) *AdminRBACHandler {
	return &AdminRBACHandler{svc: svc}
}

// swagger:model AdminAssignRoleRequest
type AdminAssignRoleRequest struct {
	Role string `json:"role" binding:"required" example:"moderator"`
}

// GetUserRoles godoc
// @Summary      Get user roles
// @Description  Returns all roles assigned to a user
// @Tags         admin-rbac
// @Produce      json
// @Security     BearerAuth
// @Param        id   path     string true "User ID (UUID)"
// @Success      200  {object} AdminUserRolesResponse
// @Failure      400  {object} map[string]string "invalid user id"
// @Failure      401  {object} map[string]string "unauthorized"
// @Failure      403  {object} map[string]string "forbidden"
// @Failure      404  {object} map[string]string "user not found"
// @Router       /api/v1/admin/users/{id}/roles [get]
func (h *AdminRBACHandler) GetUserRoles(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	roles, err := h.svc.GetRoles(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"userId": userID,
		"roles":  roles,
	})
}

// AddRole godoc
// @Summary      Assign role to user
// @Description  Adds a role to the user (does not remove existing roles)
// @Tags         admin-rbac
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id      path     string                  true "User ID (UUID)"
// @Param        request body     AdminAssignRoleRequest  true "Role to assign"
// @Success      204
// @Failure      400  {object} map[string]string "invalid input"
// @Failure      401  {object} map[string]string "unauthorized"
// @Failure      403  {object} map[string]string "forbidden"
// @Failure      404  {object} map[string]string "user or role not found"
// @Failure      409  {object} map[string]string "role already assigned"
// @Router       /api/v1/admin/users/{id}/roles [post]
func (h *AdminRBACHandler) AddRole(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	var req AdminAssignRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	role := strings.TrimSpace(strings.ToLower(req.Role))
	if role == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_role"})
		return
	}

	if r := users.Role(role); r != users.RoleParticipant && r != users.RoleModerator && r != users.RoleAdmin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_role"})
		return
	}

	if err := h.svc.AssignRole(c.Request.Context(), userID, role); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// RemoveRole godoc
// @Summary      Remove role from user
// @Description  Removes a role from the user. Cannot remove the last remaining role.
// @Tags         admin-rbac
// @Produce      json
// @Security     BearerAuth
// @Param        id        path string true "User ID (UUID)"
// @Param        roleCode  path string true "Role code (participant | moderator | admin)"
// @Success      204
// @Failure      400  {object} map[string]string "invalid input or last role"
// @Failure      401  {object} map[string]string "unauthorized"
// @Failure      403  {object} map[string]string "forbidden"
// @Failure      404  {object} map[string]string "role not found"
// @Router       /api/v1/admin/users/{id}/roles/{roleCode} [delete]
func (h *AdminRBACHandler) RemoveRole(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("id"))
	if _, err := uuid.Parse(userID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_id"})
		return
	}

	role := strings.TrimSpace(strings.ToLower(c.Param("roleCode")))
	if role == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_role"})
		return
	}

	// Защита: нельзя снять последнюю роль
	roles, err := h.svc.GetRoles(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}
	if len(roles) == 1 && strings.ToLower(roles[0]) == role {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_remove_last_role"})
		return
	}

	if err := h.svc.RemoveRole(c.Request.Context(), userID, role); err != nil {
		writeError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}
