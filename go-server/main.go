package main

import (
	"go-client/api"
	"go-client/logs"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

var debug bool = false

func main() {

	//manager := api.PodManager{
	//	Clients:    make(map[*api.Pod]bool),
	//	Register:   make(chan *api.Pod),
	//	Unregister: make(chan *api.Pod),
	//}

	router := gin.Default()

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})

	router.GET("/ready", func(c *gin.Context) {
		// Add service checks here to cluster access
		c.JSON(http.StatusOK, gin.H{
			"status": "ready",
		})
	})

	router.POST("/register", api.RegisterPod)

	router.POST("/migrate", api.MigratePod)

	//api.StartServer(manager)

	logs.LogInfo("Starting up execution with debug = " + strconv.FormatBool(debug))

	router.Run("localhost:8080")

}
