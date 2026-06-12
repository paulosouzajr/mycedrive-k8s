package overlay

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-agent/utils"
)

// fakeRunner records mount/umount commands instead of executing them.
type fakeRunner struct {
	calls []string
	fail  map[string]error // command prefix -> error to return
}

func (f *fakeRunner) run(name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	for prefix, err := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return err
		}
	}
	return nil
}

func newTestManager(t *testing.T) (*LayerManager, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{}
	lm := NewLayerManager(t.TempDir(), "/mnt/approot")
	lm.Run = fr.run
	return lm, fr
}

// --- InitVolume ---

func TestInitVolume_FreshStart(t *testing.T) {
	lm, fr := newTestManager(t)
	if err := lm.InitVolume(); err != nil {
		t.Fatalf("InitVolume: %v", err)
	}
	if lm.Level() != 1 {
		t.Errorf("level = %d, want 1", lm.Level())
	}
	for _, d := range []string{"u1", "w1", "o1"} {
		if _, err := os.Stat(filepath.Join(lm.DataDir, d)); err != nil {
			t.Errorf("missing dir %s: %v", d, err)
		}
	}
	if len(fr.calls) != 2 {
		t.Fatalf("expected 2 commands, got %v", fr.calls)
	}
	// With no received lower layers, the original volume content is lowerdir.
	if !strings.Contains(fr.calls[0], "lowerdir=/mnt/approot") {
		t.Errorf("overlay mount should use RootDir as lowerdir: %s", fr.calls[0])
	}
	if !strings.HasPrefix(fr.calls[1], "mount --bind") || !strings.Contains(fr.calls[1], "/mnt/approot") {
		t.Errorf("expected bind mount over RootDir: %s", fr.calls[1])
	}
}

func TestInitVolume_WithReceivedLayers_NumericOrder(t *testing.T) {
	lm, fr := newTestManager(t)
	// Create received lower layers 1, 2, 10, 12: lexicographic sort would
	// produce l1,l10,l12,l2; numeric must be l1,l2,l10,l12 (newest = l12).
	for _, n := range []int{1, 2, 10, 12} {
		if err := os.MkdirAll(lm.LayerDir(n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := lm.InitVolume(); err != nil {
		t.Fatalf("InitVolume: %v", err)
	}
	want := fmt.Sprintf("lowerdir=%s:%s:%s:%s",
		lm.LayerDir(12), lm.LayerDir(10), lm.LayerDir(2), lm.LayerDir(1))
	if !strings.Contains(fr.calls[0], want) {
		t.Errorf("lowerdir not in newest-first numeric order:\n got: %s\nwant substring: %s", fr.calls[0], want)
	}
}

func TestInitVolume_PropagatesMountError(t *testing.T) {
	lm, fr := newTestManager(t)
	fr.fail = map[string]error{"mount -t overlay": fmt.Errorf("boom")}
	if err := lm.InitVolume(); err == nil {
		t.Error("expected mount error to propagate")
	}
	if lm.Level() != 0 {
		t.Errorf("level should stay 0 after failed init, got %d", lm.Level())
	}
}

// --- CreateCheckpoint ---

func TestCreateCheckpoint_FreezesAndRemounts(t *testing.T) {
	lm, fr := newTestManager(t)
	if err := lm.InitVolume(); err != nil {
		t.Fatal(err)
	}
	fr.calls = nil

	frozen, err := lm.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if frozen != 1 {
		t.Errorf("frozen ordinal = %d, want 1", frozen)
	}
	if lm.Level() != 2 {
		t.Errorf("level = %d, want 2", lm.Level())
	}
	if len(fr.calls) != 3 {
		t.Fatalf("expected 3 commands (mount overlay, umount, bind), got %v", fr.calls)
	}
	// New overlay stacks on the previous merged view, fresh upperdir u2.
	if !strings.Contains(fr.calls[0], "lowerdir="+filepath.Join(lm.DataDir, "o1")) ||
		!strings.Contains(fr.calls[0], "upperdir="+filepath.Join(lm.DataDir, "u2")) {
		t.Errorf("unexpected remount command: %s", fr.calls[0])
	}
	if !strings.HasPrefix(fr.calls[1], "umount -l /mnt/approot") {
		t.Errorf("expected lazy unbind of RootDir: %s", fr.calls[1])
	}
	if !strings.Contains(fr.calls[2], filepath.Join(lm.DataDir, "o2")) {
		t.Errorf("expected bind of new merged dir: %s", fr.calls[2])
	}
}

func TestCreateCheckpoint_RequiresInit(t *testing.T) {
	lm, _ := newTestManager(t)
	if _, err := lm.CreateCheckpoint(); err == nil {
		t.Error("CreateCheckpoint before InitVolume must fail")
	}
}

// --- Discover ---

func TestDiscover_FindsCurrentLevel(t *testing.T) {
	lm, _ := newTestManager(t)
	for _, n := range []int{1, 2, 10} { // u10 must win over u2 numerically
		if err := os.MkdirAll(filepath.Join(lm.DataDir, fmt.Sprintf("u%d", n)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := lm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if lm.Level() != 10 {
		t.Errorf("discovered level = %d, want 10", lm.Level())
	}
}

func TestDiscover_EmptyDataDir(t *testing.T) {
	lm, _ := newTestManager(t)
	if err := lm.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if lm.Level() != 0 {
		t.Errorf("level = %d, want 0", lm.Level())
	}
}

// --- CopyCheckpoint / ReceiveCheckpoint over real TCP ---

func TestCopyAndReceiveCheckpoint_EndToEnd(t *testing.T) {
	src, fr := newTestManager(t)
	_ = fr
	if err := src.InitVolume(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.DataDir, "u1", "state.db"), []byte("layer-1-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateCheckpoint(); err != nil {
		t.Fatal(err)
	}

	dst, _ := newTestManager(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		_, err := utils.ReceiveAll(ln, 5*time.Second, func(h utils.FrameHeader, payload io.Reader) error {
			return dst.ReceiveCheckpoint(h.Ordinal, payload)
		})
		done <- err
	}()

	if err := src.CopyCheckpoint(ln.Addr().String()); err != nil {
		t.Fatalf("CopyCheckpoint: %v", err)
	}
	if err := utils.SendDone(ln.Addr().String()); err != nil {
		t.Fatalf("SendDone: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("receive side: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst.LayerDir(1), "state.db"))
	if err != nil {
		t.Fatalf("received layer missing: %v", err)
	}
	if string(got) != "layer-1-data" {
		t.Errorf("content mismatch: %q", got)
	}

	// A second CopyCheckpoint must not resend the already-sent layer.
	if err := src.CopyCheckpoint("127.0.0.1:1"); err != nil { // unreachable addr
		t.Errorf("CopyCheckpoint with nothing to send should succeed, got %v", err)
	}
}

// --- EndVolume ---

func TestEndVolume_UnmountsAndSendsFinalLayer(t *testing.T) {
	lm, fr := newTestManager(t)
	if err := lm.InitVolume(); err != nil {
		t.Fatal(err)
	}
	if _, err := lm.CreateCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lm.DataDir, "u2", "hot.txt"), []byte("final"), 0o644); err != nil {
		t.Fatal(err)
	}

	dstDir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		_, err := utils.ReceiveAll(ln, 5*time.Second, func(h utils.FrameHeader, payload io.Reader) error {
			return utils.ExtractTarGz(payload, filepath.Join(dstDir, h.Name))
		})
		done <- err
	}()

	fr.calls = nil
	if err := lm.EndVolume(ln.Addr().String()); err != nil {
		t.Fatalf("EndVolume: %v", err)
	}
	if err := utils.SendDone(ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("receive side: %v", err)
	}

	if lm.Level() != 0 {
		t.Errorf("level after EndVolume = %d, want 0", lm.Level())
	}
	// First the RootDir unbind, then merged dirs newest-first.
	if !strings.HasPrefix(fr.calls[0], "umount -l /mnt/approot") {
		t.Errorf("first command should unbind RootDir: %v", fr.calls)
	}
	if !strings.Contains(fr.calls[1], "o2") || !strings.Contains(fr.calls[2], "o1") {
		t.Errorf("merged dirs should unmount newest-first: %v", fr.calls)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "u2", "hot.txt"))
	if err != nil || string(got) != "final" {
		t.Errorf("final layer not transferred correctly: %q, %v", got, err)
	}
}

func TestEndVolume_NoTransferWithoutDest(t *testing.T) {
	lm, _ := newTestManager(t)
	if err := lm.InitVolume(); err != nil {
		t.Fatal(err)
	}
	if err := lm.EndVolume(""); err != nil {
		t.Fatalf("EndVolume without dest: %v", err)
	}
	if lm.Level() != 0 {
		t.Errorf("level = %d, want 0", lm.Level())
	}
}

// --- numeric ordering helper ---

func TestNumberedDirs_NumericSort(t *testing.T) {
	lm, _ := newTestManager(t)
	for _, n := range []int{12, 1, 110, 2, 10} {
		if err := os.MkdirAll(filepath.Join(lm.DataDir, fmt.Sprintf("o%d", n)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lm.numberedDirs("o")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 10, 12, 110}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("numeric ordering broken: got %v, want %v", got, want)
		}
	}
}
