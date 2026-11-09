package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	testAgentCatCommandTemplate    = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
	testAgentBinCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
)

func newTmuxIntegrationTestConfig(projectsBase string) *config.Config {
	testCfg := config.Default()
	testCfg.ProjectsBase = projectsBase

	// Generic tmux integration tests should stay focused on pane/session behavior
	// instead of shelling out to optional memory and coordination helpers.
	testCfg.AgentMail.Enabled = false
	testCfg.CASS.Context.Enabled = false
	testCfg.SessionRecovery.Enabled = false
	testCfg.SessionRecovery.AutoInjectOnSpawn = false
	testCfg.SessionRecovery.IncludeAgentMail = false
	testCfg.SessionRecovery.IncludeBeadsContext = false
	testCfg.SessionRecovery.IncludeCMMemories = false
	testCfg.GeminiSetup.AutoSelectProModel = false
	// The live aggregate-memory admission check (bd-oom-headroom) reads real
	// /proc/meminfo state, which these tests don't control and shouldn't be
	// gated by — a busy/memory-constrained runner can legitimately have less
	// MemAvailable than a few agents' worst-case memory caps.
	testCfg.SpawnPacing.Headroom.Enabled = false

	return testCfg
}

func TestMain(m *testing.M) {
	// Closure tests re-exec this binary as helper processes running real ntm code.
	if mode := os.Getenv(closureHelperModeEnv); mode != "" {
		os.Exit(runClosureHelper(mode))
	}

	// ntm's startup makes the attention feed durable on first use, so tests that
	// run it need their own config folder rather than the user's real state.db.
	configDir, err := os.MkdirTemp("", "ntm-cli-test-config-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test config folder: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", configDir)
	_ = os.Unsetenv("NTM_CONFIG")
	// Spawn saves session manifests even under go test (Loop 5), so in-process
	// tests get their own data folder rather than the real one.
	dataDir, err := os.MkdirTemp("", "ntm-cli-test-data-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test data folder: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_DATA_HOME", dataDir)

	// Clean up any orphan test sessions from previous runs before starting.
	// This catches sessions left behind when tests are interrupted (Ctrl+C, timeout, etc.)
	testutil.KillAllTestSessionsSilent()

	code := m.Run()

	// Clean up after all tests complete
	testutil.KillAllTestSessionsSilent()

	closeRobotPersistence()
	_ = os.RemoveAll(configDir)
	_ = os.RemoveAll(dataDir)

	os.Exit(code)
}
