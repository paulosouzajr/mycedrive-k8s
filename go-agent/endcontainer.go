package main

// Source-side migration path: the Kubernetes preStop hook.
//
// Per the ICFEC 2022 paper (§IV), end_container calls /remove on the MC to
// learn whether this termination is part of a migration. If it is, the agent
// performs the paper's checkpoint sequence and only returns once everything
// has been transferred to the destination Execution Agent, so the kubelet
// cannot kill the pod before its state is safe:
//
//  1. iterative volume pre-transfer rounds (CreateCheckpoint+CopyCheckpoint,
//     CloudCom 2020 §IV) while the application is still running;
//  2. DMTCP process checkpoint, then transfer of the *.dmtcp files;
//  3. EndVolume: unmount and transfer of the final upper layer;
//  4. DONE frame to the destination, /copy notification to the MC.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"go-agent/dmtcp"
	"go-agent/overlay"
	"go-agent/utils"
)

// runEndContainer is the preStop hook entry point.
//
//	Usage: end_container [<coordAddr> <podName> <checkpointDir>]
//
// Missing arguments fall back to MIGR_COOR, POD_NAME and
// DMTCP_CHECKPOINT_DIR so manifests can rely on the container environment.
func runEndContainer() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "end_container" {
		args = args[1:]
	}
	coordAddr := utils.EnvOr("MIGR_COOR", "")
	podName := utils.EnvOr("POD_NAME", "")
	checkpointDir := utils.EnvOr("DMTCP_CHECKPOINT_DIR", "/dmtcp/checkpoints")
	if len(args) > 0 {
		coordAddr = args[0]
	}
	if len(args) > 1 {
		podName = args[1]
	}
	if len(args) > 2 {
		checkpointDir = args[2]
	}
	if coordAddr == "" || podName == "" {
		log.Fatal("Usage: end_container <coordAddr> <podName> <checkpointDir> (or set MIGR_COOR / POD_NAME)")
	}
	if err := endContainer(coordAddr, podName, checkpointDir); err != nil {
		log.Fatalf("end_container failed: %v", err)
	}
}

// endContainer drives the source-side checkpoint and transfer sequence.
func endContainer(coordAddr, podName, checkpointDir string) error {
	body, err := utils.PostJSON(fmt.Sprintf("http://%s/remove", coordAddr), utils.RemoveRequest{PodName: podName})
	if err != nil {
		return fmt.Errorf("POST /remove: %w", err)
	}
	var resp utils.RemoveResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse /remove response: %w", err)
	}
	if !resp.NeedsCheckpoint {
		log.Println("normal termination, no checkpoint required")
		return nil
	}

	procMig := utils.ProcessMigrationEnabled()
	volMig := utils.VolumeMigrationEnabled()
	dest := resp.DestAddress
	if dest == "" {
		log.Println("warning: MC did not provide destAddress; checkpoints stay local for MC-driven copy")
	}
	log.Printf("migration termination: processMigration=%v volumeMigration=%v dest=%q", procMig, volMig, dest)

	var lm *overlay.LayerManager
	rootDir := utils.EnvOr("VOLUME_ROOT_DIR", "")
	if volMig && rootDir == "" {
		log.Println("ENABLE_VOLUME_MIGRATION is true but VOLUME_ROOT_DIR is unset; skipping volume migration")
		volMig = false
	}
	layersSent := 0

	// 1. Iterative volume pre-transfer rounds while the app still runs.
	if volMig {
		lm = overlay.NewLayerManager(utils.EnvOr("DATA_DIR", "/data"), rootDir)
		if err := lm.Discover(); err != nil {
			return fmt.Errorf("discover overlay state: %w", err)
		}
		if lm.Level() > 0 && dest != "" {
			rounds := utils.EnvInt("CHECKPOINT_ROUNDS", 1)
			for i := 0; i < rounds; i++ {
				frozen, err := lm.CreateCheckpoint()
				if err != nil {
					return fmt.Errorf("volume checkpoint round %d: %w", i+1, err)
				}
				if err := lm.CopyCheckpoint(dest); err != nil {
					return fmt.Errorf("copy volume checkpoint %d: %w", frozen, err)
				}
				layersSent++
				log.Printf("volume pre-transfer round %d/%d: layer %d sent", i+1, rounds, frozen)
			}
		}
	}

	// 2. DMTCP process checkpoint, then transfer the checkpoint files.
	if procMig {
		h := dmtcp.NewHandlerFromEnv(checkpointDir)
		h.AttachRunning()
		if err := h.Checkpoint(); err != nil {
			return fmt.Errorf("dmtcp checkpoint: %w", err)
		}
		if _, err := h.WaitForCheckpointFile(60 * time.Second); err != nil {
			return fmt.Errorf("wait for checkpoint files: %w", err)
		}
		files, err := h.ListCheckpoints()
		if err != nil {
			return fmt.Errorf("list checkpoints: %w", err)
		}
		log.Printf("checkpoint ready: %v", files)
		if dest != "" {
			for _, f := range files {
				if err := utils.SendCheckpointFile(dest, f); err != nil {
					return fmt.Errorf("send checkpoint file %s: %w", f, err)
				}
			}
			log.Printf("%d checkpoint file(s) transferred to %s", len(files), dest)
		}
	}

	// 3. Unmount the volume and transfer the final upper layer.
	if volMig {
		if lm.Level() > 0 {
			if err := lm.EndVolume(dest); err != nil {
				return fmt.Errorf("end volume: %w", err)
			}
			if dest != "" {
				layersSent++
			}
		} else if dest != "" {
			// Volume was never overlay-mounted: ship the whole root dir
			// as layer 1 (the bash prototype's tar_main_flow path).
			if err := utils.SendDir(dest, 1, "u1", rootDir); err != nil {
				return fmt.Errorf("send volume root: %w", err)
			}
			layersSent++
		}
	}

	// 4. Tell the destination the stream is complete, then notify the MC.
	if dest != "" {
		if err := utils.SendDone(dest); err != nil {
			return fmt.Errorf("send done frame: %w", err)
		}
	}
	if _, err := utils.PostJSON(fmt.Sprintf("http://%s/copy", coordAddr), utils.CopyNotification{
		PodName:       podName,
		CheckpointDir: checkpointDir,
		LayerCount:    layersSent,
	}); err != nil {
		return fmt.Errorf("POST /copy: %w", err)
	}
	log.Println("checkpoint transfer complete, container stopping")
	return nil
}
