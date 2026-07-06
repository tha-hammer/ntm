package coordinator

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/swarmslo"
)

func TestEvaluateDispatchPacingHealthySwarmSendsNow(t *testing.T) {
	t.Parallel()
	got := EvaluateDispatchPacing(healthyDispatchPacingInput())

	requirePacingDecision(t, got, DispatchPacingSendNow)
	if got.StaggerMS > 0 {
		t.Fatalf("StaggerMS = %d, want 0", got.StaggerMS)
	}
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonHealthy) {
		t.Fatalf("ReasonCodes = %v, want healthy", got.ReasonCodes)
	}
}

func TestEvaluateDispatchPacingSlowAckStaggers(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.SLO.TimeToFirstAck = swarmslo.Distribution{Count: 4, P95Seconds: 900}

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingStagger)
	if got.StaggerMS < DefaultDispatchPacingThresholds().SlowAckStaggerMS {
		t.Fatalf("StaggerMS = %d, want slow-ack stagger", got.StaggerMS)
	}
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonAckLatencyHigh) {
		t.Fatalf("ReasonCodes = %v, want slow ack reason", got.ReasonCodes)
	}
}

func TestEvaluateDispatchPacingHighPressureStaggers(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.Pressure = pressure.RobotPressure{Overall: pressure.LevelHigh.String()}

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingStagger)
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonPressureHigh) {
		t.Fatalf("ReasonCodes = %v, want pressure high", got.ReasonCodes)
	}
	row := got.LogRows[0]
	if strings.Compare(row.Session, "proj") != 0 ||
		row.RequestedTargets < 3 || row.RequestedTargets > 3 ||
		strings.Compare(row.PressureLevel, "high") != 0 ||
		strings.Compare(string(row.Decision), string(DispatchPacingStagger)) != 0 {
		t.Fatalf("log row missing pacing fields: %+v", row)
	}
}

func TestEvaluateDispatchPacingUnhealthyPanesStaggers(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.PaneHealth[1].Healthy = false

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingStagger)
	if got.UnhealthyCount < 1 || got.UnhealthyCount > 1 {
		t.Fatalf("UnhealthyCount = %d, want 1", got.UnhealthyCount)
	}
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonUnhealthyPane) {
		t.Fatalf("ReasonCodes = %v, want unhealthy pane", got.ReasonCodes)
	}
}

func TestEvaluateDispatchPacingMixedAgentTypesStaggers(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.PaneHealth[1].AgentType = "cod"

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingStagger)
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonMixedAgents) {
		t.Fatalf("ReasonCodes = %v, want mixed agent type", got.ReasonCodes)
	}
}

func TestEvaluateDispatchPacingMissingMailDataStaysAdvisory(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.SLO.TimeToFirstAck = swarmslo.Distribution{Missing: true}

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingSendNow)
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonMailMissing) {
		t.Fatalf("ReasonCodes = %v, want missing mail", got.ReasonCodes)
	}
}

func TestEvaluateDispatchPacingLargeFanoutSplitsBatch(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.RequestedTargets = 9
	in.PaneHealth = dispatchPacingHealth(9, "cc")

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingSplitBatch)
	if got.SplitBatchSize < 4 || got.SplitBatchSize > 4 {
		t.Fatalf("SplitBatchSize = %d, want 4", got.SplitBatchSize)
	}
	if got.StaggerMS < DefaultDispatchPacingThresholds().BaseStaggerMS ||
		got.StaggerMS > DefaultDispatchPacingThresholds().BaseStaggerMS {
		t.Fatalf("StaggerMS = %d, want base stagger", got.StaggerMS)
	}
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonLargeFanout) {
		t.Fatalf("ReasonCodes = %v, want large fanout", got.ReasonCodes)
	}
}

func TestEvaluateDispatchPacingLargeFanoutWithPendingAcksSplitsBatch(t *testing.T) {
	t.Parallel()
	in := healthyDispatchPacingInput()
	in.RequestedTargets = 12
	in.PaneHealth = dispatchPacingHealth(12, "cc")
	in.SLO.TimeToFirstAck = swarmslo.Distribution{Pending: 2}

	got := EvaluateDispatchPacing(in)

	requirePacingDecision(t, got, DispatchPacingSplitBatch)
	if got.SplitBatchSize < 4 || got.SplitBatchSize > 4 {
		t.Fatalf("SplitBatchSize = %d, want 4", got.SplitBatchSize)
	}
	if !containsPacingReason(got.ReasonCodes, DispatchPacingReasonLargeFanout) ||
		!containsPacingReason(got.ReasonCodes, DispatchPacingReasonAckPending) {
		t.Fatalf("ReasonCodes = %v, want large fanout + pending ack", got.ReasonCodes)
	}
}

func dispatchPacingHealth(count int, agentType string) []DispatchPaneHealth {
	health := make([]DispatchPaneHealth, 0, count)
	for index := 1; index <= count; index++ {
		health = append(health, DispatchPaneHealth{PaneIndex: index, AgentType: agentType, Healthy: true})
	}
	return health
}

func healthyDispatchPacingInput() DispatchPacingInput {
	return DispatchPacingInput{
		Session:          "proj",
		RequestedTargets: 3,
		PaneHealth: []DispatchPaneHealth{
			{PaneIndex: 1, AgentType: "cc", Healthy: true},
			{PaneIndex: 2, AgentType: "cc", Healthy: true},
			{PaneIndex: 3, AgentType: "cc", Healthy: true},
		},
		SLO: swarmslo.Summary{
			TimeToFirstAck: swarmslo.Distribution{Count: 5, P95Seconds: 30},
		},
		Pressure: pressure.RobotPressure{Overall: pressure.LevelNormal.String()},
	}
}

func requirePacingDecision(t *testing.T, got DispatchPacingDecision, want DispatchPacingDecisionKind) {
	t.Helper()
	if strings.Compare(got.SchemaVersion, DispatchPacingSchemaVersion) != 0 {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, DispatchPacingSchemaVersion)
	}
	if strings.Compare(string(got.Decision), string(want)) != 0 {
		t.Fatalf("Decision = %s, want %s; reasons=%v", got.Decision, want, got.ReasonCodes)
	}
	if len(got.LogRows) < 1 || len(got.LogRows) > 1 {
		t.Fatalf("LogRows = %d, want 1", len(got.LogRows))
	}
}

func containsPacingReason(reasons []DispatchPacingReasonCode, want DispatchPacingReasonCode) bool {
	for _, reason := range reasons {
		if strings.Compare(string(reason), string(want)) == 0 {
			return true
		}
	}
	return false
}
