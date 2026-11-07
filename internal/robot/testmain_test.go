package robot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestMain(m *testing.M) {
	// Some code paths open the default state store, such as the ensemble state
	// store. Tests need their own config folder rather than the user's real state.db,
	// because opening it applies any pending migrations.
	configDir, err := os.MkdirTemp("", "ntm-robot-test-config-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test config folder: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", configDir)
	_ = os.Unsetenv("NTM_CONFIG")

	code := m.Run()

	ensemble.CloseDefaultStateStore()
	_ = os.RemoveAll(configDir)

	os.Exit(code)
}

func TestMain_IsolatesStateDB(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	realConfig := filepath.Join(home, ".config") + string(filepath.Separator)
	if path := state.DefaultPath(); strings.HasPrefix(path, realConfig) {
		t.Fatalf("robot tests would use the real state.db: %s", path)
	}
}
