package backpressure

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Surface names the subsystem reporting overload evidence.
type Surface string

const (
	SurfaceTmuxCapture Surface = "tmux_capture"
	SurfaceRobot       Surface = "robot_command"
	SurfaceREST        Surface = "rest_handler"
	SurfaceSSE         Surface = "sse_stream"
	SurfaceWebSocket   Surface = "websocket"
	SurfaceProfiler    Surface = "profiler"
)

// ReasonCode is the stable machine-readable overload taxonomy.
type ReasonCode string

const (
	ReasonQueueDepth    ReasonCode = "queue_depth"
	ReasonDroppedOutput ReasonCode = "dropped_output"
	ReasonSlowCapture   ReasonCode = "slow_capture"
	ReasonSlowHandler   ReasonCode = "slow_handler"
	ReasonClientLag     ReasonCode = "client_lag"
	ReasonMissingSource ReasonCode = "missing_source"
)

// Decision is the recommended runtime response to the observed pressure.
type Decision string

const (
	DecisionOK       Decision = "ok"
	DecisionCoalesce Decision = "coalesce"
	DecisionDefer    Decision = "defer"
	DecisionDegrade  Decision = "degrade"
)

// Thresholds controls when raw counters become reason codes.
type Thresholds struct {
	QueueDepthWarn        int     `json:"queue_depth_warn"`
	QueueDepthCritical    int     `json:"queue_depth_critical"`
	QueueUtilizationWarn  float64 `json:"queue_utilization_warn"`
	QueueUtilizationCrit  float64 `json:"queue_utilization_critical"`
	DroppedWarn           int64   `json:"dropped_warn"`
	DroppedCritical       int64   `json:"dropped_critical"`
	LatencyWarnMS         int64   `json:"latency_warn_ms"`
	LatencyCriticalMS     int64   `json:"latency_critical_ms"`
	ClientLagWarnMS       int64   `json:"client_lag_warn_ms"`
	ClientLagCriticalMS   int64   `json:"client_lag_critical_ms"`
	CoalesceRetryAfterMS  int64   `json:"coalesce_retry_after_ms"`
	DeferRetryAfterMS     int64   `json:"defer_retry_after_ms"`
	DegradeRetryAfterMS   int64   `json:"degrade_retry_after_ms"`
	DashboardRefreshMinMS int64   `json:"dashboard_refresh_min_ms"`
	PaneOutputMaxLines    int     `json:"pane_output_max_lines"`
}

// DefaultThresholds returns conservative thresholds for local swarm overload.
func DefaultThresholds() Thresholds {
	return Thresholds{
		QueueDepthWarn:        128,
		QueueDepthCritical:    512,
		QueueUtilizationWarn:  0.80,
		QueueUtilizationCrit:  0.95,
		DroppedWarn:           1,
		DroppedCritical:       100,
		LatencyWarnMS:         1000,
		LatencyCriticalMS:     5000,
		ClientLagWarnMS:       2000,
		ClientLagCriticalMS:   10000,
		CoalesceRetryAfterMS:  1000,
		DeferRetryAfterMS:     2000,
		DegradeRetryAfterMS:   5000,
		DashboardRefreshMinMS: 2000,
		PaneOutputMaxLines:    50,
	}
}

// SnapshotOptions configures snapshot evaluation.
type SnapshotOptions struct {
	Now        func() time.Time
	Thresholds Thresholds
}

// SurfaceInput is the raw evidence captured from one overload surface.
type SurfaceInput struct {
	Surface        Surface
	Session        string
	Pane           string
	Command        string
	QueueDepth     int
	QueueCapacity  int
	DroppedCount   int64
	LatencyMS      int64
	ClientLagMS    int64
	SourceLoaded   bool
	MissingWarning string
}

// SurfaceSnapshot is one normalized, robot-readable overload row.
type SurfaceSnapshot struct {
	Surface       Surface      `json:"surface"`
	Session       string       `json:"session,omitempty"`
	Pane          string       `json:"pane,omitempty"`
	Command       string       `json:"command,omitempty"`
	QueueDepth    int          `json:"queue_depth"`
	QueueCapacity int          `json:"queue_capacity,omitempty"`
	DroppedCount  int64        `json:"dropped_count"`
	LatencyMS     int64        `json:"latency_ms"`
	ClientLagMS   int64        `json:"client_lag_ms,omitempty"`
	Decision      Decision     `json:"decision"`
	ReasonCodes   []ReasonCode `json:"reason_codes"`
	RetryAfterMS  int64        `json:"retry_after_ms,omitempty"`
	Hint          string       `json:"hint,omitempty"`
}

// MissingSourceWarning records a surface whose counters are not wired yet.
type MissingSourceWarning struct {
	Surface Surface `json:"surface"`
	Session string  `json:"session,omitempty"`
	Pane    string  `json:"pane,omitempty"`
	Hint    string  `json:"hint"`
}

// CoalescingRecommendation describes a low-cost way to reduce pressure.
type CoalescingRecommendation struct {
	ID                  string  `json:"id"`
	Surface             Surface `json:"surface"`
	Action              string  `json:"action"`
	Reason              string  `json:"reason"`
	SuggestedIntervalMS int64   `json:"suggested_interval_ms,omitempty"`
	MaxLines            int     `json:"max_lines,omitempty"`
	Hint                string  `json:"hint"`
}

// LogRow is intentionally shaped like structured log fields for overload events.
type LogRow struct {
	Session      string       `json:"session,omitempty"`
	Pane         string       `json:"pane,omitempty"`
	Surface      Surface      `json:"surface"`
	QueueDepth   int          `json:"queue_depth"`
	DroppedCount int64        `json:"dropped_count"`
	LatencyMS    int64        `json:"latency_ms"`
	Decision     Decision     `json:"decision"`
	ReasonCodes  []ReasonCode `json:"reason_codes"`
}

// BackpressureSnapshot is the stable robot/dashboard payload for overload state.
type BackpressureSnapshot struct {
	Success                   bool                       `json:"success"`
	GeneratedAt               string                     `json:"generated_at"`
	Decision                  Decision                   `json:"decision"`
	ErrorCode                 string                     `json:"error_code,omitempty"`
	RetryAfterMS              int64                      `json:"retry_after_ms,omitempty"`
	Hint                      string                     `json:"hint,omitempty"`
	ReasonCodes               []ReasonCode               `json:"reason_codes"`
	Surfaces                  []SurfaceSnapshot          `json:"surfaces"`
	Warnings                  []MissingSourceWarning     `json:"warnings"`
	CoalescingRecommendations []CoalescingRecommendation `json:"coalescing_recommendations"`
	LoadShedding              LoadSheddingContract       `json:"load_shedding"`
	Heatmap                   OperatorHeatmap            `json:"heatmap"`
	LogRows                   []LogRow                   `json:"log_rows"`
}

// DashboardSummary is a compact dashboard-friendly projection.
type DashboardSummary struct {
	Decision           Decision     `json:"decision"`
	ReasonCodes        []ReasonCode `json:"reason_codes"`
	SurfaceCount       int          `json:"surface_count"`
	WarningCount       int          `json:"warning_count"`
	RecommendedActions []string     `json:"recommended_actions"`
	HeatmapBuckets     int          `json:"heatmap_buckets"`
}

// Evaluate normalizes raw surface inputs into a deterministic snapshot.
func Evaluate(inputs []SurfaceInput, opts SnapshotOptions) BackpressureSnapshot {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	thresholds := normalizeThresholds(opts.Thresholds)

	surfaces := make([]SurfaceSnapshot, 0, len(inputs))
	warnings := make([]MissingSourceWarning, 0)
	logRows := make([]LogRow, 0, len(inputs))

	overall := DecisionOK
	allReasons := make([]ReasonCode, 0)
	for _, in := range inputs {
		surface := evaluateSurface(in, thresholds)
		surfaces = append(surfaces, surface)
		allReasons = append(allReasons, surface.ReasonCodes...)
		overall = maxDecision(overall, surface.Decision)
		if !in.SourceLoaded {
			warnings = append(warnings, MissingSourceWarning{
				Surface: surface.Surface,
				Session: surface.Session,
				Pane:    surface.Pane,
				Hint:    missingHint(in),
			})
		}
		logRows = append(logRows, LogRow{
			Session:      surface.Session,
			Pane:         surface.Pane,
			Surface:      surface.Surface,
			QueueDepth:   surface.QueueDepth,
			DroppedCount: surface.DroppedCount,
			LatencyMS:    surface.LatencyMS,
			Decision:     surface.Decision,
			ReasonCodes:  append([]ReasonCode(nil), surface.ReasonCodes...),
		})
	}

	sort.SliceStable(surfaces, func(i, j int) bool {
		return surfaceSortKey(surfaces[i]) < surfaceSortKey(surfaces[j])
	})
	sort.SliceStable(warnings, func(i, j int) bool {
		return warningSortKey(warnings[i]) < warningSortKey(warnings[j])
	})
	sort.SliceStable(logRows, func(i, j int) bool {
		return logSortKey(logRows[i]) < logSortKey(logRows[j])
	})

	reasons := sortedUniqueReasons(allReasons)
	recommendations := buildRecommendations(surfaces, thresholds)
	retryAfter := retryAfterMS(overall, thresholds)
	hint := overallHint(overall, retryAfter)

	snapshot := BackpressureSnapshot{
		Success:                   overall == DecisionOK || overall == DecisionCoalesce,
		GeneratedAt:               now().UTC().Format(time.RFC3339Nano),
		Decision:                  overall,
		RetryAfterMS:              retryAfter,
		Hint:                      hint,
		ReasonCodes:               reasons,
		Surfaces:                  nonNilSurfaces(surfaces),
		Warnings:                  nonNilWarnings(warnings),
		CoalescingRecommendations: nonNilRecommendations(recommendations),
		LogRows:                   nonNilLogRows(logRows),
	}
	if overall == DecisionDefer || overall == DecisionDegrade {
		snapshot.ErrorCode = "RESOURCE_BUSY"
	}
	snapshot.LoadShedding = snapshot.LoadSheddingContract()
	snapshot.Heatmap = snapshot.OperatorHeatmap()
	return snapshot
}

// Dashboard returns a compact projection suitable for dashboard refreshes.
func (s BackpressureSnapshot) Dashboard() DashboardSummary {
	actions := make([]string, 0, len(s.CoalescingRecommendations))
	for _, rec := range s.CoalescingRecommendations {
		actions = append(actions, rec.Action)
	}
	sort.Strings(actions)
	heatmap := s.Heatmap
	if len(heatmap.Buckets) == 0 && !heatmap.Empty {
		heatmap = s.OperatorHeatmap()
	}
	return DashboardSummary{
		Decision:           s.Decision,
		ReasonCodes:        append([]ReasonCode(nil), s.ReasonCodes...),
		SurfaceCount:       len(s.Surfaces),
		WarningCount:       len(s.Warnings),
		RecommendedActions: actions,
		HeatmapBuckets:     len(heatmap.Buckets),
	}
}

func evaluateSurface(in SurfaceInput, thresholds Thresholds) SurfaceSnapshot {
	in = normalizeInput(in)
	reasons := make([]ReasonCode, 0, 3)
	decision := DecisionOK
	critical := false

	if !in.SourceLoaded {
		reasons = append(reasons, ReasonMissingSource)
	}
	if queueWarn(in, thresholds) {
		reasons = append(reasons, ReasonQueueDepth)
		decision = maxDecision(decision, DecisionCoalesce)
		critical = critical || queueCritical(in, thresholds)
	}
	if in.DroppedCount >= thresholds.DroppedWarn {
		reasons = append(reasons, ReasonDroppedOutput)
		decision = maxDecision(decision, DecisionCoalesce)
		critical = critical || in.DroppedCount >= thresholds.DroppedCritical
	}
	if in.LatencyMS >= thresholds.LatencyWarnMS {
		switch in.Surface {
		case SurfaceTmuxCapture:
			reasons = append(reasons, ReasonSlowCapture)
		default:
			reasons = append(reasons, ReasonSlowHandler)
		}
		decision = maxDecision(decision, DecisionDefer)
		critical = critical || in.LatencyMS >= thresholds.LatencyCriticalMS
	}
	if in.ClientLagMS >= thresholds.ClientLagWarnMS {
		reasons = append(reasons, ReasonClientLag)
		decision = maxDecision(decision, DecisionCoalesce)
		critical = critical || in.ClientLagMS >= thresholds.ClientLagCriticalMS
	}
	if critical {
		decision = DecisionDegrade
	}
	reasons = sortedUniqueReasons(reasons)
	return SurfaceSnapshot{
		Surface:       in.Surface,
		Session:       in.Session,
		Pane:          in.Pane,
		Command:       in.Command,
		QueueDepth:    in.QueueDepth,
		QueueCapacity: in.QueueCapacity,
		DroppedCount:  in.DroppedCount,
		LatencyMS:     in.LatencyMS,
		ClientLagMS:   in.ClientLagMS,
		Decision:      decision,
		ReasonCodes:   nonNilReasonCodes(reasons),
		RetryAfterMS:  retryAfterMS(decision, thresholds),
		Hint:          surfaceHint(decision),
	}
}

func normalizeThresholds(in Thresholds) Thresholds {
	def := DefaultThresholds()
	if in.QueueDepthWarn > 0 {
		def.QueueDepthWarn = in.QueueDepthWarn
	}
	if in.QueueDepthCritical > 0 {
		def.QueueDepthCritical = in.QueueDepthCritical
	}
	if in.QueueUtilizationWarn > 0 {
		def.QueueUtilizationWarn = in.QueueUtilizationWarn
	}
	if in.QueueUtilizationCrit > 0 {
		def.QueueUtilizationCrit = in.QueueUtilizationCrit
	}
	if in.DroppedWarn > 0 {
		def.DroppedWarn = in.DroppedWarn
	}
	if in.DroppedCritical > 0 {
		def.DroppedCritical = in.DroppedCritical
	}
	if in.LatencyWarnMS > 0 {
		def.LatencyWarnMS = in.LatencyWarnMS
	}
	if in.LatencyCriticalMS > 0 {
		def.LatencyCriticalMS = in.LatencyCriticalMS
	}
	if in.ClientLagWarnMS > 0 {
		def.ClientLagWarnMS = in.ClientLagWarnMS
	}
	if in.ClientLagCriticalMS > 0 {
		def.ClientLagCriticalMS = in.ClientLagCriticalMS
	}
	if in.CoalesceRetryAfterMS > 0 {
		def.CoalesceRetryAfterMS = in.CoalesceRetryAfterMS
	}
	if in.DeferRetryAfterMS > 0 {
		def.DeferRetryAfterMS = in.DeferRetryAfterMS
	}
	if in.DegradeRetryAfterMS > 0 {
		def.DegradeRetryAfterMS = in.DegradeRetryAfterMS
	}
	if in.DashboardRefreshMinMS > 0 {
		def.DashboardRefreshMinMS = in.DashboardRefreshMinMS
	}
	if in.PaneOutputMaxLines > 0 {
		def.PaneOutputMaxLines = in.PaneOutputMaxLines
	}
	return def
}

func normalizeInput(in SurfaceInput) SurfaceInput {
	if in.Surface == "" {
		in.Surface = SurfaceProfiler
	}
	in.Session = strings.TrimSpace(in.Session)
	in.Pane = strings.TrimSpace(in.Pane)
	in.Command = strings.TrimSpace(in.Command)
	in.QueueDepth = clampInt(in.QueueDepth)
	in.QueueCapacity = clampInt(in.QueueCapacity)
	in.DroppedCount = clampInt64(in.DroppedCount)
	in.LatencyMS = clampInt64(in.LatencyMS)
	in.ClientLagMS = clampInt64(in.ClientLagMS)
	return in
}

func queueWarn(in SurfaceInput, thresholds Thresholds) bool {
	if in.QueueDepth >= thresholds.QueueDepthWarn {
		return true
	}
	if in.QueueCapacity <= 0 {
		return false
	}
	return float64(in.QueueDepth)/float64(in.QueueCapacity) >= thresholds.QueueUtilizationWarn
}

func queueCritical(in SurfaceInput, thresholds Thresholds) bool {
	if in.QueueDepth >= thresholds.QueueDepthCritical {
		return true
	}
	if in.QueueCapacity <= 0 {
		return false
	}
	return float64(in.QueueDepth)/float64(in.QueueCapacity) >= thresholds.QueueUtilizationCrit
}

func maxDecision(a, b Decision) Decision {
	if decisionRank(b) > decisionRank(a) {
		return b
	}
	return a
}

func decisionRank(d Decision) int {
	switch d {
	case DecisionCoalesce:
		return 1
	case DecisionDefer:
		return 2
	case DecisionDegrade:
		return 3
	default:
		return 0
	}
}

func retryAfterMS(d Decision, thresholds Thresholds) int64 {
	switch d {
	case DecisionCoalesce:
		return thresholds.CoalesceRetryAfterMS
	case DecisionDefer:
		return thresholds.DeferRetryAfterMS
	case DecisionDegrade:
		return thresholds.DegradeRetryAfterMS
	default:
		return 0
	}
}

func overallHint(d Decision, retryAfter int64) string {
	switch d {
	case DecisionCoalesce:
		return "Coalesce pane output and dashboard refreshes before polling again."
	case DecisionDefer:
		return retryHint("Defer non-essential robot commands", retryAfter)
	case DecisionDegrade:
		return retryHint("Degrade to terse robot status and avoid high-line captures", retryAfter)
	default:
		return ""
	}
}

func surfaceHint(d Decision) string {
	switch d {
	case DecisionCoalesce:
		return "Reduce refresh frequency or coalesce output events."
	case DecisionDefer:
		return "Retry after the suggested delay or use a cheaper robot surface."
	case DecisionDegrade:
		return "Use terse status and skip expensive captures until pressure clears."
	default:
		return ""
	}
}

func retryHint(prefix string, retryAfter int64) string {
	if retryAfter <= 0 {
		return prefix + "."
	}
	return prefix + "; retry after " + strconv.FormatInt(retryAfter, 10) + "ms."
}

func missingHint(in SurfaceInput) string {
	if strings.TrimSpace(in.MissingWarning) != "" {
		return strings.TrimSpace(in.MissingWarning)
	}
	return "Backpressure counters are not available for this surface."
}

func sortedUniqueReasons(in []ReasonCode) []ReasonCode {
	seen := make(map[ReasonCode]struct{}, len(in))
	out := make([]ReasonCode, 0, len(in))
	for _, r := range in {
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oki := reasonOrder(out[i])
		oj, okj := reasonOrder(out[j])
		if oki && okj {
			return oi < oj
		}
		if oki != okj {
			return oki
		}
		return out[i] < out[j]
	})
	return out
}

func reasonOrder(r ReasonCode) (int, bool) {
	switch r {
	case ReasonQueueDepth:
		return 0, true
	case ReasonDroppedOutput:
		return 1, true
	case ReasonSlowCapture:
		return 2, true
	case ReasonSlowHandler:
		return 3, true
	case ReasonClientLag:
		return 4, true
	case ReasonMissingSource:
		return 5, true
	default:
		return 0, false
	}
}

func buildRecommendations(surfaces []SurfaceSnapshot, thresholds Thresholds) []CoalescingRecommendation {
	recs := make(map[string]CoalescingRecommendation)
	for _, surface := range surfaces {
		if surface.Decision == DecisionOK {
			continue
		}
		if surface.Surface == SurfaceTmuxCapture || hasAnyReason(surface.ReasonCodes, ReasonQueueDepth, ReasonDroppedOutput, ReasonSlowCapture, ReasonClientLag) {
			recs["pane_output"] = CoalescingRecommendation{
				ID:       "pane_output",
				Surface:  surface.Surface,
				Action:   "coalesce_pane_output",
				Reason:   "pane output pressure is present",
				MaxLines: thresholds.PaneOutputMaxLines,
				Hint:     "Prefer status or health line budgets and reuse capture broker output.",
			}
		}
		if surface.Surface == SurfaceTmuxCapture || surface.Surface == SurfaceREST || surface.Surface == SurfaceSSE || surface.Surface == SurfaceWebSocket || surface.Surface == SurfaceRobot {
			recs["dashboard_refresh"] = CoalescingRecommendation{
				ID:                  "dashboard_refresh",
				Surface:             surface.Surface,
				Action:              "coalesce_dashboard_refresh",
				Reason:              "dashboard or transport pressure is present",
				SuggestedIntervalMS: thresholds.DashboardRefreshMinMS,
				Hint:                "Batch dashboard refreshes and skip unchanged sections while pressure is active.",
			}
		}
	}
	out := make([]CoalescingRecommendation, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func hasAnyReason(haystack []ReasonCode, needles ...ReasonCode) bool {
	for _, h := range haystack {
		for _, n := range needles {
			if h == n {
				return true
			}
		}
	}
	return false
}

func surfaceSortKey(s SurfaceSnapshot) string {
	return string(s.Surface) + "\x00" + s.Session + "\x00" + s.Pane + "\x00" + s.Command
}

func warningSortKey(w MissingSourceWarning) string {
	return string(w.Surface) + "\x00" + w.Session + "\x00" + w.Pane
}

func logSortKey(l LogRow) string {
	return string(l.Surface) + "\x00" + l.Session + "\x00" + l.Pane
}

func clampInt(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func clampInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func nonNilReasonCodes(in []ReasonCode) []ReasonCode {
	if in == nil {
		return []ReasonCode{}
	}
	return in
}

func nonNilSurfaces(in []SurfaceSnapshot) []SurfaceSnapshot {
	if in == nil {
		return []SurfaceSnapshot{}
	}
	return in
}

func nonNilWarnings(in []MissingSourceWarning) []MissingSourceWarning {
	if in == nil {
		return []MissingSourceWarning{}
	}
	return in
}

func nonNilRecommendations(in []CoalescingRecommendation) []CoalescingRecommendation {
	if in == nil {
		return []CoalescingRecommendation{}
	}
	return in
}

func nonNilLogRows(in []LogRow) []LogRow {
	if in == nil {
		return []LogRow{}
	}
	return in
}
