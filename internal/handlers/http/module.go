package http

import "github.com/gin-gonic/gin"

type RoutesRegistrar interface {
	Register(r *gin.Engine)
}
