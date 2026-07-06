package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/codex"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestCountCapturedLines(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 0},
		{"single no newline", "hello", 1},
		{"single trailing newline", "hello\n", 1},
		{"two lines", "a\nb", 2},
		{"two lines trailing", "a\nb\n", 2},
		{"blank middle", "a\n\nb\n", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countCapturedLines(tt.content); got != tt.want {
				t.Fatalf("countCapturedLines(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}

// TestPaletteStateResult_Envelope verifies the result marshals with the robot
// envelope fields and the command-specific fields, and that markers_matched is
// emitted as an array (never null).
func TestPaletteStateResult_Envelope(t *testing.T) {
	content := "› /\n  /model  switch model\nslash commands\n"
	sum := sha256.Sum256([]byte(content))
	cls := codex.Classify(content)
	if cls.State != codex.StateSlashPaletteOpen {
		t.Fatalf("fixture should classify as slash_palette_open, got %q", cls.State)
	}

	res := PaletteStateResult{
		RobotResponse:   robot.NewRobotResponse(true),
		State:           cls.State.String(),
		Session:         "demo",
		Pane:            1,
		ProvenanceHash:  hex.EncodeToString(sum[:]),
		CapturedLines:   countCapturedLines(content),
		MarkersMatched:  cls.MarkersMatched,
		TimestampSource: "capture_walltime",
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{
		"success", "timestamp", "state", "session", "pane",
		"provenance_hash", "captured_lines", "markers_matched", "timestamp_source",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing JSON key %q in %s", key, string(b))
		}
	}

	if decoded["state"] != "slash_palette_open" {
		t.Errorf("state = %v, want slash_palette_open", decoded["state"])
	}
	if _, ok := decoded["markers_matched"].([]any); !ok {
		t.Errorf("markers_matched should be a JSON array, got %T", decoded["markers_matched"])
	}
}

// TestPaletteStateResult_EmptyMarkersIsArray ensures an unknown classification
// still emits markers_matched as [] not null when the command normalizes nil.
func TestPaletteStateResult_EmptyMarkersIsArray(t *testing.T) {
	res := PaletteStateResult{
		RobotResponse:  robot.NewRobotResponse(true),
		State:          codex.StateUnknown.String(),
		MarkersMatched: []string{},
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	arr, ok := decoded["markers_matched"].([]any)
	if !ok {
		t.Fatalf("markers_matched should be array, got %T", decoded["markers_matched"])
	}
	if len(arr) != 0 {
		t.Fatalf("expected empty markers array, got %v", arr)
	}
}
