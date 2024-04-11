package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"go-agent/overlay"
	"go-agent/utils"
	"log"
	"net"
	"os"
)

type Message struct {
	podName    string
	podAddress string
	isNew      bool
	isMig      bool
}

func main() {
	masterAddr := "localhost:3333"
	rootDir := os.Args[1]
	layerCount := os.Args[2]

	ovLayer := overlay.Layer{RootDir: rootDir}

	message := Message{podAddress: os.Getenv("ip"), isNew: true,
		podName: os.Getenv("name"), isMig: false}

	binBuf := new(bytes.Buffer)
	enc := gob.NewEncoder(binBuf)
	enc.Encode(message)

	conn, err := net.Dial("tcp", masterAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected to server.")

	_, err = conn.Write(binBuf.Bytes())

	log.Println("Registering container at the server with following metadata: ", message)

	if err != nil {
		log.Fatal("Failed to Write Message")
	}

	reply := make([]byte, 1024)

	_, err = conn.Read(reply)

	json.Unmarshal(reply, &message)

	if err != nil {
		log.Fatal("Failed to read response")
	}

	if err != nil {
		log.Printf("Failed to parse response")
	}

	if message.isMig {
		utils.ReceiveData()
		ovLayer.Init()
	} else {
		if message.isNew {
			ovLayer.Init()
		} else {
			for num, _ := range layerCount {
				ovLayer.CreateLayer()
				utils.SendFile(rootDir, message.podAddress, num)
			}
		}
	}
}
