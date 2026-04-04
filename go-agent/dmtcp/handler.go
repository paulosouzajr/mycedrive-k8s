package dmtcp

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckpointState represents the current status of a DMTCP checkpoint operation.
type CheckpointState int

const (
	StateIdle         CheckpointState = iota // No active checkpoint
	StateRunning                             // Application is running under DMTCP
	StateCheckpoint                          // Checkpoint in progress
	StateCheckpointed                        // Checkpoint completed successfully
	StateRestoring                           // Restoring from checkpoint
	StateError                               // Error occurred
)

// Handler manages DMTCP coordinator lifecycle and checkpoint operations.
type Handler struct {
	CoordHost      string          // Hostname of the DMTCP coordinator (default: 127.0.0.1)
	CoordPort      int             // Port of the DMTCP coordinator (default: 7779)
	CheckpointDir  string          // Directory to store checkpoint files
	State          CheckpointState // Current state of the handler
	coordinatorCmd *exec.Cmd       // Running coordinator process
}

// NewHandler creates a Handler with sensible defaults.
func NewHandler(checkpointDir string) *Handler {
	return &Handler{
		CoordHost:     "127.0.0.1",
		CoordPort:     7779,
		CheckpointDir: checkpointDir,
		State:         StateIdle,
	}
}

// coordAddr returns the coordinator address string.
func (h *Handler) coordAddr() string {
	return fmt.Sprintf("%s:%d", h.CoordHost, h.CoordPort)
}

// Checkpoint sends a checkpoint command to the DMTCP coordinator and waits
// for completion. It sets State to StateCheckpoint during the operation and
// StateCheckpointed on success, or StateError on failure.
func (h *Handler) Checkpoint() error {
	if h.State != StateRunning {
		return fmt.Errorf("cannot checkpoint: handler is in state %d (expected StateRunning)", h.State)
	}

	log.Printf("[dmtcp] Requesting checkpoint via coordinator at %s", h.coordAddr())
	h.State = StateCheckpoint

	cmd := exec.Command("dmtcp_command",
		"--coord-host", h.CoordHost,
		"--coord-port", fmt.Sprintf("%d", h.CoordPort),
		"-bc",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		h.State = StateError
		return fmt.Errorf("dmtcp_command checkpoint failed: %w", err)
	}

	h.State = StateCheckpointed
	log.Printf("[dmtcp] Checkpoint completed, files in %s", h.CheckpointDir)
	return nil
}

// Restart launches the application from the latest checkpoint in CheckpointDir.
// It sets State to StateRestoring during the operation and StateRunning on success.
func (h *Handler) Restart() error {
	if h.State != StateCheckpointed {
		return fmt.Errorf("cannot restart: handler is in state %d (expected StateCheckpointed)", h.State)
	}

	ckptFile, err := h.latestCheckpointFile()
	if err != nil {
		h.State = StateError
		return fmt.Errorf("failed to find checkpoint file: %w", err)
	}

	log.Printf("[dmtcp] Restarting from checkpoint: %s", ckptFile)
	h.State = StateRestoring

	cmd := exec.Command("dmtcp_restart",
		"--coord-host", h.CoordHost,
		"--coord-port", fmt.Sprintf("%d", h.CoordPort),
		ckptFile,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		h.State = StateError
		return fmt.Errorf("dmtcp_restart failed: %w", err)
	}

	h.State = StateRunning
	return nil
}

// Launch wraps a command with dmtcp_launch so it is managed by the coordinator.
// It sets State to StateRunning on success.
func (h *Handler) Launch(applicationCmd string) error {
	if h.State != StateIdle {
		return fmt.Errorf("cannot launch: handler is in state %d (expected StateIdle)", h.State)
	}

	parts := strings.Fields(applicationCmd)
	if len(parts) == 0 {
		return fmt.Errorf("application command is empty")
	}

	args := []string{
		"--coord-host", h.CoordHost,
		"--coord-port", fmt.Sprintf("%d", h.CoordPort),
	}
	args = append(args, parts...)

	log.Printf("[dmtcp] Launching under DMTCP: dmtcp_launch %s", strings.Join(args, " "))

	cmd := exec.Command("dmtcp_launch", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		h.State = StateError
		return fmt.Errorf("dmtcp_launch failed: %w", err)
	}

	h.State = StateRunning
	return nil
}

// WaitForCheckpointFile polls CheckpointDir until a .dmtcp checkpoint file
// appears or the timeout is exceeded. Returns the path to the file or an error.
func (h *Handler) WaitForCheckpointFile(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		path, err := h.latestCheckpointFile()
		if err == nil {
			return path, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for checkpoint file in %s", h.CheckpointDir)
}

// latestCheckpointFile returns the most recently modified .dmtcp file in CheckpointDir.
func (h *Handler) latestCheckpointFile() (string, error) {
	pattern := filepath.Join(h.CheckpointDir, "*.dmtcp")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no checkpoint files found in %s", h.CheckpointDir)
	}

	var latest string
	var latestMod time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
			latest = m
		}
	}

	if latest == "" {
		return "", fmt.Errorf("no readable checkpoint files in %s", h.CheckpointDir)
	}
	return latest, nil
}

// ListCheckpoints returns all checkpoint files in CheckpointDir.
func (h *Handler) ListCheckpoints() ([]string, error) {
	pattern := filepath.Join(h.CheckpointDir, "*.dmtcp")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list checkpoints: %w", err)
	}
	return matches, nil
}
