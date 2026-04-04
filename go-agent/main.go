package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go-agent/overlay"
	"go-agent/utils"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Message mirrors the server-side struct for JSON serialisation.
type Message struct {
	PodName    string `json:"podName"`
	PodAddress string `json:"podAddress"`
	IsNew      bool   `json:"isNew"`
	IsMig      bool   `json:"isMig"`
}

// CopyNotification is sent to the /copy endpoint once a checkpoint is ready.
type CopyNotification struct {
	PodName       string `json:"podName"`
	CheckpointDir string `json:"checkpointDir"`
}

const (
	defaultCoordAddr = "http://localhost:80"
	httpTimeout      = 30 * time.Second
)

var httpClient = &http.Client{Timeout: httpTimeout}

func main() {
	// Allow the binary to be called as "end_container" (via symlink or os.Args[0])
	// to implement the Kubernetes preStop lifecycle hook.
	//
	//	Usage: end_container <coordAddr> <podName> <checkpointDir>
	if filepath.Base(os.Args[0]) == "end_container" || (len(os.Args) > 1 && os.Args[1] == "end_container") {
		runEndContainer()
		return
	}

	runAgent()
}

// runEndContainer is the preStop hook entry point.
func runEndContainer() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "end_container" {
		args = args[1:]
	}
	if len(args) < 3 {
		log.Fatal("Usage: end_container <coordAddr> <podName> <checkpointDir>")
	}
	coordAddr, podName, checkpointDir := args[0], args[1], args[2]
	if err := utils.EndContainer(coordAddr, podName, checkpointDir); err != nil {
		log.Fatalf("end_container failed: %v", err)
	}
}

// runAgent is the main Execution Agent loop.
func runAgent() {
	coordAddr := os.Getenv("MIGR_COOR")
	if coordAddr == "" {
		coordAddr = defaultCoordAddr
	} else {
		coordAddr = "http://" + coordAddr
	}

	if len(os.Args) < 3 {
		log.Fatal("Usage: go-agent <rootDir> <layerCount>")
	}
	rootDir := os.Args[1]
	layerCount, err := strconv.Atoi(os.Args[2])
	if err != nil {
		log.Fatalf("Invalid layerCount argument: %v", err)
	}

	ovLayer := overlay.Layer{RootDir: rootDir}

	registerMsg := Message{
		PodAddress: os.Getenv("POD_IP"),
		PodName:    os.Getenv("POD_NAME"),
		IsNew:      true,
		IsMig:      false,
	}
	log.Printf("Registering container with MC at %s: %+v", coordAddr, registerMsg)

	reply, err := postJSON(coordAddr+"/register", registerMsg)
	if err != nil {
		log.Fatalf("Failed to register with Migration Coordinator: %v", err)
	}

	var response Message
	if err := json.Unmarshal(reply, &response); err != nil {
		log.Fatalf("Failed to parse register response: %v", err)
	}

	log.Printf("Register response from MC: %+v", response)

	if response.IsMig {
		log.Println("Pod is migration target â€” waiting for checkpoint transfer")
		utils.ReceiveData()
		log.Println("Checkpoint received, initialising overlay")
		ovLayer.Init()
	} else if response.IsNew {
		log.Println("Fresh start â€” initialising overlay")
		ovLayer.Init()
	} else {
		log.Printf("Source pod â€” creating %d layer(s) and sending to %s", layerCount, response.PodAddress)
		for num := 0; num < layerCount; num++ {
			ovLayer.CreateLayer()
			utils.SendFile(rootDir, response.PodAddress, num)
		}
		log.Println("Checkpoint files sent â€” notifying MC via /copy")
		if _, err := postJSON(coordAddr+"/copy", CopyNotification{
			PodName:       registerMsg.PodName,
			CheckpointDir: rootDir,
		}); err != nil {
			log.Printf("Warning: /copy notification failed: %v", err)
		}
	}
}

// postJSON marshals payload to JSON, POSTs it to url, and returns the response body.
func postJSON(url string, payload interface{}) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("HTTP POST to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned HTTP %d for %s", resp.StatusCode, url)
	}

	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, readErr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if readErr != nil {
			break
		}
	}
	return buf, nil
}
