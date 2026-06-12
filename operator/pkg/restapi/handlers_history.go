package restapi

import (
	"net/http"

	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/history"
)

// handleHistory implements GET /api/v1/history: the migration history plus
// aggregate metrics collected by the optional metrics module. When the module
// is disabled (or not wired) only {"enabled": false} is returned so the
// dashboard can render the off state.
func (s *Server) handleHistory(w http.ResponseWriter, _ *http.Request) {
	if s.History == nil || !s.History.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	records := s.History.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"summary":    history.Summarize(records),
		"migrations": records,
	})
}

// handleHistoryConfigGet implements GET /api/v1/history/config.
func (s *Server) handleHistoryConfigGet(w http.ResponseWriter, _ *http.Request) {
	if s.History == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "history module not configured"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.History.Enabled()})
}

// handleHistoryConfigSet implements POST /api/v1/history/config: enables or
// disables collection at runtime. Already-recorded history is kept.
func (s *Server) handleHistoryConfigSet(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "history module not configured"})
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled is required"})
		return
	}
	s.History.SetEnabled(*req.Enabled)
	s.Log.Info("history module toggled", "enabled", *req.Enabled)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}
