package dmtcp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- NewHandler ---

func TestNewHandler_Defaults(t *testing.T) {
	h := NewHandler("/tmp/checkpoints")

	if h.CoordHost != "127.0.0.1" {
		t.Errorf("expected CoordHost=127.0.0.1, got %q", h.CoordHost)
	}
	if h.CoordPort != 7779 {
		t.Errorf("expected CoordPort=7779, got %d", h.CoordPort)
	}
	if h.CheckpointDir != "/tmp/checkpoints" {
		t.Errorf("expected CheckpointDir=/tmp/checkpoints, got %q", h.CheckpointDir)
	}
	if h.State != StateIdle {
		t.Errorf("expected State=StateIdle, got %d", h.State)
	}
}

// --- coordAddr ---

func TestCoordAddr(t *testing.T) {
	cases := []struct {
		host     string
		port     int
		expected string
	}{
		{"127.0.0.1", 7779, "127.0.0.1:7779"},
		{"coord.api", 7779, "coord.api:7779"},
		{"10.0.0.1", 9000, "10.0.0.1:9000"},
	}

	for _, tc := range cases {
		h := &Handler{CoordHost: tc.host, CoordPort: tc.port}
		got := h.coordAddr()
		if got != tc.expected {
			t.Errorf("coordAddr() = %q, want %q", got, tc.expected)
		}
	}
}

// --- Checkpoint state guard ---

func TestCheckpoint_FailsWhenNotRunning(t *testing.T) {
	states := []CheckpointState{StateIdle, StateCheckpoint, StateCheckpointed, StateRestoring, StateError}

	for _, s := range states {
		h := &Handler{State: s, CoordHost: "127.0.0.1", CoordPort: 7779}
		err := h.Checkpoint()
		if err == nil {
			t.Errorf("Checkpoint() should fail when state=%d, but succeeded", s)
		}
	}
}

// --- Restart state guard ---

func TestRestart_FailsWhenNotCheckpointed(t *testing.T) {
	states := []CheckpointState{StateIdle, StateRunning, StateCheckpoint, StateRestoring, StateError}

	for _, s := range states {
		h := &Handler{State: s, CoordHost: "127.0.0.1", CoordPort: 7779}
		err := h.Restart()
		if err == nil {
			t.Errorf("Restart() should fail when state=%d, but succeeded", s)
		}
	}
}

// --- Launch state guard ---

func TestLaunch_FailsWhenNotIdle(t *testing.T) {
	states := []CheckpointState{StateRunning, StateCheckpoint, StateCheckpointed, StateRestoring, StateError}

	for _, s := range states {
		h := &Handler{State: s, CoordHost: "127.0.0.1", CoordPort: 7779}
		err := h.Launch("nginx -g 'daemon off;'")
		if err == nil {
			t.Errorf("Launch() should fail when state=%d, but succeeded", s)
		}
	}
}

func TestLaunch_FailsOnEmptyCommand(t *testing.T) {
	h := NewHandler("/tmp/checkpoints")
	err := h.Launch("")
	if err == nil {
		t.Error("Launch() should fail with empty command")
	}
}

// --- latestCheckpointFile ---

func TestLatestCheckpointFile_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{CheckpointDir: tmpDir}

	_, err := h.latestCheckpointFile()
	if err == nil {
		t.Error("latestCheckpointFile() should return error when directory is empty")
	}
}

func TestLatestCheckpointFile_SingleFile(t *testing.T) {
	tmpDir := t.TempDir()
	ckpt := filepath.Join(tmpDir, "ckpt_12345.dmtcp")
	if err := os.WriteFile(ckpt, []byte("dummy"), 0644); err != nil {
		t.Fatalf("failed to create checkpoint file: %v", err)
	}

	h := &Handler{CheckpointDir: tmpDir}
	got, err := h.latestCheckpointFile()
	if err != nil {
		t.Fatalf("latestCheckpointFile() failed: %v", err)
	}
	if got != ckpt {
		t.Errorf("expected %q, got %q", ckpt, got)
	}
}

func TestLatestCheckpointFile_ReturnsNewest(t *testing.T) {
	tmpDir := t.TempDir()

	older := filepath.Join(tmpDir, "ckpt_old.dmtcp")
	if err := os.WriteFile(older, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	// Ensure a distinct modification time
	time.Sleep(10 * time.Millisecond)

	newer := filepath.Join(tmpDir, "ckpt_new.dmtcp")
	if err := os.WriteFile(newer, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{CheckpointDir: tmpDir}
	got, err := h.latestCheckpointFile()
	if err != nil {
		t.Fatalf("latestCheckpointFile() failed: %v", err)
	}
	if got != newer {
		t.Errorf("expected newest file %q, got %q", newer, got)
	}
}

// --- ListCheckpoints ---

func TestListCheckpoints_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{CheckpointDir: tmpDir}

	files, err := h.ListCheckpoints()
	if err != nil {
		t.Fatalf("ListCheckpoints() unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 checkpoints, got %d", len(files))
	}
}

func TestListCheckpoints_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()

	expected := []string{"ckpt_a.dmtcp", "ckpt_b.dmtcp", "ckpt_c.dmtcp"}
	for _, name := range expected {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Non-checkpoint file should be ignored
	_ = os.WriteFile(filepath.Join(tmpDir, "other.txt"), []byte("x"), 0644)

	h := &Handler{CheckpointDir: tmpDir}
	files, err := h.ListCheckpoints()
	if err != nil {
		t.Fatalf("ListCheckpoints() unexpected error: %v", err)
	}
	if len(files) != len(expected) {
		t.Errorf("expected %d checkpoint files, got %d", len(expected), len(files))
	}
}

// --- WaitForCheckpointFile ---

func TestWaitForCheckpointFile_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{CheckpointDir: tmpDir}

	_, err := h.WaitForCheckpointFile(50 * time.Millisecond)
	if err == nil {
		t.Error("WaitForCheckpointFile() should return error on timeout when no files exist")
	}
}

func TestWaitForCheckpointFile_FileAlreadyPresent(t *testing.T) {
	tmpDir := t.TempDir()
	ckpt := filepath.Join(tmpDir, "ckpt_ready.dmtcp")
	if err := os.WriteFile(ckpt, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{CheckpointDir: tmpDir}
	got, err := h.WaitForCheckpointFile(500 * time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForCheckpointFile() unexpected error: %v", err)
	}
	if got != ckpt {
		t.Errorf("expected %q, got %q", ckpt, got)
	}
}

func TestWaitForCheckpointFile_FileAppearsLate(t *testing.T) {
	tmpDir := t.TempDir()
	ckpt := filepath.Join(tmpDir, "ckpt_late.dmtcp")

	h := &Handler{CheckpointDir: tmpDir}

	// Write the file after a short delay in a goroutine
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(ckpt, []byte("late"), 0644)
	}()

	got, err := h.WaitForCheckpointFile(2 * time.Second)
	if err != nil {
		t.Fatalf("WaitForCheckpointFile() unexpected error: %v", err)
	}
	if got != ckpt {
		t.Errorf("expected %q, got %q", ckpt, got)
	}
}

// --- CheckpointState constants ---

func TestCheckpointState_Values(t *testing.T) {
	states := map[string]CheckpointState{
		"StateIdle":         StateIdle,
		"StateRunning":      StateRunning,
		"StateCheckpoint":   StateCheckpoint,
		"StateCheckpointed": StateCheckpointed,
		"StateRestoring":    StateRestoring,
		"StateError":        StateError,
	}

	// All states must be unique
	seen := make(map[CheckpointState]string)
	for name, s := range states {
		if existing, ok := seen[s]; ok {
			t.Errorf("duplicate state value %d for %q and %q", s, name, existing)
		}
		seen[s] = name
	}

	if len(seen) != 6 {
		t.Errorf("expected 6 unique states, got %d", len(seen))
	}
}

// --- Handler field mutation ---

func TestHandler_CustomCoordinator(t *testing.T) {
	h := NewHandler("/checkpoints")
	h.CoordHost = "coord.api"
	h.CoordPort = 9000

	addr := h.coordAddr()
	expected := "coord.api:9000"
	if addr != expected {
		t.Errorf("expected addr=%q, got %q", expected, addr)
	}
}

func TestHandler_StateTransitionGuards(t *testing.T) {
	h := NewHandler("/tmp")

	// From Idle, Checkpoint should fail
	if err := h.Checkpoint(); err == nil {
		t.Error("expected error from Checkpoint() in Idle state")
	}

	// Verify state remains Idle after failed Checkpoint
	if h.State != StateIdle {
		t.Errorf("state should remain Idle after failed Checkpoint, got %d", h.State)
	}
}

// --- Restart with missing checkpoint file ---

func TestRestart_FailsWhenNoCheckpointFile(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{
		CheckpointDir: tmpDir,
		State:         StateCheckpointed,
		CoordHost:     "127.0.0.1",
		CoordPort:     7779,
	}

	err := h.Restart()
	if err == nil {
		t.Error("Restart() should fail when no .dmtcp file exists in CheckpointDir")
	}

	expectedErr := fmt.Sprintf("no checkpoint files found in %s", tmpDir)
	if err.Error() == "" {
		t.Error("expected a non-empty error message")
	}
	_ = expectedErr
}
