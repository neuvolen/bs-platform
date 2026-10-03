package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type errorResponse struct {
	Error string `json:"error"`
}

// RoleLead is the platform session of someone who is neither the team nor a
// resident: they see only the lead home (/api/v1/platform/lead/*).
const RoleLead = "lead"

// AuthJWT checks the bearer token. A lead's token is refused here (403
// lead_forbidden): every existing endpoint is closed to leads by default, and
// only the routes registered with AuthJWTAllowLead let them in.
func AuthJWT(secret []byte) gin.HandlerFunc { return authJWT(secret, false) }

// AuthJWTAllowLead is AuthJWT that also lets a lead's token through; the
// route still has to check the role (RequireRole).
func AuthJWTAllowLead(secret []byte) gin.HandlerFunc { return authJWT(secret, true) }

func authJWT(secret []byte, allowLead bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if h == "" || !strings.HasPrefix(strings.ToLower(h), "bearer ") {
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "missing_bearer_token"})
			c.Abort()
			return
		}
		raw := strings.TrimSpace(h[len("Bearer "):])

		tkn, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secret, nil
		})
		if err != nil || !tkn.Valid {
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid_token"})
			c.Abort()
			return
		}

		claims, ok := tkn.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid_claims"})
			c.Abort()
			return
		}

		if typ, _ := claims["typ"].(string); typ != "access" {
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid_token_type"})
			c.Abort()
			return
		}

		if sub, _ := claims["sub"].(string); sub != "" {
			c.Set("userID", sub)
		}
		role, _ := claims["role"].(string)
		if role == RoleLead && !allowLead {
			c.JSON(http.StatusForbidden, errorResponse{Error: "lead_forbidden"})
			c.Abort()
			return
		}
		if role != "" {
			c.Set("role", role)
		}

		perms := make([]string, 0)
		if v, ok := claims["perms"]; ok && v != nil {
			switch vv := v.(type) {
			case []string:
				perms = append(perms, vv...)
			case []any:
				for _, x := range vv {
					if s, ok := x.(string); ok && s != "" {
						perms = append(perms, s)
					}
				}
			}
		}
		c.Set("perms", perms)

		c.Next()
	}
}
