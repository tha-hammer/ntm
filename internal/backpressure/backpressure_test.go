package backpressure

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestEvaluateDeterministicAggregationAndReasonOrdering(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC) }
	inputs := []SurfaceInput{
		{
			Surface:       SurfaceWebSocket,
			Session:       "proj",
			Pane:          "2",
			QueueDepth:    240,
			QueueCapacity: 256,
			DroppedCount:  3,
			ClientLagMS:   3500,
			SourceLoaded:  true,
		},
		{
			Surface:      SurfaceTmuxCapture,
			Session:      "proj",
			Pane:         "1",
			LatencyMS:    1400,
			SourceLoaded: true,
		},
	}

	snap := Evaluate(inputs, SnapshotOptions{Now: now})

	requireEqual(t, snap.GeneratedAt, "2026-05-09T12:00:00Z")
	requireEqual(t, snap.Decision, DecisionDefer)
	wantReasons := []ReasonCode{
		ReasonQueueDepth,
		ReasonDroppedOutput,
		ReasonSlowCapture,
		ReasonClientLag,
	}
	requireEqual(t, snap.ReasonCodes, wantReasons)
	requireEqual(t, len(snap.Surfaces), 2)
	if !reflect.DeepEqual(snap.Surfaces[0].Surface, SurfaceTmuxCapture) {
		t.Fatalf("surfaces not sorted deterministically: %#v", snap.Surfaces)
	}
	requireEqual(t, snap.ErrorCode, "RESOURCE_BUSY")
	if snap.RetryAfterMS < 1 || len(snap.Hint) < 1 {
		t.Fatalf("missing retry/degrade hint fields: %#v", snap)
	}
	requireEqual(t, len(snap.LogRows), 2)
	requireEqual(t, snap.LogRows[1].QueueDepth, 240)
	requireEqual(t, snap.LogRows[1].DroppedCount, int64(3))
	requireEqual(t, snap.LogRows[1].LatencyMS, int64(0))
}

func TestEvaluateMissingCountersStayInspectable(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:        SurfaceREST,
			Session:        "proj",
			SourceLoaded:   false,
			MissingWarning: "REST handler queue metrics are not wired yet.",
		},
	}, SnapshotOptions{Now: fixedClock()})

	if !snap.Success {
		t.Fatalf("missing-only snapshot should not fail: %#v", snap)
	}
	if got, want := snap.ReasonCodes, []ReasonCode{ReasonMissingSource}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reason_codes = %#v, want %#v", got, want)
	}
	requireEqual(t, len(snap.Warnings), 1)
	requireEqual(t, snap.Warnings[0].Hint, "REST handler queue metrics are not wired yet.")
	if len(snap.Surfaces[0].ReasonCodes) < 1 {
		t.Fatal("surface reason_codes must be an empty-or-populated array, not omitted")
	}
}

func TestSlowTmuxCaptureGetsCoalescingRecommendations(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:      SurfaceTmuxCapture,
			Session:      "proj",
			Pane:         "%1",
			LatencyMS:    1500,
			SourceLoaded: true,
		},
	}, SnapshotOptions{Now: fixedClock()})

	requireEqual(t, snap.Decision, DecisionDefer)
	requireEqual(t, len(snap.CoalescingRecommendations), 2)
	requireEqual(t, snap.CoalescingRecommendations[0].Action, "coalesce_dashboard_refresh")
	requireEqual(t, snap.CoalescingRecommendations[1].Action, "coalesce_pane_output")
}

func TestSnapshotRobotAndDashboardSerialization(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:       SurfaceSSE,
			Session:       "proj",
			QueueDepth:    9,
			QueueCapacity: 10,
			DroppedCount:  1,
			SourceLoaded:  true,
		},
	}, SnapshotOptions{Now: fixedClock()})

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	for _, field := range []string{"success", "generated_at", "decision", "reason_codes", "surfaces", "warnings", "coalescing_recommendations", "load_shedding", "heatmap", "log_rows"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("snapshot JSON missing %q: %s", field, raw)
		}
	}

	dashboard := snap.Dashboard()
	requireEqual(t, dashboard.Decision, DecisionCoalesce)
	requireEqual(t, dashboard.SurfaceCount, 1)
	requireEqual(t, dashboard.WarningCount, 0)
	requireEqual(t, len(dashboard.RecommendedActions), 2)
	requireEqual(t, dashboard.HeatmapBuckets, 1)
}

func fixedClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 5, 9, 12, 30, 0, 0, time.UTC)
	}
}

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
