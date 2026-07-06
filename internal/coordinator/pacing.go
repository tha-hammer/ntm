package coordinator

import (
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/swarmslo"
)

// DispatchPacingSchemaVersion is the stable JSON contract for advisory send
// pacing decisions.
const DispatchPacingSchemaVersion = "ntm.dispatch_pacing.v1"

// DispatchPacingDecisionKind is the advisory action for a prompt dispatch.
type DispatchPacingDecisionKind string

const (
	DispatchPacingSendNow    DispatchPacingDecisionKind = "send_now"
	DispatchPacingStagger    DispatchPacingDecisionKind = "stagger"
	DispatchPacingDefer      DispatchPacingDecisionKind = "defer"
	DispatchPacingSplitBatch DispatchPacingDecisionKind = "split_batch"
)

// DispatchPacingReasonCode explains why a pacing decision was made.
type DispatchPacingReasonCode string

const (
	DispatchPacingReasonHealthy        DispatchPacingReasonCode = "dispatch.healthy"
	DispatchPacingReasonAckLatencyHigh DispatchPacingReasonCode = "dispatch.ack_latency.high_p95"
	DispatchPacingReasonAckPending     DispatchPacingReasonCode = "dispatch.ack.pending"
	DispatchPacingReasonMailMissing    DispatchPacingReasonCode = "dispatch.mail.missing"
	DispatchPacingReasonPressureHigh   DispatchPacingReasonCode = "dispatch.pressure.high"
	DispatchPacingReasonPressureCrit   DispatchPacingReasonCode = "dispatch.pressure.critical"
	DispatchPacingReasonUnhealthyPane  DispatchPacingReasonCode = "dispatch.pane.unhealthy"
	DispatchPacingReasonMixedAgents    DispatchPacingReasonCode = "dispatch.agent_type.mixed"
	DispatchPacingReasonLargeFanout    DispatchPacingReasonCode = "dispatch.fanout.large"
)

// DispatchPacingThresholds controls the advisory policy. Zero values are filled
// from DefaultDispatchPacingThresholds.
type DispatchPacingThresholds struct {
	AckP95Seconds      float64 `json:"ack_p95_seconds"`
	PendingAckCount    int     `json:"pending_ack_count"`
	LargeFanoutTargets int     `json:"large_fanout_targets"`
	SplitBatchSize     int     `json:"split_batch_size"`
	BaseStaggerMS      int     `json:"base_stagger_ms"`
	SlowAckStaggerMS   int     `json:"slow_ack_stagger_ms"`
	PressureStaggerMS  int     `json:"pressure_stagger_ms"`
	DeferRetryAfterMS  int     `json:"defer_retry_after_ms"`
}

// DefaultDispatchPacingThresholds returns conservative advisory defaults.
func DefaultDispatchPacingThresholds() DispatchPacingThresholds {
	return DispatchPacingThresholds{
		AckP95Seconds:      5 * 60,
		PendingAckCount:    0,
		LargeFanoutTargets: 8,
		SplitBatchSize:     4,
		BaseStaggerMS:      1000,
		SlowAckStaggerMS:   5000,
		PressureStaggerMS:  7500,
		DeferRetryAfterMS:  30000,
	}
}

// DispatchPaneHealth is the caller's health view for one target pane.
type DispatchPaneHealth struct {
	PaneIndex int    `json:"pane_index"`
	AgentType string `json:"agent_type,omitempty"`
	Healthy   bool   `json:"healthy"`
}

// DispatchPacingInput collects already-observed send health signals. The
// reducer is pure and never reads tmux, Beads, Agent Mail, or pressure sources.
type DispatchPacingInput struct {
	Session          string                   `json:"session,omitempty"`
	RequestedTargets int                      `json:"requested_targets"`
	PaneHealth       []DispatchPaneHealth     `json:"pane_health,omitempty"`
	AgentTypes       []string                 `json:"agent_types,omitempty"`
	SLO              swarmslo.Summary         `json:"slo,omitempty"`
	Pressure         pressure.RobotPressure   `json:"pressure,omitempty"`
	MissingSources   []string                 `json:"missing_sources,omitempty"`
	Thresholds       DispatchPacingThresholds `json:"thresholds,omitempty"`
}

// DispatchPacingDecision is the robot-readable advisory result.
type DispatchPacingDecision struct {
	SchemaVersion    string                     `json:"schema_version"`
	GeneratedAt      time.Time                  `json:"generated_at"`
	Session          string                     `json:"session,omitempty"`
	RequestedTargets int                        `json:"requested_targets"`
	AckP95Seconds    float64                    `json:"ack_p95_seconds"`
	PendingAckCount  int                        `json:"pending_ack_count"`
	PressureLevel    string                     `json:"pressure_level"`
	UnhealthyCount   int                        `json:"unhealthy_count"`
	Decision         DispatchPacingDecisionKind `json:"decision"`
	StaggerMS        int                        `json:"stagger_ms,omitempty"`
	SplitBatchSize   int                        `json:"split_batch_size,omitempty"`
	ReasonCodes      []DispatchPacingReasonCode `json:"reason_codes"`
	LogRows          []DispatchPacingLogRow     `json:"log_rows"`
}

// DispatchPacingLogRow is shaped for structured logs and robot snapshots.
type DispatchPacingLogRow struct {
	Session          string                     `json:"session,omitempty"`
	RequestedTargets int                        `json:"requested_targets"`
	AckP95Seconds    float64                    `json:"ack_p95_seconds"`
	PressureLevel    string                     `json:"pressure_level"`
	UnhealthyCount   int                        `json:"unhealthy_count"`
	Decision         DispatchPacingDecisionKind `json:"decision"`
	StaggerMS        int                        `json:"stagger_ms,omitempty"`
	ReasonCodes      []DispatchPacingReasonCode `json:"reason_codes"`
}

// EvaluateDispatchPacing returns an advisory-only dispatch pacing decision.
func EvaluateDispatchPacing(in DispatchPacingInput) DispatchPacingDecision {
	thresholds := normalizeDispatchPacingThresholds(in.Thresholds)
	targets := in.RequestedTargets
	if targets <= 0 && len(in.PaneHealth) > 0 {
		targets = len(in.PaneHealth)
	}
	if targets < 0 {
		targets = 0
	}

	agentTypes := dispatchAgentTypes(in.AgentTypes, in.PaneHealth)
	unhealthy := dispatchUnhealthyCount(in.PaneHealth)
	ack := in.SLO.TimeToFirstAck
	ackP95 := ack.P95Seconds
	pending := ack.Pending
	pressureLevel := dispatchPressureLevel(in.Pressure)

	reasons := make([]DispatchPacingReasonCode, 0, 6)
	if ack.Missing || dispatchHasMissingMail(in.MissingSources, in.SLO.Warnings) {
		reasons = append(reasons, DispatchPacingReasonMailMissing)
	}
	if ack.Count > 0 && ackP95 > thresholds.AckP95Seconds {
		reasons = append(reasons, DispatchPacingReasonAckLatencyHigh)
	}
	if pending > thresholds.PendingAckCount {
		reasons = append(reasons, DispatchPacingReasonAckPending)
	}
	if strings.Compare(pressureLevel, pressure.LevelCritical.String()) == 0 {
		reasons = append(reasons, DispatchPacingReasonPressureCrit)
	} else if strings.Compare(pressureLevel, pressure.LevelHigh.String()) == 0 {
		reasons = append(reasons, DispatchPacingReasonPressureHigh)
	}
	if unhealthy > 0 {
		reasons = append(reasons, DispatchPacingReasonUnhealthyPane)
	}
	if len(agentTypes) > 1 && targets > 1 {
		reasons = append(reasons, DispatchPacingReasonMixedAgents)
	}
	if targets >= thresholds.LargeFanoutTargets {
		reasons = append(reasons, DispatchPacingReasonLargeFanout)
	}
	reasons = uniqueDispatchReasons(reasons)

	decision := DispatchPacingSendNow
	staggerMS := 0
	splitBatchSize := 0
	if dispatchHasReason(reasons, DispatchPacingReasonPressureCrit) {
		decision = DispatchPacingDefer
		staggerMS = thresholds.DeferRetryAfterMS
	} else if dispatchHasStressReason(reasons) {
		if targets >= thresholds.LargeFanoutTargets {
			decision = DispatchPacingSplitBatch
			splitBatchSize = minPositiveInt(thresholds.SplitBatchSize, targets)
			staggerMS = dispatchStaggerMS(reasons, thresholds)
		} else {
			decision = DispatchPacingStagger
			staggerMS = dispatchStaggerMS(reasons, thresholds)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, DispatchPacingReasonHealthy)
	}

	out := DispatchPacingDecision{
		SchemaVersion:    DispatchPacingSchemaVersion,
		GeneratedAt:      time.Now().UTC(),
		Session:          strings.TrimSpace(in.Session),
		RequestedTargets: targets,
		AckP95Seconds:    ackP95,
		PendingAckCount:  pending,
		PressureLevel:    pressureLevel,
		UnhealthyCount:   unhealthy,
		Decision:         decision,
		StaggerMS:        staggerMS,
		SplitBatchSize:   splitBatchSize,
		ReasonCodes:      reasons,
	}
	out.LogRows = []DispatchPacingLogRow{{
		Session:          out.Session,
		RequestedTargets: out.RequestedTargets,
		AckP95Seconds:    out.AckP95Seconds,
		PressureLevel:    out.PressureLevel,
		UnhealthyCount:   out.UnhealthyCount,
		Decision:         out.Decision,
		StaggerMS:        out.StaggerMS,
		ReasonCodes:      append([]DispatchPacingReasonCode(nil), out.ReasonCodes...),
	}}
	return out
}

func normalizeDispatchPacingThresholds(in DispatchPacingThresholds) DispatchPacingThresholds {
	def := DefaultDispatchPacingThresholds()
	if in.AckP95Seconds > 0 {
		def.AckP95Seconds = in.AckP95Seconds
	}
	if in.PendingAckCount >= 0 {
		def.PendingAckCount = in.PendingAckCount
	}
	if in.LargeFanoutTargets > 0 {
		def.LargeFanoutTargets = in.LargeFanoutTargets
	}
	if in.SplitBatchSize > 0 {
		def.SplitBatchSize = in.SplitBatchSize
	}
	if in.BaseStaggerMS > 0 {
		def.BaseStaggerMS = in.BaseStaggerMS
	}
	if in.SlowAckStaggerMS > 0 {
		def.SlowAckStaggerMS = in.SlowAckStaggerMS
	}
	if in.PressureStaggerMS > 0 {
		def.PressureStaggerMS = in.PressureStaggerMS
	}
	if in.DeferRetryAfterMS > 0 {
		def.DeferRetryAfterMS = in.DeferRetryAfterMS
	}
	return def
}

func dispatchAgentTypes(explicit []string, health []DispatchPaneHealth) []string {
	types := make([]string, 0, len(explicit)+len(health))
	for _, raw := range explicit {
		agentType := strings.TrimSpace(strings.ToLower(raw))
		if strings.Compare(agentType, "") != 0 {
			types = append(types, agentType)
		}
	}
	for _, pane := range health {
		agentType := strings.TrimSpace(strings.ToLower(pane.AgentType))
		if strings.Compare(agentType, "") != 0 {
			types = append(types, agentType)
		}
	}
	return uniqueSortedStrings(types)
}

func dispatchUnhealthyCount(health []DispatchPaneHealth) int {
	count := 0
	for _, pane := range health {
		if !pane.Healthy {
			count++
		}
	}
	return count
}

func dispatchPressureLevel(snapshot pressure.RobotPressure) string {
	level := strings.TrimSpace(strings.ToLower(snapshot.Overall))
	if strings.Compare(level, "") == 0 {
		return "unknown"
	}
	return level
}

func dispatchHasMissingMail(sources, warnings []string) bool {
	for _, raw := range sources {
		source := strings.TrimSpace(strings.ToLower(raw))
		if strings.Compare(source, "mail") == 0 ||
			strings.Compare(source, "agentmail") == 0 ||
			strings.Compare(source, "agent_mail") == 0 {
			return true
		}
	}
	for _, raw := range warnings {
		warning := strings.ToLower(raw)
		if strings.Contains(warning, "agentmail") ||
			strings.Contains(warning, "agent mail") ||
			strings.Contains(warning, "mail unavailable") {
			return true
		}
	}
	return false
}

func dispatchHasStressReason(reasons []DispatchPacingReasonCode) bool {
	for _, reason := range reasons {
		switch reason {
		case DispatchPacingReasonAckLatencyHigh,
			DispatchPacingReasonAckPending,
			DispatchPacingReasonPressureHigh,
			DispatchPacingReasonPressureCrit,
			DispatchPacingReasonUnhealthyPane,
			DispatchPacingReasonMixedAgents,
			DispatchPacingReasonLargeFanout:
			return true
		}
	}
	return false
}

func dispatchStaggerMS(reasons []DispatchPacingReasonCode, thresholds DispatchPacingThresholds) int {
	stagger := thresholds.BaseStaggerMS
	if dispatchHasReason(reasons, DispatchPacingReasonAckLatencyHigh) ||
		dispatchHasReason(reasons, DispatchPacingReasonAckPending) {
		stagger = maxInt(stagger, thresholds.SlowAckStaggerMS)
	}
	if dispatchHasReason(reasons, DispatchPacingReasonPressureHigh) {
		stagger = maxInt(stagger, thresholds.PressureStaggerMS)
	}
	return stagger
}

func dispatchHasReason(reasons []DispatchPacingReasonCode, want DispatchPacingReasonCode) bool {
	for _, reason := range reasons {
		if strings.Compare(string(reason), string(want)) == 0 {
			return true
		}
	}
	return false
}

func uniqueDispatchReasons(in []DispatchPacingReasonCode) []DispatchPacingReasonCode {
	seen := make(map[DispatchPacingReasonCode]struct{}, len(in))
	out := make([]DispatchPacingReasonCode, 0, len(in))
	for _, reason := range in {
		if strings.Compare(string(reason), "") == 0 {
			continue
		}
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		out = append(out, reason)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(string(out[i]), string(out[j])) < 0
	})
	return out
}

func minPositiveInt(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func uniqueSortedStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if strings.Compare(value, "") == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
