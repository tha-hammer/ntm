package profiler

import (
	"math"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

// BackpressureInputsFromBottlenecks maps profiler hotspots onto overload
// surfaces without forcing callers to understand span names.
func BackpressureInputsFromBottlenecks(snapshot BottleneckSnapshot) []backpressure.SurfaceInput {
	if !snapshot.Success {
		return []backpressure.SurfaceInput{{
			Surface:        backpressure.SurfaceProfiler,
			SourceLoaded:   false,
			MissingWarning: "profiler bottleneck snapshot is unavailable.",
		}}
	}
	inputs := make([]backpressure.SurfaceInput, 0, len(snapshot.Hotspots))
	for _, hotspot := range snapshot.Hotspots {
		surface := surfaceForHotspot(hotspot)
		inputs = append(inputs, backpressure.SurfaceInput{
			Surface:      surface,
			Session:      hotspot.Correlation.Session,
			Pane:         hotspot.Correlation.Pane,
			Command:      hotspot.Correlation.Command,
			LatencyMS:    roundFloatMS(hotspot.MaxMs),
			SourceLoaded: true,
		})
	}
	if len(inputs) == 0 {
		return []backpressure.SurfaceInput{{
			Surface:        backpressure.SurfaceProfiler,
			SourceLoaded:   false,
			MissingWarning: "profiler is enabled but has no hotspot samples yet.",
		}}
	}
	return inputs
}

func surfaceForHotspot(hotspot BottleneckHotspot) backpressure.Surface {
	switch hotspot.Phase {
	case "tmux":
		return backpressure.SurfaceTmuxCapture
	case "robot":
		return backpressure.SurfaceRobot
	case "serve":
		return backpressure.SurfaceREST
	default:
		return backpressure.SurfaceProfiler
	}
}

func roundFloatMS(ms float64) int64 {
	if ms <= 0 {
		return 0
	}
	return int64(math.Round(ms))
}
