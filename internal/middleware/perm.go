package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func RequirePerm(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, ok := c.Get("perms")
		if !ok {
			c.JSON(http.StatusForbidden, errorResponse{Error: "forbidden"})
			c.Abort()
			return
		}

		perms, _ := v.([]string)
		for _, p := range perms {
			if p == code {
				c.Next()
				return
			}
		}

		c.JSON(http.StatusForbidden, errorResponse{Error: "forbidden"})
		c.Abort()
	}
}

func RequireAnyPerm(codes ...string) gin.HandlerFunc {
	need := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		need[c] = struct{}{}
	}

	return func(c *gin.Context) {
		v, ok := c.Get("perms")
		if !ok {
			c.JSON(http.StatusForbidden, errorResponse{Error: "forbidden"})
			c.Abort()
			return
		}

		perms, _ := v.([]string)
		for _, p := range perms {
			if _, ok := need[p]; ok {
				c.Next()
				return
			}
		}

		c.JSON(http.StatusForbidden, errorResponse{Error: "forbidden"})
		c.Abort()
	}
}
