//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
)

func TestRCHHookInterception(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "rch.log")
	rchPath := filepath.Join(tempDir, "rch")

	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 0\n", logPath)
	if err := os.WriteFile(rchPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write stub rch binary: %v", err)
	}

	entry, err := dcg.GenerateRCHHookEntry(dcg.RCHHookOptions{
		BinaryPath: rchPath,
		Patterns:   []string{"^go build"},
	})
	if err != nil {
		t.Fatalf("GenerateRCHHookEntry failed: %v", err)
	}
	if len(entry.Hooks) != 1 {
		t.Fatalf("GenerateRCHHookEntry made %d handlers, want 1", len(entry.Hooks))
	}
	command := entry.Hooks[0].Command

	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "CLAUDE_TOOL_INPUT_command=go build ./...")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook command failed: %v output: %s", err, string(output))
	}
	if !strings.Contains(string(output), "\"permissionDecision\":\"deny\"") {
		t.Fatalf("expected permissionDecision deny in output, got: %s", string(output))
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed reading rch log: %v", err)
	}
	if !strings.Contains(string(logData), "intercept -- go build ./...") {
		t.Fatalf("expected intercept invocation, got: %s", string(logData))
	}

	cmd = exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "CLAUDE_TOOL_INPUT_command=ls -la")
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook command (non-match) failed: %v output: %s", err, string(output))
	}

	logData, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed reading rch log (non-match): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 intercept call, got %d: %s", len(lines), string(logData))
	}
}
