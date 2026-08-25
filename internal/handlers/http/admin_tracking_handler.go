package http

import (
	"net/http"
	"strings"

	"github.com/bnursik/business_surgery_backend/internal/domain/tracking"
	"github.com/gin-gonic/gin"
)

// swagger:model AssignDiseaseRequest
type AssignDiseaseRequest struct {
	// required: true
	UserID string `json:"userId" example:"b3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6f"`
	// required: true
	DiseaseID string `json:"diseaseId" example:"a3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e61"`
}

// swagger:model AssignDiseaseResponse
type AssignDiseaseResponse struct {
	UserDiseaseID string `json:"userDiseaseId" example:"c3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6a"`
	TotalSteps    int    `json:"totalSteps" example:"12"`
}

type AdminTrackingHandler struct {
	svc tracking.Service
}

func NewAdminTrackingHandler(svc tracking.Service) *AdminTrackingHandler {
	return &AdminTrackingHandler{svc: svc}
}

type assignDiseaseRequest struct {
	UserID    string `json:"userId"`
	DiseaseID string `json:"diseaseId"`
}

// AssignDiseaseToUser godoc
//
// @Summary      Assign disease to user
// @Description  Creates user_disease (active) for user, generates user_steps from latest treatment plan and writes activity log (assignment)
// @Tags         admin-tracking
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      AssignDiseaseRequest  true  "Assignment payload"
// @Success      201   {object}  AssignDiseaseResponse
// @Failure      401  {object}  map[string]any  "unauthorized"
// @Failure      403  {object}  map[string]any  "forbidden"
// @Failure      500  {object}  map[string]any  "internal_error"
// @Router       /api/v1/admin/assign-disease [post]
func (h *AdminTrackingHandler) AssignDiseaseToUser(c *gin.Context) {
	var req assignDiseaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	req.DiseaseID = strings.TrimSpace(req.DiseaseID)

	actorIDAny, ok := c.Get("userID")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	actorID, _ := actorIDAny.(string)

	res, err := h.svc.AssignDiseaseToUser(c.Request.Context(), req.UserID, req.DiseaseID, actorID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"userDiseaseId": res.UserDiseaseID,
		"totalSteps":    res.TotalSteps,
	})
}
