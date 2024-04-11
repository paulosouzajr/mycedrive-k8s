package main

import (
	"github.com/gin-gonic/gin"
	"go-client/api"
	"go-client/logs"
	"strconv"
)

var debug bool = false

func main() {

	//manager := api.PodManager{
	//	Clients:    make(map[*api.Pod]bool),
	//	Register:   make(chan *api.Pod),
	//	Unregister: make(chan *api.Pod),
	//}

	router := gin.Default()
	router.POST("/register", api.RegisterPod)

	router.POST("/migrate", api.MigratePod)

	//api.StartServer(manager)

	logs.LogInfo("Starting up execution with debug = " + strconv.FormatBool(debug))

	router.Run("localhost:8080")

}
