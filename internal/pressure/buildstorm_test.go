package pressure

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEvaluateBuildStormNoRCHAdmitsWithLocalHeadroom(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:             fixedClock()(),
		RCHAvailable:    false,
		LocalBuildCount: 2,
		MaxLocalBuilds:  8,
		RequestedBuilds: 1,
	})

	if strings.Compare(string(report.Decision), string(BuildStormAdmit)) != 0 {
		t.Fatalf("Decision = %s, want admit", report.Decision)
	}
	if strings.Compare(report.Reason, "rch_unavailable_local_headroom") != 0 {
		t.Fatalf("Reason = %q, want rch_unavailable_local_headroom", report.Reason)
	}
	if report.WorkerCount != 0 || report.BusyWorkers != 0 || report.QueueDepth != 0 {
		t.Fatalf("worker fields = %d/%d/%d, want zeros", report.WorkerCount, report.BusyWorkers, report.QueueDepth)
	}
}

func TestEvaluateBuildStormAllWorkersBusyDefers(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:             fixedClock()(),
		RCHAvailable:    true,
		RCHCompatible:   true,
		WorkerCount:     4,
		HealthyWorkers:  4,
		BusyWorkers:     4,
		QueueDepth:      1,
		LocalBuildCount: 0,
	})

	if strings.Compare(string(report.Decision), string(BuildStormDefer)) != 0 {
		t.Fatalf("Decision = %s, want defer", report.Decision)
	}
	if strings.Compare(report.Reason, "rch_workers_busy") != 0 {
		t.Fatalf("Reason = %q, want rch_workers_busy", report.Reason)
	}
	if strings.Compare(report.RetryAfter, "30s") != 0 {
		t.Fatalf("RetryAfter = %q, want 30s", report.RetryAfter)
	}
}

func TestEvaluateBuildStormInsufficientRemoteHeadroomDefers(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:             fixedClock()(),
		RCHAvailable:    true,
		RCHCompatible:   true,
		WorkerCount:     4,
		HealthyWorkers:  4,
		BusyWorkers:     2,
		QueueDepth:      0,
		LocalBuildCount: 1,
		MaxLocalBuilds:  8,
		RequestedBuilds: 3, // only 2 remote slots are currently free
	})

	if strings.Compare(string(report.Decision), string(BuildStormDefer)) != 0 {
		t.Fatalf("Decision = %s, want defer", report.Decision)
	}
	if strings.Compare(report.Reason, "rch_headroom_insufficient") != 0 {
		t.Fatalf("Reason = %q, want rch_headroom_insufficient", report.Reason)
	}
	if strings.Compare(report.RetryAfter, "20s") != 0 {
		t.Fatalf("RetryAfter = %q, want 20s", report.RetryAfter)
	}
	if report.RemoteHeadroom != 2 {
		t.Fatalf("RemoteHeadroom = %d, want 2", report.RemoteHeadroom)
	}
}

func TestEvaluateBuildStormLocalFallbackRiskOffloadsWhenRemoteReady(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:             fixedClock()(),
		RCHAvailable:    true,
		RCHCompatible:   true,
		WorkerCount:     3,
		HealthyWorkers:  3,
		BusyWorkers:     1,
		LocalBuildCount: 8,
		MaxLocalBuilds:  8,
		RequestedBuilds: 1,
	})

	if strings.Compare(string(report.Decision), string(BuildStormOffload)) != 0 {
		t.Fatalf("Decision = %s, want offload", report.Decision)
	}
	if strings.Compare(report.Reason, "local_fallback_risk") != 0 {
		t.Fatalf("Reason = %q, want local_fallback_risk", report.Reason)
	}
	if report.RemoteHeadroom != 2 || report.LocalHeadroom != 0 {
		t.Fatalf("headroom = remote %d local %d, want 2/0", report.RemoteHeadroom, report.LocalHeadroom)
	}
}

func TestEvaluateBuildStormLocalFallbackRiskSerializesWithoutRCH(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:             fixedClock()(),
		RCHAvailable:    false,
		LocalBuildCount: 8,
		MaxLocalBuilds:  8,
		RequestedBuilds: 1,
	})

	if strings.Compare(string(report.Decision), string(BuildStormSerialize)) != 0 {
		t.Fatalf("Decision = %s, want serialize", report.Decision)
	}
	if strings.Compare(report.Reason, "local_fallback_risk") != 0 {
		t.Fatalf("Reason = %q, want local_fallback_risk", report.Reason)
	}
	if strings.Compare(report.RetryAfter, "30s") != 0 {
		t.Fatalf("RetryAfter = %q, want 30s", report.RetryAfter)
	}
}

func TestEvaluateBuildStormSessionFairnessSerializes(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:                  fixedClock()(),
		Session:              "proj",
		RCHAvailable:         true,
		RCHCompatible:        true,
		WorkerCount:          6,
		HealthyWorkers:       6,
		BusyWorkers:          0,
		SessionRunningBuilds: 2,
		MaxBuildsPerSession:  2,
		RequestedBuilds:      1,
	})

	if strings.Compare(string(report.Decision), string(BuildStormSerialize)) != 0 {
		t.Fatalf("Decision = %s, want serialize", report.Decision)
	}
	if strings.Compare(report.Reason, "session_build_limit") != 0 {
		t.Fatalf("Reason = %q, want session_build_limit", report.Reason)
	}
	if strings.Compare(report.Session, "proj") != 0 {
		t.Fatalf("Session = %q, want proj", report.Session)
	}
}

func TestEvaluateBuildStormStructuredJSON(t *testing.T) {
	t.Parallel()

	report := EvaluateBuildStorm(BuildStormInput{
		Now:             time.Date(2026, 5, 9, 8, 0, 0, 0, time.UTC),
		RCHAvailable:    true,
		RCHCompatible:   true,
		WorkerCount:     2,
		HealthyWorkers:  2,
		BusyWorkers:     1,
		QueueDepth:      2,
		LocalBuildCount: 3,
	})

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, want := range []string{
		`"worker_count":2`,
		`"busy_workers":1`,
		`"queue_depth":2`,
		`"local_build_count":3`,
		`"decision":"defer"`,
		`"reason":"rch_workers_busy"`,
		`"retry_after":"30s"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("report JSON missing %s: %s", want, data)
		}
	}
}
