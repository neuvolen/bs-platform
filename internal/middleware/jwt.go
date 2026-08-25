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

func AuthJWT(secret []byte) gin.HandlerFunc {
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
		if role, _ := claims["role"].(string); role != "" {
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
