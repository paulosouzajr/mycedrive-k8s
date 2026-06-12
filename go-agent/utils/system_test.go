package utils

import (
	"strings"
	"testing"
)

// --- Run ---

func TestRun_Success(t *testing.T) {
	out, err := Run("echo", "hello world")
	if err != nil {
		t.Fatalf("Run(echo) unexpected error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello world" {
		t.Errorf("unexpected output %q", out)
	}
}

func TestRun_ReturnsErrorOnFailure(t *testing.T) {
	_, err := Run("false", "")
	if err == nil {
		t.Error("Run(false) should return an error")
	}
}

func TestRun_ReturnsErrorOnMissingBinary(t *testing.T) {
	_, err := Run("definitely-not-a-real-binary-xyz", "")
	if err == nil {
		t.Error("Run on missing binary should return an error")
	}
}

// --- EnvBool / flag parsing ---

func TestEnvBool_DefaultWhenUnset(t *testing.T) {
	if !EnvBool("MYCEDRIVE_TEST_UNSET_FLAG", true) {
		t.Error("expected default true for unset variable")
	}
	if EnvBool("MYCEDRIVE_TEST_UNSET_FLAG", false) {
		t.Error("expected default false for unset variable")
	}
}

func TestEnvBool_Values(t *testing.T) {
	cases := []struct {
		raw  string
		def  bool
		want bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"false", true, false},
		{"FALSE", true, false},
		{"0", true, false},
		{"no", true, false},
		{"off", true, false},
		{" true ", false, true}, // whitespace tolerated
		{"banana", true, true},  // unrecognised -> default
		{"banana", false, false},
	}
	for _, tc := range cases {
		t.Setenv("MYCEDRIVE_TEST_FLAG", tc.raw)
		if got := EnvBool("MYCEDRIVE_TEST_FLAG", tc.def); got != tc.want {
			t.Errorf("EnvBool(%q, def=%v) = %v, want %v", tc.raw, tc.def, got, tc.want)
		}
	}
}

func TestMigrationFlags_DefaultTrue(t *testing.T) {
	// Defaults documented in main.go: both mechanisms enabled.
	if !ProcessMigrationEnabled() {
		t.Error("ENABLE_PROCESS_MIGRATION must default to true")
	}
	if !VolumeMigrationEnabled() {
		t.Error("ENABLE_VOLUME_MIGRATION must default to true")
	}
}

func TestMigrationFlags_IndependentToggle(t *testing.T) {
	t.Setenv("ENABLE_PROCESS_MIGRATION", "false")
	t.Setenv("ENABLE_VOLUME_MIGRATION", "true")
	if ProcessMigrationEnabled() {
		t.Error("process migration should be disabled")
	}
	if !VolumeMigrationEnabled() {
		t.Error("volume migration should stay enabled")
	}

	t.Setenv("ENABLE_PROCESS_MIGRATION", "true")
	t.Setenv("ENABLE_VOLUME_MIGRATION", "false")
	if !ProcessMigrationEnabled() {
		t.Error("process migration should be enabled")
	}
	if VolumeMigrationEnabled() {
		t.Error("volume migration should be disabled")
	}
}

// --- EnvInt / transfer port ---

func TestEnvInt(t *testing.T) {
	t.Setenv("MYCEDRIVE_TEST_INT", "2500")
	if got := EnvInt("MYCEDRIVE_TEST_INT", 1); got != 2500 {
		t.Errorf("EnvInt = %d, want 2500", got)
	}
	t.Setenv("MYCEDRIVE_TEST_INT", "not-a-number")
	if got := EnvInt("MYCEDRIVE_TEST_INT", 42); got != 42 {
		t.Errorf("EnvInt invalid = %d, want default 42", got)
	}
	t.Setenv("MYCEDRIVE_TEST_INT", "70000") // out of port range
	if got := EnvInt("MYCEDRIVE_TEST_INT", 42); got != 42 {
		t.Errorf("EnvInt out-of-range = %d, want default 42", got)
	}
}

func TestTransferPort_RespectsContainerPort(t *testing.T) {
	t.Setenv("CONTAINER_PORT", "3001")
	if got := TransferPort(); got != 3001 {
		t.Errorf("TransferPort = %d, want 3001", got)
	}
	t.Setenv("CONTAINER_PORT", "")
	if got := TransferPort(); got != DefaultTransferPort {
		t.Errorf("TransferPort default = %d, want %d", got, DefaultTransferPort)
	}
}
