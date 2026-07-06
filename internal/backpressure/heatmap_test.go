package backpressure

import "testing"

func TestLoadSheddingContractSerializesResourceBusy(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:       SurfaceSSE,
			Session:       "proj",
			QueueDepth:    600,
			QueueCapacity: 256,
			DroppedCount:  120,
			SourceLoaded:  true,
		},
	}, SnapshotOptions{Now: fixedClock()})

	contract := snap.LoadSheddingContract()

	requireEqual(t, contract.SchemaVersion, LoadSheddingSchemaVersion)
	requireEqual(t, contract.Decision, DecisionDegrade)
	requireEqual(t, contract.ErrorCode, "RESOURCE_BUSY")
	requireEqual(t, contract.DegradedSurface, SurfaceSSE)
	requireEqual(t, contract.RecommendedAction, "degrade_to_terse_status")
	if contract.RetryAfterMS <= 0 {
		t.Fatalf("RetryAfterMS = %d, want positive retry hint", contract.RetryAfterMS)
	}
	if len(contract.ReasonCodes) == 0 {
		t.Fatalf("ReasonCodes empty: %+v", contract)
	}
}

func TestLoadSheddingContractRetryHintForDefer(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:      SurfaceTmuxCapture,
			Session:      "proj",
			Pane:         "%1",
			LatencyMS:    1500,
			SourceLoaded: true,
		},
	}, SnapshotOptions{Now: fixedClock()})

	contract := snap.LoadSheddingContract()

	requireEqual(t, contract.Decision, DecisionDefer)
	requireEqual(t, contract.ErrorCode, "RESOURCE_BUSY")
	requireEqual(t, contract.RecommendedAction, "retry_after")
	requireEqual(t, contract.RetryAfterMS, DefaultThresholds().DeferRetryAfterMS)
}

func TestOperatorHeatmapOrderingAndLogFields(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:       SurfaceWebSocket,
			Session:       "proj",
			Pane:          "%2",
			QueueDepth:    9,
			QueueCapacity: 10,
			SourceLoaded:  true,
		},
		{
			Surface:      SurfaceRobot,
			Session:      "proj",
			Command:      "tail",
			LatencyMS:    1500,
			SourceLoaded: true,
		},
		{
			Surface:       SurfaceSSE,
			Session:       "proj",
			QueueDepth:    600,
			QueueCapacity: 256,
			DroppedCount:  120,
			SourceLoaded:  true,
		},
	}, SnapshotOptions{Now: fixedClock()})

	heatmap := snap.OperatorHeatmap()

	requireEqual(t, heatmap.SchemaVersion, LoadSheddingSchemaVersion)
	requireEqual(t, heatmap.Empty, false)
	requireEqual(t, len(heatmap.Buckets), 3)
	requireEqual(t, heatmap.Buckets[0].Severity, "critical")
	requireEqual(t, heatmap.Buckets[0].HeatmapBucket, "degraded")
	requireEqual(t, heatmap.Buckets[1].Severity, "warning")
	requireEqual(t, heatmap.Buckets[2].Severity, "watch")
	requireEqual(t, len(heatmap.LogRows), 3)

	row := heatmap.LogRows[0]
	if heatmapValueEqual(row.Surface, Surface("")) ||
		heatmapValueEqual(row.Session, "") ||
		heatmapValueEqual(row.Severity, "") ||
		heatmapValueEqual(row.HeatmapBucket, "") ||
		len(row.ReasonCodes) == 0 {
		t.Fatalf("log row missing required fields: %+v", row)
	}
}

func TestOperatorHeatmapMissingSourceBucket(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:        SurfaceREST,
			Session:        "proj",
			SourceLoaded:   false,
			MissingWarning: "REST queue metrics unavailable",
		},
	}, SnapshotOptions{Now: fixedClock()})

	heatmap := snap.OperatorHeatmap()

	requireEqual(t, heatmap.Empty, false)
	requireEqual(t, len(heatmap.Buckets), 1)
	requireEqual(t, heatmap.Buckets[0].HeatmapBucket, "missing_source")
	requireEqual(t, heatmap.Buckets[0].Severity, "unknown")
	requireEqual(t, heatmap.Buckets[0].RecommendedAction, "continue")
}

func TestOperatorHeatmapNoOverloadEmptyState(t *testing.T) {
	snap := Evaluate([]SurfaceInput{
		{
			Surface:      SurfaceRobot,
			Session:      "proj",
			Command:      "status",
			QueueDepth:   1,
			SourceLoaded: true,
		},
	}, SnapshotOptions{Now: fixedClock()})

	heatmap := snap.OperatorHeatmap()
	contract := snap.LoadSheddingContract()

	requireEqual(t, heatmap.Empty, true)
	requireEqual(t, len(heatmap.Buckets), 0)
	requireEqual(t, len(heatmap.LogRows), 0)
	requireEqual(t, contract.ErrorCode, "")
	requireEqual(t, contract.RecommendedAction, "continue")
}
