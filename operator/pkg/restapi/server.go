// Package restapi exposes the Migration Coordinator REST API from inside the
// operator. It keeps the legacy Execution Agent contract (/register /remove
// /copy /migrate) byte-compatible, adds the additive endpoints used by the
// fixed agent (/sync /restored /poll) and serves the dashboard plus the
// JSON endpoints the UI consumes (/pods, /api/v1/pods, /api/v1/migrations).
package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/paulosouzajr/mycedrive-k8s/operator/dashboard"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/history"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
)

// Server is the operator's embedded Migration Coordinator REST API. It runs
// as a manager Runnable on every replica (no leader election) so agents can
// always reach it.
type Server struct {
	Client           client.Client
	Registry         *registry.Registry
	Addr             string
	DefaultNamespace string
	Log              logr.Logger
	// History is the optional migration-metrics module; nil when not wired.
	History *history.Store
}

// Handler returns the fully-routed HTTP handler. Exported so functional
// tests and embedders can serve the API without binding a socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return mux
}

// Start implements manager.Runnable: it serves until the context is done.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.Log.Info("REST API listening", "addr", s.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// NeedLeaderElection keeps the REST API up on non-leader replicas.
func (s *Server) NeedLeaderElection() bool { return false }

func (s *Server) routes(mux *http.ServeMux) {
	// Health.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Legacy Execution Agent contract (go-server compatible).
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("POST /remove", s.handleRemove)
	mux.HandleFunc("POST /copy", s.handleCopy)
	mux.HandleFunc("POST /migrate", s.handleMigrate)

	// Additive agent endpoints (fixed agent flow).
	mux.HandleFunc("POST /sync", s.handleSync)
	mux.HandleFunc("POST /restored", s.handleRestored)
	mux.HandleFunc("GET /poll", s.handlePoll)

	// Dashboard / UI JSON.
	mux.HandleFunc("GET /pods", s.handleLegacyPods)
	mux.HandleFunc("GET /api/v1/pods", s.handleAPIPods)
	mux.HandleFunc("GET /api/v1/migrations", s.handleAPIMigrations)
	mux.HandleFunc("POST /api/v1/migrations", s.handleMigrate)

	// Migration history & metrics (optional module).
	mux.HandleFunc("GET /api/v1/history", s.handleHistory)
	mux.HandleFunc("GET /api/v1/history/config", s.handleHistoryConfigGet)
	mux.HandleFunc("POST /api/v1/history/config", s.handleHistoryConfigSet)

	// Embedded static dashboard.
	mux.Handle("GET /dashboard/", http.StripPrefix("/dashboard/", http.FileServerFS(dashboard.FS)))
}

// writeJSON serialises v with the given HTTP status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON parses the request body (limited to 1 MiB) into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}
