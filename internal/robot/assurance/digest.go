package assurance

import (
	"sort"
	"strings"
	"time"
)

// DigestStatus is the rolled-up operator-visible state. It is the
// single field a dashboard can colour-code without reading any of
// the per-section detail.
type DigestStatus string

const (
	// DigestStatusHealthy means no critical or warning findings are
	// outstanding and the swarm is at a safe quiet point.
	DigestStatusHealthy DigestStatus = "healthy"

	// DigestStatusDegraded means at least one warning condition or
	// degraded provider exists, but no hard blocker. The operator
	// can proceed cautiously.
	DigestStatusDegraded DigestStatus = "degraded"

	// DigestStatusUnsafe means a critical condition exists that
	// should block commit/handoff/shutdown until resolved.
	DigestStatusUnsafe DigestStatus = "unsafe"
)

// DigestSeverity rolls all findings up into one priority token. The
// strings sort lexicographically ascending for stable JSON output:
// "critical" < "info" < "ok" < "warning" alphabetically, so we use
// a numeric rank for ordering and emit the strings as-is.
type DigestSeverity string

const (
	DigestSeverityCritical DigestSeverity = "critical"
	DigestSeverityWarning  DigestSeverity = "warning"
	DigestSeverityInfo     DigestSeverity = "info"
	DigestSeverityOK       DigestSeverity = "ok"
)

// DigestFinding is one stripped-down finding the digest evaluator
// folds into the rollup. Callers convert from richer per-source
// types (commitlint.Finding, identityhygiene.Finding, ...) by
// mapping their severity to the four-value DigestSeverity scale and
// providing a stable Code + Source.
type DigestFinding struct {
	Code     string         `json:"code"`
	Severity DigestSeverity `json:"severity"`
	Source   string         `json:"source"` // "commit_readiness" | "identity_hygiene" | "slo" | "evidence_budget" | ...
	Hint     string         `json:"hint,omitempty"`
}

// DigestSLO captures the SLO summary in the most compact way
// the digest cares about. The full distribution lives elsewhere;
// the digest only needs a healthy/unhealthy bit + optional notes.
type DigestSLO struct {
	Healthy bool     `json:"healthy"`
	Notes   []string `json:"notes,omitempty"`
}

// DigestInput is the full set of evidence ComputeDigest reduces.
type DigestInput struct {
	// Quiescence is the existing per-swarm quiescence assessment.
	// Its zero value (empty State, zero Signal) maps to "unknown",
	// which the digest treats as a non-blocker.
	Quiescence QuiescenceAssessment

	// CoordinationFindings folds in every commitlint / identity
	// hygiene / per-surface finding the caller could collect. The
	// digest only inspects Severity + Source + Hint; full text
	// remains in the per-source detail of the snapshot.
	CoordinationFindings []DigestFinding

	// SLO is a compact health bit. Zero-value (Healthy=false,
	// Notes=nil) is interpreted as "unknown SLO" and does not
	// trigger a degraded status by itself; callers should set
	// Healthy=true explicitly when a snapshot was taken.
	SLO DigestSLO

	// DegradedSources lists provider names that returned anything
	// other than a healthy result (slow, unavailable, malformed,
	// stale, partial). One non-empty entry triggers degraded status
	// even with no findings.
	DegradedSources []string

	// Now lets tests pin the wall clock. Defaults to time.Now().
	Now time.Time
}

// Digest is the rolled-up operator-visible summary. It is small on
// purpose so a TUI tile and a robot JSON consumer can both render
// it without inspecting every raw section.
type Digest struct {
	GeneratedAt         time.Time       `json:"generated_at"`
	Status              DigestStatus    `json:"status"`
	HighestSeverity     DigestSeverity  `json:"highest_severity"`
	Quiescence          QuiescenceState `json:"quiescence,omitempty"`
	DegradedSources     []string        `json:"degraded_sources,omitempty"`
	ReasonCodes         []ReasonCode    `json:"reason_codes,omitempty"`
	SuggestedNextAction string          `json:"suggested_next_action"`
	Summary             string          `json:"summary"`
	Counts              DigestCounts    `json:"counts"`
}

// DigestCounts breaks down findings by severity so a dashboard tile
// can show "3 critical / 1 warning / 0 info" without re-traversing
// the per-source list.
type DigestCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// ComputeDigest reduces DigestInput to a Digest. Pure: no I/O, no
// state mutation. Output JSON is byte-stable across calls because
// every list it produces is sorted deterministically.
func ComputeDigest(in DigestInput) Digest {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	out := Digest{
		GeneratedAt: now.UTC(),
		Quiescence:  in.Quiescence.State,
	}

	// Filter and dedupe DegradedSources up front so the status
	// decision below and the serialized DegradedSources field use the
	// SAME cleaned view. Pre-fix the status switch read the raw input
	// while the output ran it through uniqueSortedDigest, so a
	// caller passing [""] could flip status to degraded while the
	// JSON had no surfaced source (bd-6e26q).
	cleanedDegradedSources := uniqueSortedDigest(in.DegradedSources)

	// Tally findings.
	for _, f := range in.CoordinationFindings {
		switch f.Severity {
		case DigestSeverityCritical:
			out.Counts.Critical++
		case DigestSeverityWarning:
			out.Counts.Warning++
		case DigestSeverityInfo:
			out.Counts.Info++
		}
	}

	// Decide highest severity / status.
	switch {
	case out.Counts.Critical > 0 || in.Quiescence.State == QuiescenceUnsafeToStandDown:
		out.HighestSeverity = DigestSeverityCritical
		out.Status = DigestStatusUnsafe
	case out.Counts.Warning > 0 || len(cleanedDegradedSources) > 0 ||
		in.Quiescence.State == QuiescenceBlockedByPeer ||
		in.Quiescence.State == QuiescenceBlockedBySelf ||
		in.Quiescence.State == QuiescenceSaturatedReviewLoop ||
		(sloUnhealthy(in.SLO)):
		out.HighestSeverity = DigestSeverityWarning
		out.Status = DigestStatusDegraded
	case out.Counts.Info > 0:
		out.HighestSeverity = DigestSeverityInfo
		out.Status = DigestStatusHealthy
	default:
		out.HighestSeverity = DigestSeverityOK
		out.Status = DigestStatusHealthy
	}

	// Reason codes: pull from quiescence + synthesize one per non-OK
	// source. Keep them sorted + deduped so JSON is stable.
	codeSet := make(map[ReasonCode]struct{})
	for _, c := range in.Quiescence.ReasonCodes {
		codeSet[c] = struct{}{}
	}
	for _, f := range in.CoordinationFindings {
		if f.Source == "" || f.Code == "" {
			continue
		}
		codeSet[ReasonCode("digest."+f.Source+"."+f.Code)] = struct{}{}
	}
	for _, s := range cleanedDegradedSources {
		codeSet[ReasonCode("digest.source_degraded."+strings.ToLower(s))] = struct{}{}
	}
	if sloUnhealthy(in.SLO) {
		codeSet[ReasonCode("digest.slo.unhealthy")] = struct{}{}
	}
	out.ReasonCodes = sortedReasonCodes(codeSet)

	// Degraded source list: reuse the cleaned slice computed up front
	// so status and serialized output stay in lock-step.
	if len(cleanedDegradedSources) > 0 {
		out.DegradedSources = cleanedDegradedSources
	}

	out.SuggestedNextAction = chooseDigestNextAction(in, out)
	out.Summary = composeDigestSummary(in, out)

	return out
}

// chooseDigestNextAction picks the most actionable hint available.
// Priority: a quiescence assessment's existing suggested_next_action
// (already calibrated for the tracker/work state); else the first
// critical-finding hint; else the first warning-finding hint; else a
// degraded-source nudge; else "stand down".
func chooseDigestNextAction(in DigestInput, out Digest) string {
	if hint := strings.TrimSpace(in.Quiescence.SuggestedNextAction); hint != "" {
		// Honor existing quiescence guidance unless a critical
		// finding outranks it.
		if out.HighestSeverity != DigestSeverityCritical || in.Quiescence.State == QuiescenceUnsafeToStandDown {
			return hint
		}
	}
	if hint := firstHint(in.CoordinationFindings, DigestSeverityCritical); hint != "" {
		return hint
	}
	if hint := firstHint(in.CoordinationFindings, DigestSeverityWarning); hint != "" {
		return hint
	}
	if len(out.DegradedSources) > 0 {
		return "investigate degraded providers: " + strings.Join(out.DegradedSources, ",")
	}
	if sloUnhealthy(in.SLO) {
		return "investigate SLO regressions"
	}
	if hint := firstHint(in.CoordinationFindings, DigestSeverityInfo); hint != "" {
		return hint
	}
	return "stand down — no operator-visible work or hygiene blockers"
}

func composeDigestSummary(in DigestInput, out Digest) string {
	parts := []string{string(out.Status)}
	if out.Counts.Critical > 0 {
		parts = append(parts, "critical="+itoa(out.Counts.Critical))
	}
	if out.Counts.Warning > 0 {
		parts = append(parts, "warning="+itoa(out.Counts.Warning))
	}
	if out.Counts.Info > 0 {
		parts = append(parts, "info="+itoa(out.Counts.Info))
	}
	if len(out.DegradedSources) > 0 {
		parts = append(parts, "degraded_sources="+strings.Join(out.DegradedSources, ","))
	}
	if out.Quiescence != "" {
		parts = append(parts, "quiescence="+string(out.Quiescence))
	}
	return strings.Join(parts, " ")
}

func firstHint(findings []DigestFinding, sev DigestSeverity) string {
	for _, f := range findings {
		if f.Severity != sev {
			continue
		}
		h := strings.TrimSpace(f.Hint)
		if h != "" {
			return h
		}
	}
	return ""
}

func sortedReasonCodes(set map[ReasonCode]struct{}) []ReasonCode {
	if len(set) == 0 {
		return nil
	}
	out := make([]ReasonCode, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uniqueSortedDigest(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sloUnhealthy reports whether DigestSLO indicates an unhealthy
// state. The DigestSLO zero value (Healthy=false, Notes=nil) is
// treated as "not provided" — only an explicitly-set Healthy=false
// with at least one Note (or any other distinguishing field set
// later) qualifies. We treat the empty struct as "no data" so a
// caller that omits SLO does not accidentally trip degraded.
func sloUnhealthy(s DigestSLO) bool {
	if s.Healthy {
		return false
	}
	// Healthy=false alone is ambiguous (could be the zero value).
	// Require at least one Note to be sure the caller meant to flag
	// SLO as unhealthy.
	return len(s.Notes) > 0
}

// itoa is an inline strconv replacement to keep this file's imports
// minimal. Supports the small range we need for finding counts.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
