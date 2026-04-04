package main

import (
	"go-client/api"
	"go-client/logs"
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	router := gin.Default()

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	router.GET("/ready", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	// Execution Agent lifecycle endpoints
	router.POST("/register", api.RegisterPod)
	router.POST("/remove", api.RemovePod)
	router.POST("/copy", api.CopyCheckpoint)

	// Migration trigger
	router.POST("/migrate", api.MigratePod)

	logs.LogInfo("Migration Coordinator starting on 0.0.0.0:80")
	router.Run("0.0.0.0:80")
}
