package utils

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func Run(command string, args string) []byte {
	log.Println("running: ", command, args)

	cmd := exec.Command(command, strings.Fields(args)...)
	stdout, err := cmd.Output()

	if err != nil {
		log.Printf("failed to run command: %s %s\n", err.Error(), stdout)
	}

	return stdout
}

func ReceiveData() bool {
	listener, err := net.Listen("tcp", ":2486")
	if err != nil {
		log.Fatal(err)
	}
	done := make(chan struct{})
	log.Println("Server listening on: " + listener.Addr().String())

	go func() {
		defer func() { done <- struct{}{} }()
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Println(err)
				return
			}
			go func(c net.Conn) {
				defer func() {
					c.Close()
					done <- struct{}{}
				}()
				buf := make([]byte, 1024)
				f, err := os.Create("/tmp/dat2")
				check(err)
				defer f.Close()
				for {
					n, err := c.Read(buf)
					if err != nil {
						if err != io.EOF {
							log.Println(err)
						}
						return
					}
					log.Printf("received: %q", buf[:n])
					log.Printf("bytes: %d", n)
					dBuf, err := decompress(buf[:n])
					check(err)
					f.Write(dBuf)
				}

			}(conn)
		}
	}()

	<-done

	return true
}

func decompress(obj []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(obj))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	res, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func SendFile(filePath string, podAddr string, num int) {
	//var wg sync.WaitGroup
	//wg.Add(1)
	println("Starting to send data")

	conn, err := net.Dial("tcp", podAddr)
	check(err)
	log.Println("Connected to server.")

	file, err := os.Open(filePath + "/o" + strconv.Itoa(num))
	check(err)

	pr, pw := io.Pipe()
	w, err := gzip.NewWriterLevel(pw, 7)
	check(err)
	go func() {
		n, err := io.Copy(w, file)
		if err != nil {
			log.Fatal(err)
		}
		w.Close()
		pw.Close()
		log.Printf("copied to piped writer via the compressed writer: %d", n)

	}()

	n, err := io.Copy(conn, pr)
	check(err)
	log.Printf("copied to connection: %d", n)

	conn.Close()

}

func check(e error) {
	if e != nil {
		log.Println(e)
		log.Fatal(e)
	}
}
