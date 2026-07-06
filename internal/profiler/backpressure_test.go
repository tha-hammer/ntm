package profiler

import (
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
)

func TestBackpressureInputsFromBottlenecksMapsSurfaces(t *testing.T) {
	snapshot := BottleneckSnapshot{
		Success: true,
		Hotspots: []BottleneckHotspot{
			{
				Name:  "tmux.capture",
				Phase: "tmux",
				MaxMs: 1250.4,
				Correlation: BottleneckCorrelation{
					Session: "proj",
					Pane:    "%1",
				},
			},
			{
				Name:        "serve.handler",
				Phase:       "serve",
				MaxMs:       900,
				Correlation: BottleneckCorrelation{Command: "GET /api/status"},
			},
			{
				Name:  "robot.status",
				Phase: "robot",
				MaxMs: 400,
			},
		},
	}

	inputs := BackpressureInputsFromBottlenecks(snapshot)
	requireEqual(t, len(inputs), 3)
	requireEqual(t, inputs[0].Surface, backpressure.SurfaceTmuxCapture)
	requireEqual(t, inputs[0].LatencyMS, int64(1250))
	requireEqual(t, inputs[1].Surface, backpressure.SurfaceREST)
	requireEqual(t, inputs[1].Command, "GET /api/status")
	requireEqual(t, inputs[2].Surface, backpressure.SurfaceRobot)
}

func TestBackpressureInputsFromBottlenecksMissingSnapshot(t *testing.T) {
	inputs := BackpressureInputsFromBottlenecks(BottleneckSnapshot{})

	requireEqual(t, len(inputs), 1)
	if inputs[0].SourceLoaded {
		t.Fatalf("missing source should not be loaded: %#v", inputs[0])
	}
}

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
