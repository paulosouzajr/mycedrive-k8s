package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Run executes command with the given space-separated args string and returns
// its stdout. A non-zero exit status is returned as an error that includes
// the command's stderr.
func Run(command string, args string) ([]byte, error) {
	cmd := exec.Command(command, strings.Fields(args)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w (stderr: %s)", command, args, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// EnvBool reads a boolean environment variable. Accepted true values:
// "1", "t", "true", "yes", "y", "on"; false values: "0", "f", "false",
// "no", "n", "off" (case-insensitive). Unset or unrecognised values return def.
func EnvBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

// EnvInt reads an integer environment variable, returning def when unset or
// invalid. Values outside (0, 65535] are rejected for port-like settings;
// callers needing other ranges should validate separately.
func EnvInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 || v > 65535 {
		return def
	}
	return v
}

// EnvOr returns the value of the environment variable name, or def when unset.
func EnvOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// ProcessMigrationEnabled reports whether DMTCP process checkpoint/restore is
// enabled (ENABLE_PROCESS_MIGRATION, default true).
func ProcessMigrationEnabled() bool {
	return EnvBool("ENABLE_PROCESS_MIGRATION", true)
}

// VolumeMigrationEnabled reports whether overlay volume layer checkpointing is
// enabled (ENABLE_VOLUME_MIGRATION, default true).
func VolumeMigrationEnabled() bool {
	return EnvBool("ENABLE_VOLUME_MIGRATION", true)
}

// --- Migration Coordinator wire types (REST contract) ---

// RemoveRequest is the payload sent to POST /remove.
type RemoveRequest struct {
	PodName string `json:"podName"`
}

// RemoveResponse is the response from POST /remove. DestAddress is an
// additive field: when set it carries the host:port of the migration-target
// Execution Agent so the source can stream checkpoints directly.
type RemoveResponse struct {
	NeedsCheckpoint bool   `json:"needsCheckpoint"`
	DestAddress     string `json:"destAddress,omitempty"`
}

// CopyNotification is the payload sent to POST /copy. LayerCount is additive.
type CopyNotification struct {
	PodName       string `json:"podName"`
	CheckpointDir string `json:"checkpointDir"`
	LayerCount    int    `json:"layerCount,omitempty"`
}

// PostJSON marshals payload to JSON, POSTs it to url, and returns the
// response body.
func PostJSON(url string, payload interface{}) ([]byte, error) {
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
