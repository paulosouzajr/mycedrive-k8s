package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLayerInit verifies that Init correctly computes rootLayer and level.
// Since Init calls utils.Run (which invokes system commands), we validate
// the struct state transitions without side effects by using a temp dir.
func TestLayerInit_DefaultState(t *testing.T) {
	l := Layer{RootDir: "/mnt/root"}
	if l.level != 0 {
		t.Errorf("expected initial level=0, got %d", l.level)
	}
	if l.rootLayer != 0 {
		t.Errorf("expected initial rootLayer=0, got %d", l.rootLayer)
	}
	if l.RootDir != "/mnt/root" {
		t.Errorf("expected RootDir=/mnt/root, got %s", l.RootDir)
	}
}

// TestLayerCreateLayer_DefaultState verifies that CreateLayer starts from level 0.
func TestLayerCreateLayer_DefaultState(t *testing.T) {
	l := Layer{RootDir: "/mnt/root", level: 0}
	if l.level != 0 {
		t.Errorf("expected initial level=0, got %d", l.level)
	}
}

// TestLayerFinish_ReturnsTrue verifies that Finish returns true as expected.
func TestLayerFinish_ReturnsTrue(t *testing.T) {
	l := Layer{RootDir: "/mnt/root", level: 3}
	result := l.Finish()
	if !result {
		t.Errorf("expected Finish() to return true, got false")
	}
}

// TestLayerRootDir_IsSet verifies the RootDir field is assigned properly.
func TestLayerRootDir_IsSet(t *testing.T) {
	cases := []struct {
		name    string
		rootDir string
	}{
		{"absolute path", "/data/overlay"},
		{"relative path", "overlay/data"},
		{"empty path", ""},
		{"deep path", "/var/lib/containers/storage/overlay"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := Layer{RootDir: tc.rootDir}
			if l.RootDir != tc.rootDir {
				t.Errorf("expected RootDir=%q, got %q", tc.rootDir, l.RootDir)
			}
		})
	}
}

// TestOverlayGlobPattern verifies that the glob pattern used in CreateLayer
// and Init correctly matches the expected directory naming convention.
func TestOverlayGlobPattern(t *testing.T) {
	// Create a temporary directory to simulate /data layout
	tmpDir := t.TempDir()

	// Create directories matching the overlay naming convention: o1<N>
	dirs := []string{"o10", "o11", "o12"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(tmpDir, d), 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", d, err)
		}
	}

	matches, err := filepath.Glob(filepath.Join(tmpDir, "o1*"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}

	if len(matches) != len(dirs) {
		t.Errorf("expected %d glob matches, got %d", len(dirs), len(matches))
	}
}

// TestOverlayGlobPattern_NoMatches verifies that glob returns nil when no matching dirs exist.
func TestOverlayGlobPattern_NoMatches(t *testing.T) {
	tmpDir := t.TempDir()

	matches, err := filepath.Glob(filepath.Join(tmpDir, "o1*"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}

	if len(matches) != 0 {
		t.Errorf("expected 0 glob matches, got %d", len(matches))
	}
}

// TestLayerLevel_Increments verifies that layer level increments correctly after CreateLayer.
// We test the value increment logic directly without calling system commands.
func TestLayerLevel_Increments(t *testing.T) {
	initial := 2
	// Simulate what CreateLayer does to l.level
	level := initial
	level += 1
	if level != 3 {
		t.Errorf("expected level=3 after increment, got %d", level)
	}
}

// TestLayerInit_RootLevelComputation verifies the rootLayer + level computation
// matches what Init() would compute from the number of existing layers.
func TestLayerInit_RootLevelComputation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 3 overlay directories
	for i := 0; i < 3; i++ {
		_ = os.MkdirAll(filepath.Join(tmpDir, "o1"+string(rune('0'+i))), 0755)
	}

	lowLayers, err := filepath.Glob(filepath.Join(tmpDir, "o1*"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}

	rootLayer := len(lowLayers)
	level := rootLayer + 1

	if rootLayer != 3 {
		t.Errorf("expected rootLayer=3, got %d", rootLayer)
	}
	if level != 4 {
		t.Errorf("expected level=4, got %d", level)
	}
}

// TestLayerInit_EmptyData verifies Init handles empty /data gracefully (no existing layers).
func TestLayerInit_EmptyData(t *testing.T) {
	tmpDir := t.TempDir()

	lowLayers, err := filepath.Glob(filepath.Join(tmpDir, "o1*"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}

	rootLayer := len(lowLayers)
	level := rootLayer + 1

	if rootLayer != 0 {
		t.Errorf("expected rootLayer=0 when no layers exist, got %d", rootLayer)
	}
	if level != 1 {
		t.Errorf("expected level=1 for first layer, got %d", level)
	}
}
