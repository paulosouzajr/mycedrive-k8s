package utils

// Transfer protocol between source and destination Execution Agents.
//
// Each item (an overlay layer directory, a DMTCP checkpoint file, or the
// final DONE marker) is sent over its own TCP connection as a single frame:
//
//	[48-byte header][gzip-compressed tar payload]
//
// Header layout (big-endian):
//	[0:4)   magic   0xDEADBEEF
//	[4:8)   version 1
//	[8:12)  kind    0=volume layer dir, 1=checkpoint file, 2=done
//	[12:16) ordinal layer number (0 for checkpoint files / done)
//	[16:48) name    null-padded item name (layer dir or file base name)
//
// After fully processing the payload the receiver writes a single ACK byte
// (0x06) back on the same connection. The sender blocks until the ACK is
// read, which gives the source-side preStop hook its "block until the
// destination has the data" semantics.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	frameMagic      = 0xDEADBEEF
	frameVersion    = 1
	frameHeaderSize = 48
	frameNameSize   = 32
	ackByte         = 0x06

	// DefaultTransferPort is used when CONTAINER_PORT is not set.
	DefaultTransferPort = 2486
)

// FrameKind identifies the payload type of a transfer frame.
type FrameKind uint32

const (
	KindLayer          FrameKind = 0 // tar of an overlay layer directory
	KindCheckpointFile FrameKind = 1 // tar containing a single checkpoint file
	KindDone           FrameKind = 2 // end of transfer, no payload
)

// FrameHeader describes one transfer frame.
type FrameHeader struct {
	Kind    FrameKind
	Ordinal int
	Name    string
}

// TransferPort returns the TCP port used for checkpoint transfer, taken from
// CONTAINER_PORT when valid, falling back to DefaultTransferPort.
func TransferPort() int {
	return EnvInt("CONTAINER_PORT", DefaultTransferPort)
}

// WriteFrameHeader encodes h and writes it to w.
func WriteFrameHeader(w io.Writer, h FrameHeader) error {
	if len(h.Name) > frameNameSize {
		return fmt.Errorf("frame name %q longer than %d bytes", h.Name, frameNameSize)
	}
	buf := make([]byte, frameHeaderSize)
	binary.BigEndian.PutUint32(buf[0:4], frameMagic)
	binary.BigEndian.PutUint32(buf[4:8], frameVersion)
	binary.BigEndian.PutUint32(buf[8:12], uint32(h.Kind))
	binary.BigEndian.PutUint32(buf[12:16], uint32(h.Ordinal))
	copy(buf[16:16+frameNameSize], h.Name)
	_, err := w.Write(buf)
	return err
}

// ReadFrameHeader reads and validates a frame header from r.
func ReadFrameHeader(r io.Reader) (FrameHeader, error) {
	buf := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return FrameHeader{}, fmt.Errorf("read frame header: %w", err)
	}
	if magic := binary.BigEndian.Uint32(buf[0:4]); magic != frameMagic {
		return FrameHeader{}, fmt.Errorf("bad frame magic 0x%08X", magic)
	}
	if v := binary.BigEndian.Uint32(buf[4:8]); v != frameVersion {
		return FrameHeader{}, fmt.Errorf("unsupported frame version %d", v)
	}
	h := FrameHeader{
		Kind:    FrameKind(binary.BigEndian.Uint32(buf[8:12])),
		Ordinal: int(binary.BigEndian.Uint32(buf[12:16])),
		Name:    strings.TrimRight(string(buf[16:16+frameNameSize]), "\x00"),
	}
	return h, nil
}

// SendDirFrame writes a layer frame for dir over rw and waits for the ACK.
func SendDirFrame(rw io.ReadWriter, ordinal int, name, dir string) error {
	if err := WriteFrameHeader(rw, FrameHeader{Kind: KindLayer, Ordinal: ordinal, Name: name}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	gw := gzip.NewWriter(rw)
	tw := tar.NewWriter(gw)
	if err := tarDir(dir, tw); err != nil {
		return fmt.Errorf("tar %s: %w", dir, err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}
	return readAck(rw)
}

// SendFileFrame writes a checkpoint-file frame containing the single file at
// path over rw and waits for the ACK.
func SendFileFrame(rw io.ReadWriter, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	name := filepath.Base(path)
	if err := WriteFrameHeader(rw, FrameHeader{Kind: KindCheckpointFile, Name: name}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	gw := gzip.NewWriter(rw)
	tw := tar.NewWriter(gw)
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("tar header %s: %w", path, err)
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	_, copyErr := io.Copy(tw, f)
	f.Close()
	if copyErr != nil {
		return fmt.Errorf("copy %s: %w", path, copyErr)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}
	return readAck(rw)
}

// SendDoneFrame signals the end of the transfer and waits for the ACK.
func SendDoneFrame(rw io.ReadWriter) error {
	if err := WriteFrameHeader(rw, FrameHeader{Kind: KindDone}); err != nil {
		return fmt.Errorf("write done header: %w", err)
	}
	return readAck(rw)
}

// FrameHandler consumes the payload of one frame. The payload reader yields
// the gzip-compressed tar stream that followed the header; the handler must
// consume it fully (e.g. via ExtractTarGz) before returning.
type FrameHandler func(h FrameHeader, payload io.Reader) error

// ReceiveFrame reads one frame from rw, passes its payload to handle (not
// called for KindDone), then writes the ACK byte. It returns the header.
func ReceiveFrame(rw io.ReadWriter, handle FrameHandler) (FrameHeader, error) {
	h, err := ReadFrameHeader(rw)
	if err != nil {
		return h, err
	}
	if h.Kind != KindDone {
		if err := handle(h, rw); err != nil {
			return h, fmt.Errorf("handle frame %q: %w", h.Name, err)
		}
	}
	if _, err := rw.Write([]byte{ackByte}); err != nil {
		return h, fmt.Errorf("write ack: %w", err)
	}
	return h, nil
}

func readAck(r io.Reader) error {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("wait for ack: %w", err)
	}
	if buf[0] != ackByte {
		return fmt.Errorf("unexpected ack byte 0x%02X", buf[0])
	}
	return nil
}

// SendDir dials addr and transfers the directory dir as layer ordinal.
func SendDir(addr string, ordinal int, name, dir string) error {
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	return SendDirFrame(conn, ordinal, name, dir)
}

// SendCheckpointFile dials addr and transfers a single checkpoint file.
func SendCheckpointFile(addr, path string) error {
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	return SendFileFrame(conn, path)
}

// SendDone dials addr and signals the end of the checkpoint transfer.
func SendDone(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	return SendDoneFrame(conn)
}

// ReceiveAll accepts connections on ln, processing one frame per connection
// until a KindDone frame arrives or timeout elapses. Frames are routed via
// handle (see ReceiveFrame). It returns the headers of the received frames
// (excluding the DONE frame).
func ReceiveAll(ln net.Listener, timeout time.Duration, handle FrameHandler) ([]FrameHeader, error) {
	var received []FrameHeader
	deadline := time.Now().Add(timeout)
	for {
		if tl, ok := ln.(*net.TCPListener); ok {
			if err := tl.SetDeadline(deadline); err != nil {
				return received, fmt.Errorf("set accept deadline: %w", err)
			}
		}
		conn, err := ln.Accept()
		if err != nil {
			return received, fmt.Errorf("accept: %w", err)
		}
		h, err := ReceiveFrame(conn, handle)
		conn.Close()
		if err != nil {
			return received, err
		}
		if h.Kind == KindDone {
			return received, nil
		}
		received = append(received, h)
	}
}

// ExtractTarGz decompresses a gzip-compressed tar stream from r into destDir.
// Entry names are sanitised so the archive cannot escape destDir.
func ExtractTarGz(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()
	// The payload is a single gzip member; the same connection is then used
	// for the ACK exchange. Multistream mode would block probing the
	// connection for a second gzip member that never arrives.
	gr.Multistream(false)

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&os.ModePerm); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&os.ModePerm)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("write %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", target, closeErr)
			}
		}
	}
}

// safeJoin joins name under dir, rejecting absolute or escaping paths.
func safeJoin(dir, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("tar entry %q has absolute path", name)
	}
	cleaned := filepath.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar entry %q escapes destination", name)
	}
	return filepath.Join(dir, cleaned), nil
}

// tarDir writes the contents of dir (relative paths) to tw.
func tarDir(dir string, tw *tar.Writer) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
}
