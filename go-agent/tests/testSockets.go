// Manual smoke test for the agent transfer channel.
//
//	go run ./tests
//
// Starts a receiver on 127.0.0.1:2486, streams a directory through the
// frame protocol (tar+gzip), sends DONE and prints the result.
package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"go-agent/utils"
)

func main() {
	srcDir, err := os.MkdirTemp("", "mycedrive-src-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(srcDir)
	dstDir, err := os.MkdirTemp("", "mycedrive-dst-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dstDir)

	if err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("payload"), 0o644); err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:2486")
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		frames, err := utils.ReceiveAll(ln, 10*time.Second, func(h utils.FrameHeader, payload io.Reader) error {
			fmt.Printf("received frame kind=%d ordinal=%d name=%s\n", h.Kind, h.Ordinal, h.Name)
			return utils.ExtractTarGz(payload, filepath.Join(dstDir, h.Name))
		})
		fmt.Printf("receiver finished with %d frame(s)\n", len(frames))
		done <- err
	}()

	if err := utils.SendDir("127.0.0.1:2486", 1, "u1", srcDir); err != nil {
		log.Fatal("send dir:", err)
	}
	if err := utils.SendDone("127.0.0.1:2486"); err != nil {
		log.Fatal("send done:", err)
	}
	if err := <-done; err != nil {
		log.Fatal("receive:", err)
	}

	data, err := os.ReadFile(filepath.Join(dstDir, "u1", "hello.txt"))
	if err != nil {
		log.Fatal("verify:", err)
	}
	fmt.Printf("round-trip OK: %q\n", data)
}
