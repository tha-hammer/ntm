package pipeline

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sergi/go-diff/diffmatchpatch"
)

var updateGoldens = flag.Bool("update-goldens", false, "rewrite e2e pipeline golden files")

func assertDispatchGolden(t *testing.T, workspace, goldenPath string) {
	t.Helper()

	actual := collectDispatchGolden(t, workspace)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(actual), 0o644); err != nil {
			t.Fatalf("write golden %q: %v", goldenPath, err)
		}
		return
	}

	expectedBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %q: %v", goldenPath, err)
	}
	expected := string(expectedBytes)
	if expected != actual {
		t.Fatalf("dispatch golden mismatch for %s; run go test ./e2e/pipeline -update-goldens to refresh\n%s", goldenPath, unifiedTextDiff(expected, actual))
	}
}

func collectDispatchGolden(t *testing.T, workspace string) string {
	t.Helper()

	logDir := filepath.Join(workspace, "session-logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read dispatch log dir %q: %v", logDir, err)
	}

	var logs []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "dispatch-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(logDir, entry.Name()))
		if err != nil {
			t.Fatalf("read dispatch log %q: %v", entry.Name(), err)
		}
		logs = append(logs, normalizeDispatchLog(string(content), workspace))
	}
	if len(logs) == 0 {
		t.Fatal("no dispatch logs found")
	}
	sort.Strings(logs)

	var b strings.Builder
	for i, log := range logs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "--- dispatch %d ---\n", i+1)
		b.WriteString(log)
	}
	return b.String()
}

func normalizeDispatchLog(content, workspace string) string {
	content = strings.ReplaceAll(content, workspace, "<WORKSPACE>")

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Target pane: ") {
			lines[i] = "Target pane: " + stablePaneLabel(strings.TrimSpace(strings.TrimPrefix(line, "Target pane: ")))
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func stablePaneLabel(paneID string) string {
	if strings.HasPrefix(paneID, "%") {
		index := strings.TrimPrefix(paneID, "%")
		if index != "" {
			return "pane-" + index
		}
	}
	return paneID
}

func unifiedTextDiff(expected, actual string) string {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(expected, actual, false)
	dmp.DiffCleanupSemantic(diffs)
	return dmp.DiffPrettyText(diffs)
}
