package utils

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Run executes command with the given space-separated args string and returns stdout.
func Run(command string, args string) []byte {
	cmd := exec.Command(command, strings.Fields(args)...)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("command %q %q failed: %v", command, args, err)
	}
	return out
}

// RemoveRequest is the payload sent to POST /remove.
type RemoveRequest struct {
	PodName string `json:"podName"`
}

// RemoveResponse is the response from POST /remove.
type RemoveResponse struct {
	NeedsCheckpoint bool `json:"needsCheckpoint"`
}

// CopyNotification is the payload sent to POST /copy.
type CopyNotification struct {
	PodName       string `json:"podName"`
	CheckpointDir string `json:"checkpointDir"`
}

// EndContainer implements the preStop lifecycle hook. It calls /remove on the
// Migration Coordinator to determine whether this shutdown is part of a
// migration. If so it waits for DMTCP to write checkpoint files then calls
// /copy to signal that the coordinator can authorise the destination EA.
func EndContainer(coordAddr, podName, checkpointDir string) error {
	body, err := postJSON(fmt.Sprintf("http://%s/remove", coordAddr), RemoveRequest{PodName: podName})
	if err != nil {
		return fmt.Errorf("POST /remove: %w", err)
	}

	var resp RemoveResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse /remove response: %w", err)
	}

	if !resp.NeedsCheckpoint {
		log.Println("normal termination, no checkpoint required")
		return nil
	}

	log.Println("migration termination, requesting DMTCP checkpoint")
	Run("dmtcp_command", "--checkpoint")

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(checkpointDir + "/*.dmtcp")
		if len(matches) > 0 {
			log.Printf("checkpoint ready: %v", matches)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if _, err = postJSON(fmt.Sprintf("http://%s/copy", coordAddr), CopyNotification{
		PodName:       podName,
		CheckpointDir: checkpointDir,
	}); err != nil {
		return fmt.Errorf("POST /copy: %w", err)
	}

	log.Println("checkpoint acknowledged, container stopping")
	return nil
}

// ReceiveData listens on TCP :2486 and writes the first incoming gzip-compressed
// payload to /tmp/dat2. Used by the destination EA to receive checkpoint data.
func ReceiveData() bool {
	listener, err := net.Listen("tcp", ":2486")
	if err != nil {
		log.Fatal(err)
	}
	done := make(chan struct{})
	log.Println("listening for checkpoint data on", listener.Addr())

	go func() {
		defer func() { done <- struct{}{} }()
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Println(err)
				return
			}
			go receiveConn(conn, done)
		}
	}()

	<-done
	return true
}

func receiveConn(c net.Conn, done chan struct{}) {
	defer func() {
		c.Close()
		done <- struct{}{}
	}()

	f, err := os.Create("/tmp/dat2")
	if err != nil {
		log.Println("create /tmp/dat2:", err)
		return
	}
	defer f.Close()

	buf := make([]byte, 1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			dec, derr := decompress(buf[:n])
			if derr != nil {
				log.Println("decompress:", derr)
				return
			}
			f.Write(dec)
		}
		if err != nil {
			if err != io.EOF {
				log.Println(err)
			}
			return
		}
	}
}

// SendFile compresses the file at filePath+"/o"+num with gzip and sends it
// over TCP to podAddr. Used by the source EA to transfer checkpoint data.
func SendFile(filePath string, podAddr string, num int) {
	conn, err := net.Dial("tcp", podAddr)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer conn.Close()

	file, err := os.Open(filePath + "/o" + strconv.Itoa(num))
	if err != nil {
		log.Fatal("open:", err)
	}
	defer file.Close()

	pr, pw := io.Pipe()
	w, err := gzip.NewWriterLevel(pw, 7)
	if err != nil {
		log.Fatal("gzip writer:", err)
	}

	go func() {
		if _, err := io.Copy(w, file); err != nil {
			log.Fatal("compress:", err)
		}
		w.Close()
		pw.Close()
	}()

	n, err := io.Copy(conn, pr)
	if err != nil {
		log.Fatal("send:", err)
	}
	log.Printf("sent %d bytes to %s", n, podAddr)
}

func postJSON(url string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("http post %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func decompress(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// RemoveRequest mirrors the server-side payload for POST /remove.
