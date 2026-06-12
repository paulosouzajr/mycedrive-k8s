package restapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/history"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
)

// newHistoryServer returns a test server with the metrics module wired.
func newHistoryServer(enabled bool) (*Server, *http.ServeMux) {
	s := &Server{
		Registry:         registry.New(),
		History:          history.NewStore(enabled, 10),
		DefaultNamespace: "mig-ready",
		Log:              logr.Discard(),
	}
	mux := http.NewServeMux()
	s.routes(mux)
	return s, mux
}

// TestHistoryEndpoint checks GET /api/v1/history with recorded transitions.
func TestHistoryEndpoint(t *testing.T) {
	s, mux := newHistoryServer(true)

	base := time.Now().Add(-30 * time.Second)
	tr := history.Transition{
		Namespace: "mig-ready", Name: "web-abc12", Workload: "web",
		SourceNode: "node-1", TargetNode: "node-2", Phase: "Pending", Time: base,
	}
	s.History.RecordTransition(tr)
	tr.Phase, tr.Time = "Checkpointing", base.Add(5*time.Second)
	s.History.RecordTransition(tr)
	tr.Phase, tr.Time = "Completed", base.Add(12*time.Second)
	s.History.RecordTransition(tr)

	rr, resp := doJSON(t, mux, http.MethodGet, "/api/v1/history", nil)
	if rr.Code != http.StatusOK || resp["enabled"] != true {
		t.Fatalf("history = %d %v, want enabled=true", rr.Code, resp)
	}
	migrations, ok := resp["migrations"].([]any)
	if !ok || len(migrations) != 1 {
		t.Fatalf("migrations = %v, want 1 record", resp["migrations"])
	}
	rec := migrations[0].(map[string]any)
	if rec["phase"] != "Completed" || rec["totalMs"] != float64(12000) || rec["downtimeMs"] != float64(7000) {
		t.Fatalf("record wrong: %v", rec)
	}
	summary := resp["summary"].(map[string]any)
	if summary["completed"] != float64(1) || summary["successRate"] != float64(1) {
		t.Fatalf("summary wrong: %v", summary)
	}
}

// TestHistoryToggle checks the runtime enable/disable round trip.
func TestHistoryToggle(t *testing.T) {
	s, mux := newHistoryServer(false)

	// Disabled: history reports enabled=false only.
	rr, resp := doJSON(t, mux, http.MethodGet, "/api/v1/history", nil)
	if rr.Code != http.StatusOK || resp["enabled"] != false {
		t.Fatalf("disabled history = %d %v", rr.Code, resp)
	}
	if _, ok := resp["migrations"]; ok {
		t.Fatalf("disabled history must not return records: %v", resp)
	}

	// Enable via the API.
	rr, resp = doJSON(t, mux, http.MethodPost, "/api/v1/history/config", map[string]any{"enabled": true})
	if rr.Code != http.StatusOK || resp["enabled"] != true {
		t.Fatalf("enable = %d %v", rr.Code, resp)
	}
	if !s.History.Enabled() {
		t.Fatalf("store must be enabled after POST")
	}
	rr, resp = doJSON(t, mux, http.MethodGet, "/api/v1/history/config", nil)
	if rr.Code != http.StatusOK || resp["enabled"] != true {
		t.Fatalf("config get = %d %v", rr.Code, resp)
	}

	// Missing body field → 400.
	rr, _ = doJSON(t, mux, http.MethodPost, "/api/v1/history/config", map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("config without enabled = %d, want 400", rr.Code)
	}
}

// TestHistoryNotConfigured checks the nil-store guard.
func TestHistoryNotConfigured(t *testing.T) {
	_, mux := newTestServer()

	rr, resp := doJSON(t, mux, http.MethodGet, "/api/v1/history", nil)
	if rr.Code != http.StatusOK || resp["enabled"] != false {
		t.Fatalf("history without module = %d %v, want enabled=false", rr.Code, resp)
	}
	rr, _ = doJSON(t, mux, http.MethodPost, "/api/v1/history/config", map[string]any{"enabled": true})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("config without module = %d, want 503", rr.Code)
	}
}
