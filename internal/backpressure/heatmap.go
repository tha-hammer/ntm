package backpressure

import "sort"

const (
	// LoadSheddingSchemaVersion is the stable JSON schema token for overload
	// contracts and operator heatmaps.
	LoadSheddingSchemaVersion = "ntm.load_shedding.v1"
)

// LoadSheddingContract is the compact robot contract for overload handling.
type LoadSheddingContract struct {
	SchemaVersion     string       `json:"schema_version"`
	Decision          Decision     `json:"decision"`
	ErrorCode         string       `json:"error_code,omitempty"`
	RetryAfterMS      int64        `json:"retry_after_ms,omitempty"`
	DegradedSurface   Surface      `json:"degraded_surface,omitempty"`
	RecommendedAction string       `json:"recommended_action"`
	ReasonCodes       []ReasonCode `json:"reason_codes"`
}

// OperatorHeatmap is a dashboard-friendly overload map. Buckets are sorted by
// operator urgency and remain empty when no overload exists.
type OperatorHeatmap struct {
	SchemaVersion string                  `json:"schema_version"`
	Decision      Decision                `json:"decision"`
	Empty         bool                    `json:"empty"`
	Buckets       []OperatorHeatmapBucket `json:"buckets"`
	LogRows       []OperatorHeatmapLogRow `json:"log_rows"`
}

// OperatorHeatmapBucket is one inspectable overloaded or degraded surface.
type OperatorHeatmapBucket struct {
	HeatmapBucket     string       `json:"heatmap_bucket"`
	Surface           Surface      `json:"surface"`
	Session           string       `json:"session,omitempty"`
	Pane              string       `json:"pane,omitempty"`
	Severity          string       `json:"severity"`
	Score             int          `json:"score"`
	RetryAfterMS      int64        `json:"retry_after_ms,omitempty"`
	ReasonCodes       []ReasonCode `json:"reason_codes"`
	RecommendedAction string       `json:"recommended_action"`
}

// OperatorHeatmapLogRow carries stable fields for dashboard/debug artifacts.
type OperatorHeatmapLogRow struct {
	Surface       Surface      `json:"surface"`
	Session       string       `json:"session,omitempty"`
	Pane          string       `json:"pane,omitempty"`
	Severity      string       `json:"severity"`
	RetryAfterMS  int64        `json:"retry_after_ms,omitempty"`
	ReasonCodes   []ReasonCode `json:"reason_codes"`
	HeatmapBucket string       `json:"heatmap_bucket"`
}

// LoadSheddingContract returns the stable robot-facing overload contract for a
// snapshot. It is a projection only; callers still choose whether to enforce it.
func (s BackpressureSnapshot) LoadSheddingContract() LoadSheddingContract {
	worst := worstSurface(s.Surfaces)
	contract := LoadSheddingContract{
		SchemaVersion:     LoadSheddingSchemaVersion,
		Decision:          s.Decision,
		RetryAfterMS:      s.RetryAfterMS,
		DegradedSurface:   worst.Surface,
		RecommendedAction: recommendedActionForDecision(s.Decision),
		ReasonCodes:       append([]ReasonCode(nil), s.ReasonCodes...),
	}
	if heatmapValueEqual(s.Decision, DecisionDefer) || heatmapValueEqual(s.Decision, DecisionDegrade) {
		contract.ErrorCode = "RESOURCE_BUSY"
	}
	return contract
}

// OperatorHeatmap returns the stable dashboard heatmap projection for a snapshot.
func (s BackpressureSnapshot) OperatorHeatmap() OperatorHeatmap {
	out := OperatorHeatmap{
		SchemaVersion: LoadSheddingSchemaVersion,
		Decision:      s.Decision,
		Buckets:       []OperatorHeatmapBucket{},
		LogRows:       []OperatorHeatmapLogRow{},
	}

	for _, surface := range s.Surfaces {
		bucket, ok := heatmapBucketFromSurface(surface)
		if !ok {
			continue
		}
		out.Buckets = append(out.Buckets, bucket)
		out.LogRows = append(out.LogRows, OperatorHeatmapLogRow{
			Surface:       bucket.Surface,
			Session:       bucket.Session,
			Pane:          bucket.Pane,
			Severity:      bucket.Severity,
			RetryAfterMS:  bucket.RetryAfterMS,
			ReasonCodes:   append([]ReasonCode(nil), bucket.ReasonCodes...),
			HeatmapBucket: bucket.HeatmapBucket,
		})
	}

	sort.SliceStable(out.Buckets, func(i, j int) bool {
		if out.Buckets[i].Score != out.Buckets[j].Score {
			return out.Buckets[i].Score > out.Buckets[j].Score
		}
		return heatmapSortKey(out.Buckets[i]) < heatmapSortKey(out.Buckets[j])
	})
	sort.SliceStable(out.LogRows, func(i, j int) bool {
		if heatmapSeverityScore(out.LogRows[i].Severity) != heatmapSeverityScore(out.LogRows[j].Severity) {
			return heatmapSeverityScore(out.LogRows[i].Severity) > heatmapSeverityScore(out.LogRows[j].Severity)
		}
		return heatmapLogSortKey(out.LogRows[i]) < heatmapLogSortKey(out.LogRows[j])
	})
	out.Empty = len(out.Buckets) == 0
	return out
}

func heatmapBucketFromSurface(surface SurfaceSnapshot) (OperatorHeatmapBucket, bool) {
	severity := severityForSurface(surface)
	if heatmapValueEqual(severity, "ok") {
		return OperatorHeatmapBucket{}, false
	}
	return OperatorHeatmapBucket{
		HeatmapBucket:     bucketNameForSeverity(severity),
		Surface:           surface.Surface,
		Session:           surface.Session,
		Pane:              surface.Pane,
		Severity:          severity,
		Score:             heatmapSeverityScore(severity),
		RetryAfterMS:      surface.RetryAfterMS,
		ReasonCodes:       append([]ReasonCode(nil), surface.ReasonCodes...),
		RecommendedAction: recommendedActionForDecision(surface.Decision),
	}, true
}

func worstSurface(surfaces []SurfaceSnapshot) SurfaceSnapshot {
	var worst SurfaceSnapshot
	for _, surface := range surfaces {
		if heatmapSeverityScore(severityForSurface(surface)) > heatmapSeverityScore(severityForSurface(worst)) {
			worst = surface
		}
	}
	return worst
}

func severityForSurface(surface SurfaceSnapshot) string {
	if hasAnyReason(surface.ReasonCodes, ReasonMissingSource) && heatmapValueEqual(surface.Decision, DecisionOK) {
		return "unknown"
	}
	switch surface.Decision {
	case DecisionDegrade:
		return "critical"
	case DecisionDefer:
		return "warning"
	case DecisionCoalesce:
		return "watch"
	default:
		return "ok"
	}
}

func bucketNameForSeverity(severity string) string {
	switch severity {
	case "critical":
		return "degraded"
	case "warning":
		return "retry_after"
	case "watch":
		return "coalesce"
	case "unknown":
		return "missing_source"
	default:
		return "ok"
	}
}

func heatmapSeverityScore(severity string) int {
	switch severity {
	case "critical":
		return 100
	case "warning":
		return 70
	case "watch":
		return 40
	case "unknown":
		return 20
	default:
		return 0
	}
}

func recommendedActionForDecision(decision Decision) string {
	switch decision {
	case DecisionDegrade:
		return "degrade_to_terse_status"
	case DecisionDefer:
		return "retry_after"
	case DecisionCoalesce:
		return "coalesce_updates"
	default:
		return "continue"
	}
}

func heatmapSortKey(bucket OperatorHeatmapBucket) string {
	return string(bucket.Surface) + "\x00" + bucket.Session + "\x00" + bucket.Pane
}

func heatmapLogSortKey(row OperatorHeatmapLogRow) string {
	return string(row.Surface) + "\x00" + row.Session + "\x00" + row.Pane
}

func heatmapValueEqual[T comparable](left, right T) bool {
	return left == right
}
