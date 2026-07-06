package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderTemplate_SingleParam(t *testing.T) {
	content := "Hello <NAME>, welcome!"
	params := map[string]interface{}{"NAME": "Alice"}
	got, err := RenderTemplate(content, params, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Hello Alice, welcome!"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderTemplate_TwoParams(t *testing.T) {
	content := "Project: <PROJECT_NAME>, Phase: <PHASE>"
	params := map[string]interface{}{
		"PROJECT_NAME": "ntm",
		"PHASE":        "investigation",
	}
	got, err := RenderTemplate(content, params, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Project: ntm, Phase: investigation"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderTemplate_ArgsAsFallback(t *testing.T) {
	content := "Value: <KEY>"
	args := map[string]interface{}{"KEY": "from-args"}
	got, err := RenderTemplate(content, nil, args, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Value: from-args" {
		t.Errorf("got %q, want args fallback", got)
	}
}

func TestRenderTemplate_ParamsOverrideArgs(t *testing.T) {
	content := "Value: <KEY>"
	params := map[string]interface{}{"KEY": "from-params"}
	args := map[string]interface{}{"KEY": "from-args"}
	got, err := RenderTemplate(content, params, args, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Value: from-params" {
		t.Errorf("got %q, want params to win over args", got)
	}
}

func TestRenderTemplate_ReservedPlaceholders(t *testing.T) {
	content := "Time: <TIMESTAMP_UTC>, Path: <WORKSPACE_PATH>, Session: <SESSION_ID>"
	reserved := map[string]string{
		"TIMESTAMP_UTC":  "2026-05-07T00:00:00Z",
		"WORKSPACE_PATH": "/data/projects/ntm",
		"SESSION_ID":     "test-session",
	}
	got, err := RenderTemplate(content, nil, nil, reserved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "2026-05-07") {
		t.Errorf("TIMESTAMP_UTC not substituted: %q", got)
	}
	if !strings.Contains(got, "/data/projects/ntm") {
		t.Errorf("WORKSPACE_PATH not substituted: %q", got)
	}
	if !strings.Contains(got, "test-session") {
		t.Errorf("SESSION_ID not substituted: %q", got)
	}
}

func TestRenderTemplate_DeclaredPlaceholderUnresolved(t *testing.T) {
	content := "**Parameters:** <NAME>, <ROLE>\nHello <NAME>, your role is <ROLE>."
	params := map[string]interface{}{"NAME": "Alice"}
	_, err := RenderTemplate(content, params, nil, nil)
	if err == nil {
		t.Fatal("expected error for unresolved declared placeholder ROLE")
	}
	if !strings.Contains(err.Error(), "ROLE") {
		t.Errorf("error should mention ROLE: %v", err)
	}
}

func TestRenderTemplate_InstructionalPlaceholderSurvives(t *testing.T) {
	content := "Step <NNN>: do the thing with <PARAM>"
	params := map[string]interface{}{"PARAM": "value"}
	got, err := RenderTemplate(content, params, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "<NNN>") {
		t.Errorf("instructional <NNN> should survive: %q", got)
	}
	if strings.Contains(got, "<PARAM>") {
		t.Errorf("PARAM should be substituted: %q", got)
	}
}

func TestRenderTemplate_NoDeclaredLine(t *testing.T) {
	content := "Simple template with <THING> placeholder"
	got, err := RenderTemplate(content, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "<THING>") {
		t.Errorf("non-declared placeholder should survive: %q", got)
	}
}

func TestRenderTemplate_CaseInsensitiveKey(t *testing.T) {
	content := "Hello <NAME>"
	params := map[string]interface{}{"name": "Alice"}
	got, err := RenderTemplate(content, params, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Hello Alice" {
		t.Errorf("got %q, want case-insensitive match", got)
	}
}

func TestDeclaredPlaceholders(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "two declared",
			content: "**Parameters:** <NAME>, <ROLE>\nbody",
			want:    []string{"NAME", "ROLE"},
		},
		{
			name:    "none declared",
			content: "no parameters line here",
			want:    nil,
		},
		{
			name:    "one declared",
			content: "**Parameters:** <WORKSPACE_PATH>\nuse it",
			want:    []string{"WORKSPACE_PATH"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := declaredPlaceholders(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestArgsToEnvStringifiesSupportedTypes(t *testing.T) {
	got, err := argsToEnv(map[string]interface{}{
		"FOO":    "bar",
		"COUNT":  5,
		"FLAG":   true,
		"EMPTY":  nil,
		"LIST":   []interface{}{1, 2, 3},
		"NESTED": map[string]interface{}{"name": "ada", "count": 2},
	})
	if err != nil {
		t.Fatalf("argsToEnv() error: %v", err)
	}
	gotMap := envSliceMap(got)
	want := map[string]string{
		"FOO":    "bar",
		"COUNT":  "5",
		"FLAG":   "true",
		"EMPTY":  "",
		"LIST":   "[1,2,3]",
		"NESTED": `{"count":2,"name":"ada"}`,
	}
	if !reflect.DeepEqual(gotMap, want) {
		t.Fatalf("env map = %#v, want %#v", gotMap, want)
	}
}

func TestArgsToEnvRejectsInvalidKey(t *testing.T) {
	_, err := argsToEnv(map[string]interface{}{"foo-bar": "bad"})
	if err == nil {
		t.Fatal("argsToEnv() error = nil, want invalid env var name")
	}
	if !strings.Contains(err.Error(), "invalid env var name") {
		t.Fatalf("error = %q, want invalid env var name", err.Error())
	}
}

func envSliceMap(values []string) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if ok {
			out[key] = val
		}
	}
	return out
}
