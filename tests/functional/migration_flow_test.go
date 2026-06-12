// Package functional verifies the real wire contract between the Execution
// Agent helpers (go-agent) and the operator's Migration Coordinator REST API:
// the control plane over actual HTTP and the checkpoint data plane over
// actual TCP, with no Kubernetes cluster or DMTCP runtime required.
package functional

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"

	agent "go-agent/utils"

	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/restapi"
)

// newAPI starts the operator REST API on a loopback HTTP server.
func newAPI(t *testing.T) (*registry.Registry, string) {
	t.Helper()
	reg := registry.New()
	srv := &restapi.Server{Registry: reg, DefaultNamespace: "mig-ready", Log: logr.Discard()}
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	return reg, api.URL
}

func postJSON(t *testing.T, url string, payload any) map[string]any {
	t.Helper()
	body, err := agent.PostJSON(url, payload)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s response %q: %v", url, body, err)
	}
	return decoded
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d (%s)", url, resp.StatusCode, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s response %q: %v", url, body, err)
	}
	return decoded
}

// TestMigrationFlow_EndToEnd drives one complete migration with the real
// agent client code against the real operator API: source register → arm →
// poll → sync → destination register → remove (returns destAddress) →
// stream overlay layers + DMTCP checkpoint over TCP → copy → restored.
func TestMigrationFlow_EndToEnd(t *testing.T) {
	reg, apiURL := newAPI(t)

	// 1. Source EA registers (same payload shape go-agent/main.go sends).
	resp := postJSON(t, apiURL+"/register", map[string]any{
		"podName": "web-0", "podAddress": "10.0.0.5:2486", "containerPort": 2486, "isNew": true,
	})
	if resp["isNew"] != true || resp["isMig"] != false {
		t.Fatalf("fresh register: %v", resp)
	}

	// 2. The Migration controller arms the workload (both mechanisms on).
	reg.Arm("web-0", registry.ArmInfo{
		CheckpointDir:    "/dmtcp/checkpoints",
		ProcessMigration: true,
		VolumeMigration:  true,
		SyncRounds:       1,
	})

	// 3. Source EA polls, sees the armed migration, reports its sync round.
	poll := getJSON(t, apiURL+"/poll?podName=web-0")
	if poll["migrating"] != true || poll["syncRounds"] != float64(1) {
		t.Fatalf("poll: %v", poll)
	}
	sync := postJSON(t, apiURL+"/sync", map[string]any{"podName": "web-0", "round": 1})
	if sync["remaining"] != float64(0) {
		t.Fatalf("sync: %v", sync)
	}

	// 4. Destination EA starts its transfer listener and re-registers under
	// the same pod name, advertising the listener as its podAddress.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	destLayers := t.TempDir()
	destCkpt := t.TempDir()
	type recvResult struct {
		frames []agent.FrameHeader
		err    error
	}
	recvCh := make(chan recvResult, 1)
	go func() {
		frames, err := agent.ReceiveAll(ln, 10*time.Second, func(h agent.FrameHeader, payload io.Reader) error {
			if h.Kind == agent.KindCheckpointFile {
				return agent.ExtractTarGz(payload, destCkpt)
			}
			return agent.ExtractTarGz(payload, filepath.Join(destLayers, h.Name))
		})
		recvCh <- recvResult{frames, err}
	}()

	resp = postJSON(t, apiURL+"/register", map[string]any{
		"podName": "web-0", "podAddress": ln.Addr().String(), "isNew": true,
	})
	if resp["isMig"] != true || resp["checkpointDir"] != "/dmtcp/checkpoints" {
		t.Fatalf("dest register must arm the blocking restore path: %v", resp)
	}

	// 5. preStop on the source: /remove via the real wire types hands back
	// the destination's transfer endpoint.
	body, err := agent.PostJSON(apiURL+"/remove", agent.RemoveRequest{PodName: "web-0"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	var rm agent.RemoveResponse
	if err := json.Unmarshal(body, &rm); err != nil {
		t.Fatalf("decode remove response %q: %v", body, err)
	}
	if !rm.NeedsCheckpoint {
		t.Fatalf("remove must request a checkpoint: %+v", rm)
	}
	if rm.DestAddress != ln.Addr().String() {
		t.Fatalf("destAddress = %q, want %q", rm.DestAddress, ln.Addr().String())
	}

	// 6. Source streams two overlay layers and the DMTCP image, then DONE.
	srcLayer := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcLayer, "mosquitto.db"), []byte("retained-messages"), 0o644); err != nil {
		t.Fatal(err)
	}
	ckpt := filepath.Join(t.TempDir(), "ckpt_web-0.dmtcp")
	if err := os.WriteFile(ckpt, []byte("dmtcp-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for ord, name := range map[int]string{1: "u1", 2: "u2"} {
		if err := agent.SendDir(rm.DestAddress, ord, name, srcLayer); err != nil {
			t.Fatalf("SendDir %s: %v", name, err)
		}
	}
	if err := agent.SendCheckpointFile(rm.DestAddress, ckpt); err != nil {
		t.Fatalf("SendCheckpointFile: %v", err)
	}
	if err := agent.SendDone(rm.DestAddress); err != nil {
		t.Fatalf("SendDone: %v", err)
	}

	res := <-recvCh
	if res.err != nil {
		t.Fatalf("ReceiveAll: %v", res.err)
	}
	if len(res.frames) != 3 {
		t.Fatalf("expected 3 frames (2 layers + checkpoint), got %d: %v", len(res.frames), res.frames)
	}
	for _, name := range []string{"u1", "u2"} {
		got, err := os.ReadFile(filepath.Join(destLayers, name, "mosquitto.db"))
		if err != nil || string(got) != "retained-messages" {
			t.Fatalf("layer %s content: %q, %v", name, got, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(destCkpt, "ckpt_web-0.dmtcp")); err != nil || string(got) != "dmtcp-image" {
		t.Fatalf("checkpoint content: %q, %v", got, err)
	}

	// 7. Source acknowledges the copy; destination acknowledges the restore.
	if _, err := agent.PostJSON(apiURL+"/copy", agent.CopyNotification{
		PodName: "web-0", CheckpointDir: "/dmtcp/checkpoints", LayerCount: 2,
	}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	postJSON(t, apiURL+"/restored", map[string]any{"podName": "web-0"})

	rec, ok := reg.Get("web-0")
	if !ok || !rec.CheckpointReady || !rec.Restored {
		t.Fatalf("final registry state: %+v (ok=%v)", rec, ok)
	}
}

// TestMechanismToggles_Independent asserts each mechanism can be enabled on
// its own and that the toggles reach the agent through /poll and /remove.
func TestMechanismToggles_Independent(t *testing.T) {
	cases := []struct {
		name            string
		process, volume bool
	}{
		{"volume-only", false, true},
		{"process-only", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, apiURL := newAPI(t)
			postJSON(t, apiURL+"/register", map[string]any{
				"podName": "db-0", "podAddress": "10.0.0.9:2486", "isNew": true,
			})
			reg.Arm("db-0", registry.ArmInfo{
				ProcessMigration: tc.process,
				VolumeMigration:  tc.volume,
			})

			poll := getJSON(t, apiURL+"/poll?podName=db-0")
			if poll["processMigration"] != tc.process || poll["volumeMigration"] != tc.volume {
				t.Fatalf("poll toggles: %v, want process=%v volume=%v", poll, tc.process, tc.volume)
			}
			rm := postJSON(t, apiURL+"/remove", map[string]any{"podName": "db-0"})
			if rm["processMigration"] != tc.process || rm["volumeMigration"] != tc.volume {
				t.Fatalf("remove toggles: %v, want process=%v volume=%v", rm, tc.process, tc.volume)
			}
		})
	}
}
