package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeCassHealthScript(t *testing.T, healthJSON string, exitCode string) string {
	t.Helper()

	fakeDir := t.TempDir()
	fakeCass := filepath.Join(fakeDir, "cass")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"cass 0.3.7\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"health\" ] && [ \"$2\" = \"--json\" ]; then\n" +
		"  printf '%s\\n' '" + healthJSON + "'\n" +
		"  exit " + exitCode + "\n" +
		"fi\n" +
		"printf '%s\\n' '{}'\n"
	if err := os.WriteFile(fakeCass, []byte(script), 0755); err != nil {
		t.Fatalf("write fake cass: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+oldPath)
	return fakeCass
}

func TestCASSAdapter_HealthCurrentSchemaWithoutInitializedUsesUnhealthyDetails(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"corrupt_index","healthy":false,"errors":["lexical index missing"],"recommended_action":"Run cass index --full --json"}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatal("Health() Healthy = true, want false")
	}
	if strings.Contains(health.Message, "not initialized") {
		t.Fatalf("Health() message = %q, should not misclassify as uninitialized", health.Message)
	}
	for _, want := range []string{"cass reports unhealthy", "corrupt_index", "lexical index missing", "Run cass index --full --json"} {
		if !strings.Contains(health.Message, want) {
			t.Fatalf("Health() message = %q, want substring %q", health.Message, want)
		}
	}
}

func TestCASSAdapter_HealthInitializedFalseStillMapsToUninitialized(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"not_initialized","healthy":false,"initialized":false}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatal("Health() Healthy = true, want false")
	}
	if !strings.Contains(health.Message, "not initialized") {
		t.Fatalf("Health() message = %q, want uninitialized hint", health.Message)
	}
}

func TestCASSAdapter_HealthCurrentSchemaWithoutHealthyUsesStatusAndExitCode(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"healthy","initialized":true}`,
		"0",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("Health() Healthy = false, want true when status is healthy and command succeeds")
	}
	if !strings.Contains(health.Message, "cass is healthy") {
		t.Fatalf("Health() message = %q, want healthy message", health.Message)
	}
}

func TestCASSAdapter_HealthWithoutHealthyUnknownStatusFailsClosed(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"warning","initialized":true}`,
		"0",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health() Healthy = true, want false for unknown status without healthy field")
	}
	if !strings.Contains(health.Message, "cass reports unhealthy: warning") {
		t.Fatalf("Health() message = %q, want warning unhealthy message", health.Message)
	}
}

func TestCASSAdapter_HealthWithoutHealthyAndStatusFailsClosed(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{}`,
		"0",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health() Healthy = true, want false when schema omits both healthy and status")
	}
	if !strings.Contains(health.Message, "cass reports unhealthy: unhealthy") {
		t.Fatalf("Health() message = %q, want fail-closed unhealthy message", health.Message)
	}
}

// TestCASSAdapter_HealthStaleDerivedIndexIsDegradedNotUnhealthy is the acfs#296
// regression guard. cass reports healthy:false / status:unhealthy when its
// derived lexical index is stale, but the canonical archive DB is intact and
// lexical search still works (cass's own "degraded-derived-assets" class). ntm
// must treat this as a healthy-with-note degradation, not a hard failure that
// surfaces a scary ⚠ in `ntm deps`.
func TestCASSAdapter_HealthStaleDerivedIndexIsDegradedNotUnhealthy(t *testing.T) {
	// Shape mirrors the real `cass status/health --json` on a stale index:
	// index exists + stale, DB exists + opened, the only error is "index stale".
	writeFakeCassHealthScript(t,
		`{"status":"unhealthy","healthy":false,"initialized":true,"errors":["index stale"],"recommended_action":"Run cass index --full only for stale derived search assets.","state":{"index":{"exists":true,"status":"stale","stale":true},"database":{"exists":true,"opened":true}}}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("Health() Healthy = false, want true for stale-derived-index degradation; message=%q", health.Message)
	}
	if !strings.Contains(health.Message, "degraded") {
		t.Fatalf("Health() message = %q, want a degraded note", health.Message)
	}
	if strings.Contains(health.Message, "reports unhealthy") {
		t.Fatalf("Health() message = %q, must not hard-fail a stale index as unhealthy", health.Message)
	}
}

// TestCASSAdapter_HealthMissingIndexStillUnhealthy ensures the stale-index
// downgrade does NOT mask a genuinely broken index. A status that mentions
// "stale" but whose structured state shows the index does not exist must stay
// hard-unhealthy.
func TestCASSAdapter_HealthMissingIndexStillUnhealthy(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"unhealthy","healthy":false,"initialized":true,"errors":["index stale"],"state":{"index":{"exists":false,"status":"missing","stale":true},"database":{"exists":true,"opened":true}}}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health() Healthy = true, want false when the index does not exist; message=%q", health.Message)
	}
	if !strings.Contains(health.Message, "cass reports unhealthy") {
		t.Fatalf("Health() message = %q, want hard unhealthy message", health.Message)
	}
}

// TestCASSAdapter_HealthDBOpenFailureStillUnhealthy ensures a stale advisory
// does not mask a database that failed to open (canonical archive at risk).
func TestCASSAdapter_HealthDBOpenFailureStillUnhealthy(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"unhealthy","healthy":false,"initialized":true,"errors":["index stale"],"state":{"index":{"exists":true,"status":"stale","stale":true},"database":{"exists":true,"opened":false}}}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health() Healthy = true, want false when the database failed to open; message=%q", health.Message)
	}
}

// TestCASSAdapter_HealthNonStaleErrorAlongsideStaleStillUnhealthy ensures that a
// genuine error (e.g. corruption) reported ALONGSIDE a stale advisory keeps cass
// classified as hard-unhealthy. Only a stale-ONLY blocker is downgraded.
func TestCASSAdapter_HealthNonStaleErrorAlongsideStaleStillUnhealthy(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"unhealthy","healthy":false,"initialized":true,"errors":["index stale","lexical index corrupt"],"state":{"index":{"exists":true,"status":"stale","stale":true},"database":{"exists":true,"opened":true}}}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health() Healthy = true, want false when a non-stale error is also present; message=%q", health.Message)
	}
	if !strings.Contains(health.Message, "lexical index corrupt") {
		t.Fatalf("Health() message = %q, want it to surface the corruption error", health.Message)
	}
}

// TestCASSAdapter_HealthStaleErrorWithoutStateStaysUnhealthy ensures we fail
// closed when cass omits the structured `state` block: without positive proof
// the index exists and the DB opened, a "stale" status is treated as unhealthy.
func TestCASSAdapter_HealthStaleErrorWithoutStateStaysUnhealthy(t *testing.T) {
	writeFakeCassHealthScript(t,
		`{"status":"unhealthy","healthy":false,"initialized":true,"errors":["index stale"]}`,
		"1",
	)

	health, err := NewCASSAdapter().Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health() Healthy = true, want false without a structured state block; message=%q", health.Message)
	}
}
