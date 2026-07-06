package pressure

import (
	"math"
	"time"
)

// BuildStormDecision is the advisory decision for a build or test command.
type BuildStormDecision string

const (
	BuildStormAdmit     BuildStormDecision = "admit"
	BuildStormDefer     BuildStormDecision = "defer"
	BuildStormOffload   BuildStormDecision = "offload"
	BuildStormSerialize BuildStormDecision = "serialize"
)

// BuildStormInput contains build pressure signals from rch, local process
// accounting, and scheduler fairness.
type BuildStormInput struct {
	Now                  time.Time
	Session              string
	RequestedBuilds      int
	RCHAvailable         bool
	RCHCompatible        bool
	WorkerCount          int
	HealthyWorkers       int
	BusyWorkers          int
	QueueDepth           int
	LocalBuildCount      int
	MaxLocalBuilds       int
	SessionRunningBuilds int
	MaxBuildsPerSession  int
}

// BuildStormReport is stable robot JSON for build-storm coordination.
type BuildStormReport struct {
	Success              bool               `json:"success"`
	Timestamp            string             `json:"timestamp"`
	Session              string             `json:"session,omitempty"`
	RequestedBuilds      int                `json:"requested_builds"`
	RCHAvailable         bool               `json:"rch_available"`
	RCHCompatible        bool               `json:"rch_compatible"`
	WorkerCount          int                `json:"worker_count"`
	HealthyWorkers       int                `json:"healthy_workers"`
	BusyWorkers          int                `json:"busy_workers"`
	QueueDepth           int                `json:"queue_depth"`
	LocalBuildCount      int                `json:"local_build_count"`
	MaxLocalBuilds       int                `json:"max_local_builds,omitempty"`
	SessionRunningBuilds int                `json:"session_running_builds,omitempty"`
	MaxBuildsPerSession  int                `json:"max_builds_per_session,omitempty"`
	RemoteHeadroom       int                `json:"remote_headroom"`
	LocalHeadroom        int                `json:"local_headroom"`
	Decision             BuildStormDecision `json:"decision"`
	Reason               string             `json:"reason"`
	RetryAfter           string             `json:"retry_after,omitempty"`
	Sources              []Reading          `json:"sources,omitempty"`
}

// EvaluateBuildStorm recommends how a build/test command should run. It is
// advisory-only: callers decide whether to enforce the recommendation.
func EvaluateBuildStorm(in BuildStormInput) BuildStormReport {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	requested := maxInt(in.RequestedBuilds, 1)
	maxLocal := in.MaxLocalBuilds
	if maxLocal <= 0 {
		maxLocal = DefaultBudget().MaxBuildSlots
	}
	workerCount := maxInt(in.WorkerCount, 0)
	healthy := maxInt(in.HealthyWorkers, 0)
	if healthy > workerCount && workerCount > 0 {
		healthy = workerCount
	}
	busy := maxInt(in.BusyWorkers, 0)
	if busy > healthy && healthy > 0 {
		busy = healthy
	}
	queueDepth := maxInt(in.QueueDepth, 0)
	localBuilds := maxInt(in.LocalBuildCount, 0)
	sessionRunning := maxInt(in.SessionRunningBuilds, 0)
	remoteHeadroom := maxInt(healthy-busy, 0)
	localHeadroom := maxInt(maxLocal-localBuilds, 0)

	out := BuildStormReport{
		Success:              true,
		Timestamp:            now.UTC().Format(time.RFC3339Nano),
		Session:              in.Session,
		RequestedBuilds:      requested,
		RCHAvailable:         in.RCHAvailable,
		RCHCompatible:        in.RCHCompatible,
		WorkerCount:          workerCount,
		HealthyWorkers:       healthy,
		BusyWorkers:          busy,
		QueueDepth:           queueDepth,
		LocalBuildCount:      localBuilds,
		MaxLocalBuilds:       maxLocal,
		SessionRunningBuilds: sessionRunning,
		MaxBuildsPerSession:  maxInt(in.MaxBuildsPerSession, 0),
		RemoteHeadroom:       remoteHeadroom,
		LocalHeadroom:        localHeadroom,
		Sources:              buildStormReadings(healthy, busy, queueDepth, localBuilds),
		Decision:             BuildStormAdmit,
		Reason:               "local_headroom_available",
	}

	switch {
	case out.MaxBuildsPerSession > 0 && sessionRunning+requested > out.MaxBuildsPerSession:
		out.Decision = BuildStormSerialize
		out.Reason = "session_build_limit"
		out.RetryAfter = "15s"
	case !in.RCHAvailable:
		applyNoRCHDecision(&out, requested)
	case !in.RCHCompatible:
		applyNoRCHDecision(&out, requested)
	case healthy == 0:
		applyNoRCHDecision(&out, requested)
	case busy >= healthy || queueDepth >= healthy:
		out.Decision = BuildStormDefer
		out.Reason = "rch_workers_busy"
		out.RetryAfter = "30s"
	case requested > remoteHeadroom:
		// bd-mr59k: avoid offloading bursts that already exceed current remote
		// headroom; queueing those blindly can intensify a build storm.
		out.Decision = BuildStormDefer
		out.Reason = "rch_headroom_insufficient"
		out.RetryAfter = "20s"
	case localBuilds+requested > maxLocal:
		out.Decision = BuildStormOffload
		out.Reason = "local_fallback_risk"
	default:
		out.Decision = BuildStormOffload
		out.Reason = "remote_headroom_available"
	}
	return out
}

func applyNoRCHDecision(out *BuildStormReport, requested int) {
	if out.LocalBuildCount+requested > out.MaxLocalBuilds {
		out.Decision = BuildStormSerialize
		out.Reason = "local_fallback_risk"
		out.RetryAfter = "30s"
		return
	}
	out.Decision = BuildStormAdmit
	out.Reason = "rch_unavailable_local_headroom"
}

func buildStormReadings(healthyWorkers, busyWorkers, queueDepth, localBuilds int) []Reading {
	out := []Reading{
		{Source: SourceLocalBuild, Value: float64(localBuilds), Unit: "builds"},
	}
	if healthyWorkers > 0 {
		ratio := float64(busyWorkers+queueDepth) / float64(healthyWorkers)
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
			ratio = 0
		}
		out = append(out, Reading{Source: SourceRchQueue, Value: round3Float(ratio), Unit: "ratio"})
	}
	return out
}
