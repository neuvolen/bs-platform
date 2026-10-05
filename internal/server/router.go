package server

import (
	"net/http"
	"time"

	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/bnursik/business_surgery_backend/internal/middleware"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func SetupRouter(registrars ...httpapi.RoutesRegistrar) *gin.Engine {
	r := gin.Default()

		corsConfig := cors.Config{
		AllowOriginFunc: func(origin string) bool {
			return true
		},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders: []string{
			"Authorization", "Content-Type",
		},
		ExposeHeaders: []string{
			"Set-Cookie",
			"ETag", // R44: the Mini App keeps the bundle's tag and asks with _et (app_gzip.go)
		},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}

	// R38a: http → https (301/308) behind Railway's proxy, HSTS on the custom domain
	r.Use(middleware.HTTPS())
	r.Use(cors.New(corsConfig))
	// R45: JSON answers of the API go brotli or gzip (the first sync is ~450 KB of JSON)
	r.Use(middleware.Compress())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"message": "Business Surgery backend is healthy",
		})
	})

	for _, reg := range registrars {
		reg.Register(r)
	}

	return r
}
