// MyceDrive Execution Agent (EA).
//
// The agent runs inside every application container. At container start it
// registers with the Migration Coordinator (MC). On a migration target it
// receives the source pod's checkpoints (overlay volume layers and/or DMTCP
// process checkpoints), mounts the overlay volume and execs dmtcp_restart.
// As the "end_container" preStop hook it checkpoints and streams state to
// the destination before letting the source pod terminate.
//
// Feature flags (each independently toggleable, both default to true):
//
//	ENABLE_PROCESS_MIGRATION  DMTCP memory/socket checkpoint + restore
//	ENABLE_VOLUME_MIGRATION   OverlayFS volume layer checkpointing
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go-agent/dmtcp"
	"go-agent/overlay"
	"go-agent/utils"
)

// Message mirrors the server-side struct for JSON serialisation.
// ProcessMigration / VolumeMigration are additive fields that advertise the
// agent's enabled mechanisms to the MC (older MCs ignore them).
type Message struct {
	PodName          string `json:"podName"`
	PodAddress       string `json:"podAddress"`
	ContainerPort    int    `json:"containerPort,omitempty"`
	IsNew            bool   `json:"isNew"`
	IsMig            bool   `json:"isMig"`
	ProcessMigration bool   `json:"processMigration,omitempty"`
	VolumeMigration  bool   `json:"volumeMigration,omitempty"`
}

const defaultCoordAddr = "localhost:80"

// restoredMarker is created in the checkpoint directory just before the
// agent execs dmtcp_restart. The container entrypoint consults it after the
// agent returns: marker present means the restored application already ran
// (and exited), so the entrypoint must NOT dmtcp_launch a fresh instance.
func restoredMarker(checkpointDir string) string {
	return filepath.Join(checkpointDir, ".restored")
}

func main() {
	// The binary doubles as the Kubernetes preStop hook when invoked as
	// "end_container" (via symlink/argv[0] or first argument).
	if filepath.Base(os.Args[0]) == "end_container" || (len(os.Args) > 1 && os.Args[1] == "end_container") {
		runEndContainer()
		return
	}
	runAgent()
}

// runAgent is the container-start entry point.
func runAgent() {
	coordAddr := "http://" + utils.EnvOr("MIGR_COOR", defaultCoordAddr)
	procMig := utils.ProcessMigrationEnabled()
	volMig := utils.VolumeMigrationEnabled()

	rootDir := utils.EnvOr("VOLUME_ROOT_DIR", "")
	if len(os.Args) > 1 && os.Args[1] != "" {
		rootDir = os.Args[1]
	}
	if volMig && rootDir == "" {
		log.Println("ENABLE_VOLUME_MIGRATION is true but no volume root dir (arg or VOLUME_ROOT_DIR); disabling volume migration")
		volMig = false
	}
	if len(os.Args) > 2 { // legacy layerCount argument, now governed by CHECKPOINT_ROUNDS
		if _, err := strconv.Atoi(os.Args[2]); err != nil {
			log.Fatalf("Invalid layerCount argument: %v", err)
		}
	}

	transferPort := utils.TransferPort()
	checkpointDir := utils.EnvOr("DMTCP_CHECKPOINT_DIR", "/dmtcp/checkpoints")
	dataDir := utils.EnvOr("DATA_DIR", "/data")

	registerMsg := Message{
		PodAddress:       net.JoinHostPort(os.Getenv("POD_IP"), strconv.Itoa(transferPort)),
		ContainerPort:    transferPort,
		PodName:          os.Getenv("POD_NAME"),
		IsNew:            true,
		ProcessMigration: procMig,
		VolumeMigration:  volMig,
	}
	log.Printf("Registering with MC at %s: %+v", coordAddr, registerMsg)

	reply, err := utils.PostJSON(coordAddr+"/register", registerMsg)
	if err != nil {
		log.Fatalf("Failed to register with Migration Coordinator: %v", err)
	}
	var response Message
	if err := json.Unmarshal(reply, &response); err != nil {
		log.Fatalf("Failed to parse register response: %v", err)
	}
	log.Printf("Register response from MC: %+v", response)

	lm := overlay.NewLayerManager(dataDir, rootDir)

	if response.IsMig {
		runMigrationTarget(lm, transferPort, checkpointDir, procMig, volMig)
		return
	}

	// Fresh start or non-migration duplicate registration.
	if volMig {
		if err := lm.InitVolume(); err != nil {
			log.Fatalf("overlay init failed: %v", err)
		}
		log.Printf("overlay volume initialised at level %d over %s", lm.Level(), rootDir)
	}
	// The entrypoint dmtcp_launches the application after we return.
}

// runMigrationTarget receives the source pod's checkpoints and restores.
func runMigrationTarget(lm *overlay.LayerManager, transferPort int, checkpointDir string, procMig, volMig bool) {
	log.Printf("Pod is migration target: listening on :%d for checkpoint transfer", transferPort)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", transferPort))
	if err != nil {
		log.Fatalf("listen on :%d: %v", transferPort, err)
	}
	defer ln.Close()

	timeout := time.Duration(utils.EnvInt("RECEIVE_TIMEOUT_SECONDS", 600)) * time.Second
	layers, ckptFiles := 0, 0

	frames, err := utils.ReceiveAll(ln, timeout, func(h utils.FrameHeader, payload io.Reader) error {
		switch h.Kind {
		case utils.KindLayer:
			layers++
			log.Printf("receiving volume layer %d (%s)", h.Ordinal, h.Name)
			return lm.ReceiveCheckpoint(h.Ordinal, payload)
		case utils.KindCheckpointFile:
			ckptFiles++
			log.Printf("receiving checkpoint file %s", h.Name)
			return utils.ExtractTarGz(payload, checkpointDir)
		default:
			return fmt.Errorf("unexpected frame kind %d", h.Kind)
		}
	})
	if err != nil {
		log.Fatalf("checkpoint transfer failed after %d frame(s): %v", len(frames), err)
	}
	log.Printf("transfer complete: %d volume layer(s), %d checkpoint file(s)", layers, ckptFiles)

	if volMig {
		if err := lm.InitVolume(); err != nil {
			log.Fatalf("overlay init with received layers failed: %v", err)
		}
		log.Printf("overlay volume mounted at level %d with %d received layer(s)", lm.Level(), layers)
	}

	if procMig {
		h := dmtcp.NewHandlerFromEnv(checkpointDir)
		if ckpts, _ := h.ListCheckpoints(); len(ckpts) > 0 {
			marker := restoredMarker(checkpointDir)
			if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
				log.Fatalf("write restore marker: %v", err)
			}
			// ExecRestart replaces this process with dmtcp_restart; the
			// entrypoint's agent invocation becomes the restored app.
			if err := h.ExecRestart(); err != nil {
				os.Remove(marker)
				log.Fatalf("dmtcp restore failed: %v", err)
			}
		} else {
			log.Println("process migration enabled but no checkpoint files received; entrypoint will launch fresh")
		}
	}
}
