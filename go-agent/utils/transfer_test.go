package utils

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- frame header ---

func TestFrameHeader_RoundTrip(t *testing.T) {
	cases := []FrameHeader{
		{Kind: KindLayer, Ordinal: 1, Name: "u1"},
		{Kind: KindLayer, Ordinal: 12, Name: "u12"},
		{Kind: KindCheckpointFile, Ordinal: 0, Name: "ckpt_app_1234.dmtcp"},
		{Kind: KindDone},
	}
	for _, h := range cases {
		var buf bytes.Buffer
		if err := WriteFrameHeader(&buf, h); err != nil {
			t.Fatalf("write header %+v: %v", h, err)
		}
		if buf.Len() != frameHeaderSize {
			t.Errorf("header size = %d, want %d", buf.Len(), frameHeaderSize)
		}
		got, err := ReadFrameHeader(&buf)
		if err != nil {
			t.Fatalf("read header %+v: %v", h, err)
		}
		if got != h {
			t.Errorf("round trip mismatch: got %+v, want %+v", got, h)
		}
	}
}

func TestFrameHeader_RejectsBadMagic(t *testing.T) {
	buf := make([]byte, frameHeaderSize) // all zero: bad magic
	if _, err := ReadFrameHeader(bytes.NewReader(buf)); err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestFrameHeader_RejectsLongName(t *testing.T) {
	var buf bytes.Buffer
	name := make([]byte, frameNameSize+1)
	for i := range name {
		name[i] = 'x'
	}
	err := WriteFrameHeader(&buf, FrameHeader{Kind: KindLayer, Name: string(name)})
	if err == nil {
		t.Error("expected error for over-long name")
	}
}

// --- frame transfer over net.Pipe ---

func TestSendDirFrame_ReceiveFrame_NetPipe(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantBody := []byte("hello overlay layer")
	if err := os.WriteFile(filepath.Join(src, "sub", "f.txt"), wantBody, 0o640); err != nil {
		t.Fatal(err)
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	sendErr := make(chan error, 1)
	go func() { sendErr <- SendDirFrame(client, 7, "u7", src) }()

	h, err := ReceiveFrame(server, func(h FrameHeader, payload io.Reader) error {
		return ExtractTarGz(payload, dst)
	})
	if err != nil {
		t.Fatalf("ReceiveFrame: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("SendDirFrame: %v", err)
	}
	if h.Kind != KindLayer || h.Ordinal != 7 || h.Name != "u7" {
		t.Errorf("unexpected header %+v", h)
	}
	got, err := os.ReadFile(filepath.Join(dst, "sub", "f.txt"))
	if err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
	if !bytes.Equal(got, wantBody) {
		t.Errorf("content mismatch: got %q want %q", got, wantBody)
	}
}

func TestSendFileFrame_ReceiveFrame_NetPipe(t *testing.T) {
	src := filepath.Join(t.TempDir(), "ckpt_test_1.dmtcp")
	if err := os.WriteFile(src, []byte("dmtcp-image-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	sendErr := make(chan error, 1)
	go func() { sendErr <- SendFileFrame(client, src) }()

	h, err := ReceiveFrame(server, func(h FrameHeader, payload io.Reader) error {
		return ExtractTarGz(payload, dst)
	})
	if err != nil {
		t.Fatalf("ReceiveFrame: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("SendFileFrame: %v", err)
	}
	if h.Kind != KindCheckpointFile || h.Name != "ckpt_test_1.dmtcp" {
		t.Errorf("unexpected header %+v", h)
	}
	got, err := os.ReadFile(filepath.Join(dst, "ckpt_test_1.dmtcp"))
	if err != nil {
		t.Fatalf("extracted checkpoint missing: %v", err)
	}
	if string(got) != "dmtcp-image-bytes" {
		t.Errorf("content mismatch: %q", got)
	}
}

func TestSendDoneFrame_NetPipe(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	sendErr := make(chan error, 1)
	go func() { sendErr <- SendDoneFrame(client) }()

	h, err := ReceiveFrame(server, func(h FrameHeader, payload io.Reader) error {
		t.Error("handler must not be called for DONE frames")
		return nil
	})
	if err != nil {
		t.Fatalf("ReceiveFrame: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("SendDoneFrame: %v", err)
	}
	if h.Kind != KindDone {
		t.Errorf("expected KindDone, got %+v", h)
	}
}

// --- end-to-end over TCP with ReceiveAll ---

func TestReceiveAll_UntilDone(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.bin"), []byte("xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	type result struct {
		frames []FrameHeader
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		frames, err := ReceiveAll(ln, 5*time.Second, func(h FrameHeader, payload io.Reader) error {
			return ExtractTarGz(payload, filepath.Join(dst, h.Name))
		})
		resCh <- result{frames, err}
	}()

	if err := SendDir(addr, 1, "u1", src); err != nil {
		t.Fatalf("SendDir: %v", err)
	}
	if err := SendDir(addr, 2, "u2", src); err != nil {
		t.Fatalf("SendDir 2: %v", err)
	}
	if err := SendDone(addr); err != nil {
		t.Fatalf("SendDone: %v", err)
	}

	res := <-resCh
	if res.err != nil {
		t.Fatalf("ReceiveAll: %v", res.err)
	}
	if len(res.frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(res.frames))
	}
	if res.frames[0].Ordinal != 1 || res.frames[1].Ordinal != 2 {
		t.Errorf("unexpected ordinals: %+v", res.frames)
	}
	for _, name := range []string{"u1", "u2"} {
		if _, err := os.Stat(filepath.Join(dst, name, "data.bin")); err != nil {
			t.Errorf("layer %s not extracted: %v", name, err)
		}
	}
}

// --- extraction safety ---

func TestExtractTarGz_RejectsEscapingPaths(t *testing.T) {
	if _, err := safeJoin("/tmp/dest", "../../etc/passwd"); err == nil {
		t.Error("expected path traversal to be rejected")
	}
	if got, err := safeJoin("/tmp/dest", "ok/file.txt"); err != nil || got != "/tmp/dest/ok/file.txt" {
		t.Errorf("safeJoin valid path: got %q, %v", got, err)
	}
}
