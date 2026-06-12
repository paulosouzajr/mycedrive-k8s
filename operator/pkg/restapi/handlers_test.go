package restapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"

	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
)

// newTestServer returns a Server wired to a fresh registry and a test mux.
// The Kubernetes client is nil: routes touching it are not exercised here.
func newTestServer() (*Server, *http.ServeMux) {
	s := &Server{
		Registry:         registry.New(),
		DefaultNamespace: "mig-ready",
		Log:              logr.Discard(),
	}
	mux := http.NewServeMux()
	s.routes(mux)
	return s, mux
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var decoded map[string]any
	if rr.Body.Len() > 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode response %q: %v", rr.Body.String(), err)
		}
	}
	return rr, decoded
}

// TestRegisterCopyHandshake walks the full agent handshake of a StatefulSet
// migration: fresh register → arm → remove → copy → destination re-register
// → restored.
func TestRegisterCopyHandshake(t *testing.T) {
	s, mux := newTestServer()

	// 1. Fresh registration: 201, isNew=true, isMig=false.
	rr, resp := doJSON(t, mux, http.MethodPost, "/register", map[string]any{
		"podName":       "web-0",
		"podAddress":    "10.0.0.5:2486",
		"containerPort": 2486,
		"isNew":         true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("fresh register code = %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	if resp["isNew"] != true || resp["isMig"] != false {
		t.Fatalf("fresh register flags wrong: %v", resp)
	}

	// 2. Controller arms the migration (process+volume, 1 sync round).
	s.Registry.Arm("web-0", registry.ArmInfo{
		CheckpointDir:    "/dmtcp/checkpoints",
		ProcessMigration: true,
		VolumeMigration:  true,
		SyncRounds:       1,
	})

	// 3. Source EA polls and sees the armed migration.
	rr, resp = doJSON(t, mux, http.MethodGet, "/poll?podName=web-0", nil)
	if rr.Code != http.StatusOK || resp["migrating"] != true {
		t.Fatalf("poll = %d %v, want armed migration", rr.Code, resp)
	}
	if resp["processMigration"] != true || resp["volumeMigration"] != true {
		t.Fatalf("poll must carry mechanism toggles: %v", resp)
	}

	// 4. Source EA reports the pre-downtime sync round.
	rr, resp = doJSON(t, mux, http.MethodPost, "/sync", map[string]any{"podName": "web-0", "round": 1})
	if rr.Code != http.StatusOK || resp["remaining"] != float64(0) {
		t.Fatalf("sync = %d %v, want remaining 0", rr.Code, resp)
	}

	// 5. preStop: /remove answers needsCheckpoint with the toggles.
	rr, resp = doJSON(t, mux, http.MethodPost, "/remove", map[string]any{"podName": "web-0"})
	if rr.Code != http.StatusOK || resp["needsCheckpoint"] != true {
		t.Fatalf("remove = %d %v, want needsCheckpoint=true", rr.Code, resp)
	}
	if resp["processMigration"] != true || resp["volumeMigration"] != true {
		t.Fatalf("remove must carry mechanism toggles: %v", resp)
	}
	if _, ok := resp["destAddress"]; ok {
		t.Fatalf("destAddress must be omitted before the migration target registers: %v", resp)
	}

	// 6. Source EA finished writing checkpoint files.
	rr, resp = doJSON(t, mux, http.MethodPost, "/copy", map[string]any{
		"podName":       "web-0",
		"checkpointDir": "/dmtcp/checkpoints",
	})
	if rr.Code != http.StatusOK || resp["status"] != "copy_initiated" {
		t.Fatalf("copy = %d %v", rr.Code, resp)
	}
	if rec, _ := s.Registry.Get("web-0"); !rec.CheckpointReady {
		t.Fatalf("copy must mark the checkpoint ready")
	}

	// 7. Destination EA re-registers under the same StatefulSet pod name:
	// 200, isMig=true, carries checkpointDir.
	rr, resp = doJSON(t, mux, http.MethodPost, "/register", map[string]any{
		"podName":       "web-0",
		"podAddress":    "10.0.1.7:2486",
		"containerPort": 2486,
		"isNew":         true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("dest register code = %d, want 200", rr.Code)
	}
	if resp["isNew"] != false || resp["isMig"] != true {
		t.Fatalf("dest register flags wrong: %v", resp)
	}
	if resp["checkpointDir"] != "/dmtcp/checkpoints" {
		t.Fatalf("dest register must carry checkpointDir: %v", resp)
	}
	if resp["podAddress"] != "10.0.0.5:2486" {
		t.Fatalf("dest register must return the previous (source) address: %v", resp)
	}
	if rec, _ := s.Registry.Get("web-0"); !rec.DestRegistered {
		t.Fatalf("dest registration must set DestRegistered")
	}

	// 7b. With the migration target registered, /remove now hands the source
	// EA the destination's transfer endpoint for the direct stream.
	rr, resp = doJSON(t, mux, http.MethodPost, "/remove", map[string]any{"podName": "web-0"})
	if rr.Code != http.StatusOK || resp["needsCheckpoint"] != true {
		t.Fatalf("remove after dest register = %d %v, want needsCheckpoint=true", rr.Code, resp)
	}
	if resp["destAddress"] != "10.0.1.7:2486" {
		t.Fatalf("remove must carry the migration target's destAddress: %v", resp)
	}

	// 8. Destination EA signals restore completion.
	rr, resp = doJSON(t, mux, http.MethodPost, "/restored", map[string]any{"podName": "web-0"})
	if rr.Code != http.StatusOK || resp["status"] != "restored" {
		t.Fatalf("restored = %d %v", rr.Code, resp)
	}
	if rec, _ := s.Registry.Get("web-0"); !rec.Restored {
		t.Fatalf("restored must mark the record")
	}
}

// TestRemoveDestAddressDefaults covers the destAddress port-defaulting rules
// and its omission when no migration is armed.
func TestRemoveDestAddressDefaults(t *testing.T) {
	s, mux := newTestServer()

	// Unarmed duplicate registration must NOT yield a destAddress.
	s.Registry.Register("db-0", "10.0.0.2:2486", 2486)
	doJSON(t, mux, http.MethodPost, "/register", map[string]any{
		"podName": "db-0", "podAddress": "10.0.0.3:2486", "containerPort": 2486,
	})
	rr, resp := doJSON(t, mux, http.MethodPost, "/remove", map[string]any{"podName": "db-0"})
	if rr.Code != http.StatusOK || resp["needsCheckpoint"] != false {
		t.Fatalf("unarmed remove = %d %v", rr.Code, resp)
	}
	if _, ok := resp["destAddress"]; ok {
		t.Fatalf("destAddress must be omitted when no migration is armed: %v", resp)
	}

	// Bare host + explicit containerPort → host:containerPort.
	s.Registry.Register("web-1", "10.0.0.5:2486", 2486)
	s.Registry.Arm("web-1", registry.ArmInfo{ProcessMigration: true})
	doJSON(t, mux, http.MethodPost, "/register", map[string]any{
		"podName": "web-1", "podAddress": "10.0.1.8", "containerPort": 2400,
	})
	_, resp = doJSON(t, mux, http.MethodPost, "/remove", map[string]any{"podName": "web-1"})
	if resp["destAddress"] != "10.0.1.8:2400" {
		t.Fatalf("destAddress = %v, want 10.0.1.8:2400", resp["destAddress"])
	}

	// Bare host without containerPort → default transfer port 2486.
	s.Registry.Register("web-2", "10.0.0.6:2486", 2486)
	s.Registry.Arm("web-2", registry.ArmInfo{ProcessMigration: true})
	doJSON(t, mux, http.MethodPost, "/register", map[string]any{
		"podName": "web-2", "podAddress": "10.0.1.9",
	})
	_, resp = doJSON(t, mux, http.MethodPost, "/remove", map[string]any{"podName": "web-2"})
	if resp["destAddress"] != "10.0.1.9:2486" {
		t.Fatalf("destAddress = %v, want 10.0.1.9:2486", resp["destAddress"])
	}
}

func TestRemoveUnknownPod(t *testing.T) {
	_, mux := newTestServer()
	rr, _ := doJSON(t, mux, http.MethodPost, "/remove", map[string]any{"podName": "ghost"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("remove unknown pod = %d, want 404", rr.Code)
	}
}

func TestLegacyPodsShape(t *testing.T) {
	s, mux := newTestServer()
	s.Registry.Register("web-0", "10.0.0.5:2486", 2486)
	s.Registry.Arm("web-0", registry.ArmInfo{})

	req := httptest.NewRequest(http.MethodGet, "/pods", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/pods = %d, want 200", rr.Code)
	}
	var pods []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &pods); err != nil {
		t.Fatalf("/pods must return a JSON array: %v", err)
	}
	if len(pods) != 1 || pods[0]["podName"] != "web-0" || pods[0]["migrating"] != true {
		t.Fatalf("legacy /pods shape wrong: %v", pods)
	}
	if _, ok := pods[0]["podAddress"]; !ok {
		t.Fatalf("legacy /pods must keep the podAddress key")
	}
}

func TestRegisterValidation(t *testing.T) {
	_, mux := newTestServer()
	rr, _ := doJSON(t, mux, http.MethodPost, "/register", map[string]any{"podAddress": "x"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("register without podName = %d, want 400", rr.Code)
	}
}
