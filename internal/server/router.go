package server

import (
	"net/http"
	"time"

	httpapi "github.com/bnursik/business_surgery_backend/internal/handlers/http"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func SetupRouter(registrars ...httpapi.RoutesRegistrar) *gin.Engine {
	r := gin.Default()

	corsConfig := cors.Config{
		AllowOrigins: []string{
			"http://localhost:3000",
			"https://busines-hirurgiy-ten.vercel.app",
		},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders: []string{
			"Authorization", "Content-Type",
		},
		ExposeHeaders: []string{
			"Set-Cookie",
		},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}

	r.Use(cors.New(corsConfig))

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
