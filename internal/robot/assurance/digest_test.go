package assurance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedDigestClock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func TestComputeDigest_EmptyInputsAreHealthyOK(t *testing.T) {
	t.Parallel()
	d := ComputeDigest(DigestInput{Now: fixedDigestClock()})
	if d.Status != DigestStatusHealthy {
		t.Errorf("Status = %s, want healthy", d.Status)
	}
	if d.HighestSeverity != DigestSeverityOK {
		t.Errorf("HighestSeverity = %s, want ok", d.HighestSeverity)
	}
	if d.Counts.Critical != 0 || d.Counts.Warning != 0 || d.Counts.Info != 0 {
		t.Errorf("Counts = %+v, want zeros", d.Counts)
	}
	if d.SuggestedNextAction == "" {
		t.Error("SuggestedNextAction empty; want a sensible default for empty inputs")
	}
}

func TestComputeDigest_HealthyWithExplicitSLO(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now:        fixedDigestClock(),
		SLO:        DigestSLO{Healthy: true},
		Quiescence: QuiescenceAssessment{State: QuiescenceQueueDry, SafeToStandDown: true},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusHealthy {
		t.Errorf("Status = %s, want healthy", d.Status)
	}
	if d.HighestSeverity != DigestSeverityOK {
		t.Errorf("HighestSeverity = %s, want ok", d.HighestSeverity)
	}
	if d.Quiescence != QuiescenceQueueDry {
		t.Errorf("Quiescence = %s, want queue_dry", d.Quiescence)
	}
}

func TestComputeDigest_DegradedSourcesAlone(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now:             fixedDigestClock(),
		SLO:             DigestSLO{Healthy: true},
		DegradedSources: []string{"mail", "bv", "mail"}, // dedupe
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusDegraded {
		t.Errorf("Status = %s, want degraded", d.Status)
	}
	if d.HighestSeverity != DigestSeverityWarning {
		t.Errorf("HighestSeverity = %s, want warning", d.HighestSeverity)
	}
	if !equalStringSlice(d.DegradedSources, []string{"bv", "mail"}) {
		t.Errorf("DegradedSources = %v, want sorted+deduped [bv mail]", d.DegradedSources)
	}
	hasMail := false
	hasBV := false
	for _, c := range d.ReasonCodes {
		if c == "digest.source_degraded.mail" {
			hasMail = true
		}
		if c == "digest.source_degraded.bv" {
			hasBV = true
		}
	}
	if !hasMail || !hasBV {
		t.Errorf("ReasonCodes missing degraded markers: %v", d.ReasonCodes)
	}
}

// bd-6e26q: status and serialized DegradedSources must agree. A
// caller passing only empty/whitespace strings must NOT flip the
// status to degraded; both views see the same cleaned slice.
func TestComputeDigest_EmptyOnlyDegradedSourcesDoesNotFlipStatus(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now:             fixedDigestClock(),
		SLO:             DigestSLO{Healthy: true},
		DegradedSources: []string{"", "  ", "\t"},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusHealthy {
		t.Errorf("Status = %s, want healthy (empty-only DegradedSources must not flip status)", d.Status)
	}
	if len(d.DegradedSources) != 0 {
		t.Errorf("DegradedSources = %v, want nil/empty", d.DegradedSources)
	}
	for _, c := range d.ReasonCodes {
		if strings.HasPrefix(string(c), "digest.source_degraded.") {
			t.Errorf("ReasonCodes leaked source_degraded entry from empty input: %q", c)
		}
	}
}

// Companion: a mixed input with one real entry plus empties must
// surface ONLY the real entry — and the status reflects the cleaned
// view, not the raw input length.
func TestComputeDigest_MixedDegradedSourcesUsesCleanedView(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now:             fixedDigestClock(),
		SLO:             DigestSLO{Healthy: true},
		DegradedSources: []string{"", "mail", "  "},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusDegraded {
		t.Errorf("Status = %s, want degraded (one real source after cleaning)", d.Status)
	}
	if !equalStringSlice(d.DegradedSources, []string{"mail"}) {
		t.Errorf("DegradedSources = %v, want [mail]", d.DegradedSources)
	}
}

func TestComputeDigest_CriticalFindingForcesUnsafe(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now: fixedDigestClock(),
		SLO: DigestSLO{Healthy: true},
		CoordinationFindings: []DigestFinding{
			{Code: "foreign_reservation", Severity: DigestSeverityCritical, Source: "commit_readiness", Hint: "negotiate handoff before committing"},
		},
		Quiescence: QuiescenceAssessment{State: QuiescenceQueueDry, SafeToStandDown: true, SuggestedNextAction: "stand down — quiescence says safe"},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusUnsafe {
		t.Errorf("Status = %s, want unsafe", d.Status)
	}
	if d.HighestSeverity != DigestSeverityCritical {
		t.Errorf("HighestSeverity = %s, want critical", d.HighestSeverity)
	}
	if d.Counts.Critical != 1 {
		t.Errorf("Counts.Critical = %d, want 1", d.Counts.Critical)
	}
	// Critical finding's hint must outrank quiescence's safe message.
	if !strings.Contains(d.SuggestedNextAction, "negotiate handoff") {
		t.Errorf("SuggestedNextAction = %q, want critical-finding hint", d.SuggestedNextAction)
	}
}

func TestComputeDigest_QuiescenceUnsafeForcesUnsafeEvenWithoutFindings(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now: fixedDigestClock(),
		Quiescence: QuiescenceAssessment{
			State:               QuiescenceUnsafeToStandDown,
			SafeToStandDown:     false,
			SuggestedNextAction: "resolve pending work before standing down",
			ReasonCodes:         []ReasonCode{ReasonQuiescenceInProgressWork},
		},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusUnsafe {
		t.Errorf("Status = %s, want unsafe", d.Status)
	}
	if !contains(d.ReasonCodes, ReasonQuiescenceInProgressWork) {
		t.Errorf("ReasonCodes missing quiescence reason: %v", d.ReasonCodes)
	}
	if !strings.Contains(d.SuggestedNextAction, "resolve pending work") {
		t.Errorf("SuggestedNextAction = %q, want quiescence guidance preserved", d.SuggestedNextAction)
	}
}

// bd-u068s: QuiescenceBlockedBySelf must trigger DigestStatusDegraded
// (the same as BlockedByPeer) so a digest consumer treating both
// blocker-states uniformly gets the same severity rollup.
func TestComputeDigest_QuiescenceBlockedBySelfIsDegraded(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now: fixedDigestClock(),
		Quiescence: QuiescenceAssessment{
			State:           QuiescenceBlockedBySelf,
			SafeToStandDown: false,
			ReasonCodes:     []ReasonCode{ReasonQuiescenceInProgressWork},
		},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusDegraded {
		t.Errorf("Status = %s, want degraded for BlockedBySelf", d.Status)
	}
	if d.HighestSeverity != DigestSeverityWarning {
		t.Errorf("HighestSeverity = %s, want warning", d.HighestSeverity)
	}
}

func TestComputeDigest_WarningFindingsAreDegradedNotUnsafe(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now: fixedDigestClock(),
		SLO: DigestSLO{Healthy: true},
		CoordinationFindings: []DigestFinding{
			{Code: "missing_reservation", Severity: DigestSeverityWarning, Source: "commit_readiness", Hint: "file_reservation_paths before committing"},
			{Code: "stale_identity", Severity: DigestSeverityWarning, Source: "identity_hygiene"},
		},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusDegraded {
		t.Errorf("Status = %s, want degraded", d.Status)
	}
	if d.Counts.Warning != 2 {
		t.Errorf("Counts.Warning = %d, want 2", d.Counts.Warning)
	}
	if !strings.Contains(d.SuggestedNextAction, "file_reservation_paths") {
		t.Errorf("SuggestedNextAction = %q, want first warning's hint", d.SuggestedNextAction)
	}
}

func TestComputeDigest_SLOUnhealthyTriggersDegraded(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now: fixedDigestClock(),
		SLO: DigestSLO{Healthy: false, Notes: []string{"p95 ack latency above target"}},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusDegraded {
		t.Errorf("Status = %s, want degraded", d.Status)
	}
	if !contains(d.ReasonCodes, "digest.slo.unhealthy") {
		t.Errorf("ReasonCodes missing slo.unhealthy: %v", d.ReasonCodes)
	}
}

func TestComputeDigest_InfoOnlyIsHealthyButNoticed(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now: fixedDigestClock(),
		SLO: DigestSLO{Healthy: true},
		CoordinationFindings: []DigestFinding{
			{Code: "dirty_tree_no_identity", Severity: DigestSeverityInfo, Source: "commit_readiness"},
		},
	}
	d := ComputeDigest(in)
	if d.Status != DigestStatusHealthy {
		t.Errorf("Status = %s, want healthy", d.Status)
	}
	if d.HighestSeverity != DigestSeverityInfo {
		t.Errorf("HighestSeverity = %s, want info", d.HighestSeverity)
	}
	if d.Counts.Info != 1 {
		t.Errorf("Counts.Info = %d, want 1", d.Counts.Info)
	}
}

func TestComputeDigest_ReasonCodesAreSortedAndDeduped(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now:             fixedDigestClock(),
		DegradedSources: []string{"mail", "BV", "Mail"}, // case-folding via tolower
		CoordinationFindings: []DigestFinding{
			{Code: "x", Severity: DigestSeverityWarning, Source: "src_a"},
			{Code: "x", Severity: DigestSeverityWarning, Source: "src_a"}, // dup
			{Code: "y", Severity: DigestSeverityWarning, Source: "src_b"},
		},
		Quiescence: QuiescenceAssessment{ReasonCodes: []ReasonCode{ReasonReservationPathConflict, ReasonReservationPathConflict}},
	}
	d := ComputeDigest(in)
	for i := 1; i < len(d.ReasonCodes); i++ {
		if d.ReasonCodes[i-1] >= d.ReasonCodes[i] {
			t.Errorf("ReasonCodes not strictly sorted ascending: %v", d.ReasonCodes)
			break
		}
	}
}

func TestComputeDigest_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := DigestInput{
		Now:             fixedDigestClock(),
		SLO:             DigestSLO{Healthy: false},
		DegradedSources: []string{"mail"},
		CoordinationFindings: []DigestFinding{
			{Code: "warn1", Severity: DigestSeverityWarning, Source: "src", Hint: "do thing"},
		},
	}
	a, err := json.Marshal(ComputeDigest(in))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	b, err := json.Marshal(ComputeDigest(in))
	if err != nil {
		t.Fatalf("Marshal twice: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("Digest JSON drifted between Compute calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{`"status":"degraded"`, `"highest_severity":"warning"`, `"degraded_sources"`, `"reason_codes"`, `"suggested_next_action"`, `"summary"`} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
}

func TestComputeDigest_DefaultActionWhenEverythingClean(t *testing.T) {
	t.Parallel()
	d := ComputeDigest(DigestInput{
		Now: fixedDigestClock(),
		SLO: DigestSLO{Healthy: true},
		Quiescence: QuiescenceAssessment{
			State:               QuiescenceQueueDry,
			SafeToStandDown:     true,
			SuggestedNextAction: "stand down — quiescence says safe",
		},
	})
	if !strings.Contains(d.SuggestedNextAction, "stand down") {
		t.Errorf("SuggestedNextAction = %q, want quiescence default", d.SuggestedNextAction)
	}
}

func contains(codes []ReasonCode, want ReasonCode) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
