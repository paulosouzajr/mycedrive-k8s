package main

import (
	"go-agent/utils"
	"time"
)

func evalSocket() {
	println("Evaluating socket over go-agent")

	println("Opening channel at port 2486")
	go utils.ReceiveData()

	time.Sleep(time.Second * 2)

	utils.SendFile("/home/paulo", "localhost:2486", 1)

	print("File sent")
}

func main() {
	evalSocket()
}
