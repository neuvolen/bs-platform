package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RequireRole lets the request through only if the JWT "role" claim is one
// of the given roles. Must run after AuthJWT.
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(c *gin.Context) {
		v, _ := c.Get("role")
		role, _ := v.(string)
		if _, ok := allowed[role]; !ok {
			c.JSON(http.StatusForbidden, errorResponse{Error: "forbidden"})
			c.Abort()
			return
		}
		c.Next()
	}
}
