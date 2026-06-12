// Package overlay implements the volume-checkpointing mechanism from
// "Stateful Container Migration in Geo-Distributed Environments"
// (CloudCom 2020, §IV). The pod's volume is remounted as an OverlayFS stack;
// each checkpoint freezes the current upper layer into a read-only layer and
// stacks a fresh writable upper on top, so frozen layers can be streamed to
// the destination node while the application keeps running.
package overlay

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go-agent/utils"
)

// Runner executes a privileged system command (mount/umount). It is
// injectable so unit tests can run without real mounts.
type Runner func(name string, args ...string) error

// ExecRunner runs the command for real via os/exec.
func ExecRunner(name string, args ...string) error {
	_, err := utils.Run(name, strings.Join(args, " "))
	return err
}

// VolumeManager is the five-method volume migration API from the paper.
type VolumeManager interface {
	// InitVolume ensures the lowerdir/upperdir structure exists and
	// (re)mounts the volume as an OverlayFS stack.
	InitVolume() error
	// CreateCheckpoint freezes the current upper layer as read-only layer N,
	// creates a new empty upper layer and remounts on the fly. It returns
	// the ordinal of the frozen layer.
	CreateCheckpoint() (int, error)
	// CopyCheckpoint streams every frozen, not-yet-transferred layer to the
	// destination agent at destAddr (tar+gzip over TCP).
	CopyCheckpoint(destAddr string) error
	// ReceiveCheckpoint extracts one incoming layer payload (gzip+tar
	// stream) into lower layer <ordinal> on the destination node.
	ReceiveCheckpoint(ordinal int, payload io.Reader) error
	// EndVolume unmounts the overlay stack and transfers the final,
	// un-transferred upper layer to destAddr (empty destAddr skips the
	// transfer, e.g. on normal termination).
	EndVolume(destAddr string) error
}

// LayerManager implements VolumeManager.
//
// On-disk layout inside DataDir:
//
//	u<N>      upper (writable) dir of level N; frozen once level N+1 exists
//	w<N>      OverlayFS workdir of level N
//	o<N>      merged mountpoint of level N
//	l<N>      received lower layer N (destination side)
//	.sent_<N> marker: layer u<N> was successfully transferred
type LayerManager struct {
	DataDir string // layer storage root (default /data)
	RootDir string // application volume mountpoint
	Run     Runner // mount/umount executor

	level int // current writable level (0 = not mounted yet)
}

// NewLayerManager returns a LayerManager using the real ExecRunner.
func NewLayerManager(dataDir, rootDir string) *LayerManager {
	if dataDir == "" {
		dataDir = "/data"
	}
	return &LayerManager{DataDir: dataDir, RootDir: rootDir, Run: ExecRunner}
}

// Level returns the current writable layer level (0 when unmounted).
func (lm *LayerManager) Level() int { return lm.level }

// Discover derives the current level from the directories present in
// DataDir, so a separate process (the preStop hook) can resume management of
// an overlay stack created by the agent at container start.
func (lm *LayerManager) Discover() error {
	uppers, err := lm.numberedDirs("u")
	if err != nil {
		return err
	}
	if len(uppers) == 0 {
		lm.level = 0
		return nil
	}
	lm.level = uppers[len(uppers)-1]
	return nil
}

// InitVolume implements the paper's Init Volume method: it creates the
// upperdir/workdir/merged structure for a fresh writable layer, mounts the
// overlay using any received lower layers (or the original volume content)
// as lowerdir, and bind-mounts the merged view over RootDir.
func (lm *LayerManager) InitVolume() error {
	lowers, err := lm.numberedDirs("l")
	if err != nil {
		return fmt.Errorf("list lower layers: %w", err)
	}

	level := lm.level + 1
	if err := lm.mkLevelDirs(level); err != nil {
		return err
	}

	// Newest layer must be the leftmost (topmost) lowerdir entry.
	var lowerdir string
	if len(lowers) == 0 {
		lowerdir = lm.RootDir
	} else {
		parts := make([]string, 0, len(lowers))
		for i := len(lowers) - 1; i >= 0; i-- {
			parts = append(parts, lm.dir("l", lowers[i]))
		}
		lowerdir = strings.Join(parts, ":")
	}

	if err := lm.mountLevel(level, lowerdir); err != nil {
		return err
	}
	if err := lm.Run("mount", "--bind", lm.dir("o", level), lm.RootDir); err != nil {
		return fmt.Errorf("bind %s over %s: %w", lm.dir("o", level), lm.RootDir, err)
	}
	lm.level = level
	return nil
}

// CreateCheckpoint implements the paper's Create Checkpoint method.
func (lm *LayerManager) CreateCheckpoint() (int, error) {
	if lm.level == 0 {
		return 0, fmt.Errorf("volume not initialised (call InitVolume first)")
	}
	frozen := lm.level
	level := frozen + 1
	if err := lm.mkLevelDirs(level); err != nil {
		return 0, err
	}
	// The previous merged view becomes the (frozen, read-only) lowerdir.
	if err := lm.mountLevel(level, lm.dir("o", frozen)); err != nil {
		return 0, err
	}
	if err := lm.Run("umount", "-l", lm.RootDir); err != nil {
		return 0, fmt.Errorf("unbind %s: %w", lm.RootDir, err)
	}
	if err := lm.Run("mount", "--bind", lm.dir("o", level), lm.RootDir); err != nil {
		return 0, fmt.Errorf("bind %s over %s: %w", lm.dir("o", level), lm.RootDir, err)
	}
	lm.level = level
	return frozen, nil
}

// CopyCheckpoint implements the paper's Copy Checkpoint method.
func (lm *LayerManager) CopyCheckpoint(destAddr string) error {
	if destAddr == "" {
		return fmt.Errorf("no destination address")
	}
	uppers, err := lm.numberedDirs("u")
	if err != nil {
		return fmt.Errorf("list upper layers: %w", err)
	}
	for _, n := range uppers {
		if n >= lm.level || lm.isSent(n) {
			continue // still writable, or already transferred
		}
		if err := utils.SendDir(destAddr, n, fmt.Sprintf("u%d", n), lm.dir("u", n)); err != nil {
			return fmt.Errorf("send layer %d: %w", n, err)
		}
		if err := lm.markSent(n); err != nil {
			return err
		}
	}
	return nil
}

// ReceiveCheckpoint implements the paper's Receive Checkpoint method. The
// payload is a gzip-compressed tar stream of one layer. When the volume is
// already mounted the overlay is remounted to include the new layer.
func (lm *LayerManager) ReceiveCheckpoint(ordinal int, payload io.Reader) error {
	dest := lm.dir("l", ordinal)
	if err := utils.ExtractTarGz(payload, dest); err != nil {
		return fmt.Errorf("extract layer %d: %w", ordinal, err)
	}
	if lm.level > 0 {
		// Remount on the fly so the running container sees the new layer.
		if err := lm.Run("umount", "-l", lm.RootDir); err != nil {
			return fmt.Errorf("unbind %s: %w", lm.RootDir, err)
		}
		if err := lm.Run("umount", "-l", lm.dir("o", lm.level)); err != nil {
			return fmt.Errorf("unmount level %d: %w", lm.level, err)
		}
		lm.level--
		return lm.InitVolume()
	}
	return nil
}

// EndVolume implements the paper's End Volume method: unmount the stack and
// transfer the final upper layer that was never frozen/copied.
func (lm *LayerManager) EndVolume(destAddr string) error {
	if lm.level == 0 {
		return fmt.Errorf("volume not initialised")
	}
	final := lm.level

	if err := lm.Run("umount", "-l", lm.RootDir); err != nil {
		return fmt.Errorf("unbind %s: %w", lm.RootDir, err)
	}
	merged, err := lm.numberedDirs("o")
	if err != nil {
		return fmt.Errorf("list merged dirs: %w", err)
	}
	for i := len(merged) - 1; i >= 0; i-- {
		if err := lm.Run("umount", "-l", lm.dir("o", merged[i])); err != nil {
			return fmt.Errorf("unmount level %d: %w", merged[i], err)
		}
	}
	lm.level = 0

	if destAddr != "" && !lm.isSent(final) {
		if err := utils.SendDir(destAddr, final, fmt.Sprintf("u%d", final), lm.dir("u", final)); err != nil {
			return fmt.Errorf("send final layer %d: %w", final, err)
		}
		if err := lm.markSent(final); err != nil {
			return err
		}
	}
	return nil
}

// LayerDir returns the destination directory for received lower layer n.
func (lm *LayerManager) LayerDir(n int) string { return lm.dir("l", n) }

// --- helpers ---

func (lm *LayerManager) dir(prefix string, n int) string {
	return filepath.Join(lm.DataDir, fmt.Sprintf("%s%d", prefix, n))
}

func (lm *LayerManager) mkLevelDirs(level int) error {
	for _, p := range []string{"u", "w", "o"} {
		if err := os.MkdirAll(lm.dir(p, level), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", lm.dir(p, level), err)
		}
	}
	return nil
}

func (lm *LayerManager) mountLevel(level int, lowerdir string) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerdir, lm.dir("u", level), lm.dir("w", level))
	if err := lm.Run("mount", "-t", "overlay", "overlay", "-o", opts, lm.dir("o", level)); err != nil {
		return fmt.Errorf("mount overlay level %d: %w", level, err)
	}
	return nil
}

func (lm *LayerManager) sentMarker(n int) string {
	return filepath.Join(lm.DataDir, fmt.Sprintf(".sent_%d", n))
}

func (lm *LayerManager) isSent(n int) bool {
	_, err := os.Stat(lm.sentMarker(n))
	return err == nil
}

func (lm *LayerManager) markSent(n int) error {
	if err := os.WriteFile(lm.sentMarker(n), nil, 0o644); err != nil {
		return fmt.Errorf("mark layer %d sent: %w", n, err)
	}
	return nil
}

// numberedDirs returns the ordinals of DataDir entries named <prefix><N>,
// sorted numerically (so 10 sorts after 9, not after 1).
func (lm *LayerManager) numberedDirs(prefix string) ([]int, error) {
	entries, err := os.ReadDir(lm.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var nums []int
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		n, convErr := strconv.Atoi(e.Name()[len(prefix):])
		if convErr != nil || n <= 0 {
			continue
		}
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums, nil
}
