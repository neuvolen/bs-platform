package http

import (
	"errors"
	"net/http"

	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/gin-gonic/gin"
)

func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, users.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, users.ErrAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "already exists"})
	case errors.Is(err, users.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "conflict"})
	case errors.Is(err, users.ErrInvalidArgument):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, users.ErrUnauthorized):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	case errors.Is(err, users.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
