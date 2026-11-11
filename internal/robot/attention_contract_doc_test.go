package robot_test

// Loop 9, Behavior 8: docs/attention-feed-contract.md matches the code. Every
// JSON example parses, every field name in one is a JSON field of a robot or
// serve output type, the event field table is exactly AttentionEvent's
// fields, and the facts the doc states (retention, poll interval, recipient
// filter) are read from the code, not typed here.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/serve"
)

func readContractDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "attention-feed-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// docSection is the text under a heading, up to the next heading of the same
// or a higher level.
func docSection(t *testing.T, doc, heading string) string {
	t.Helper()
	level := strings.Count(strings.Fields(heading)[0], "#")
	_, rest, ok := strings.Cut(doc, "\n"+heading)
	if !ok {
		t.Fatalf("the doc has no %q heading", heading)
	}
	lines := strings.Split(rest, "\n")
	var out []string
	inCode := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
		}
		if i > 0 && !inCode && strings.HasPrefix(line, "#") {
			if hashes := len(line) - len(strings.TrimLeft(line, "#")); hashes <= level && strings.HasPrefix(line[hashes:], " ") {
				break
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// jsonFieldNames is every JSON field name reachable from the types, and the
// names of fields whose Go type is a map (their JSON keys are data).
func jsonFieldNames(roots ...any) (names, mapFields map[string]bool) {
	names, mapFields = map[string]bool{}, map[string]bool{}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			switch {
			case name == "-":
				continue
			case name == "" && field.Anonymous:
			case name == "" && field.IsExported():
				name = field.Name
			}
			if name != "" {
				names[name] = true
				inner := field.Type
				for inner.Kind() == reflect.Pointer {
					inner = inner.Elem()
				}
				if inner.Kind() == reflect.Map {
					mapFields[name] = true
				}
			}
			walk(field.Type)
		}
	}
	for _, root := range roots {
		walk(reflect.TypeOf(root))
	}
	return names, mapFields
}

// docJSONBlocks returns each ```json block with its line number. Comments
// (/* ... */) and "..." lines mark elided content, and are removed, along
// with the trailing commas they leave.
func docJSONBlocks(doc string) (blocks []string, lines []int) {
	comment := regexp.MustCompile(`/\*.*?\*/`)
	trailing := regexp.MustCompile(`,(\s*[}\]])`)
	var current []string
	start, in := 0, false
	for i, line := range strings.Split(doc, "\n") {
		switch {
		case !in && strings.TrimSpace(line) == "```json":
			in, start, current = true, i+2, nil
		case in && strings.TrimSpace(line) == "```":
			in = false
			text := comment.ReplaceAllString(strings.Join(current, "\n"), "")
			var kept []string
			for _, l := range strings.Split(text, "\n") {
				if trimmed := strings.TrimSpace(l); trimmed != "..." && trimmed != "...," {
					kept = append(kept, l)
				}
			}
			blocks = append(blocks, trailing.ReplaceAllString(strings.Join(kept, "\n"), "$1"))
			lines = append(lines, start)
		case in:
			current = append(current, line)
		}
	}
	return blocks, lines
}

// collectKeys adds every object key in v to keys. Inside a free-form object
// (a field whose Go type is a map, such as event details, WebSocket data or
// counts by category) the keys are data, so it adds them only when the values
// are objects, whose own keys it then checks.
func collectKeys(v any, freeForm map[string]bool, keys map[string]bool) {
	switch val := v.(type) {
	case map[string]any:
		for key, child := range val {
			keys[key] = true
			if !freeForm[key] {
				collectKeys(child, freeForm, keys)
				continue
			}
			if inner, ok := child.(map[string]any); ok {
				for _, value := range inner {
					if _, isObject := value.(map[string]any); isObject {
						collectKeys(value, freeForm, keys)
					}
				}
			}
		}
	case []any:
		for _, child := range val {
			collectKeys(child, freeForm, keys)
		}
	}
}

func TestAttentionContractDocMatchesCode(t *testing.T) {
	doc := readContractDoc(t)

	// Names no code produces.
	// The old capabilities block was "attention_feed": {...}; the heartbeat's
	// source value is really "attention_feed", so only the object is banned.
	for _, banned := range []string{"evt_", "retention_expires_at", "cursor_retention_min_sec", `"attention_feed": {`} {
		for i, line := range strings.Split(doc, "\n") {
			if strings.Contains(line, banned) {
				t.Errorf("line %d names %s, which no code produces: %s", i+1, banned, strings.TrimSpace(line))
			}
		}
	}

	// Retention, from the code's default.
	retention := robot.DefaultAttentionFeedConfig().RetentionPeriod
	days := fmt.Sprintf("**%d days**", int(retention.Hours()/24))
	if !strings.Contains(docSection(t, doc, "### 6.3"), days) {
		t.Errorf("section 6.3 doesn't say events are kept %s (the code keeps them %s)", days, retention)
	}

	// The event field table is exactly AttentionEvent's fields.
	tableFields := map[string]bool{}
	for _, match := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(docSection(t, doc, "### 3.1"), -1) {
		tableFields[match[1]] = true
	}
	topLevel := map[string]bool{}
	typ := reflect.TypeOf(robot.AttentionEvent{})
	for i := 0; i < typ.NumField(); i++ {
		if name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			topLevel[name] = true
		}
	}
	if !reflect.DeepEqual(tableFields, topLevel) {
		t.Errorf("section 3.1's field table lists %v, but AttentionEvent has %v", sortedKeys(tableFields), sortedKeys(topLevel))
	}

	// Addressed mail events, cross-process delivery and the store.
	addressed := docSection(t, doc, "## 16. Addressed Mail Events")
	for _, want := range []string{"`recipient_project`", "`recipient_agent`", "`message_id`", "`--events-recipient", "unique"} {
		if !strings.Contains(addressed, want) {
			t.Errorf("section 16 doesn't mention %s", want)
		}
	}
	durable := docSection(t, doc, "## 17. Durable Feed and Cross-Process Delivery")
	if poll := robot.AttentionPollInterval.String(); !strings.Contains(durable, "every "+strings.TrimSuffix(poll, "s")+" second") {
		t.Errorf("section 17 doesn't say other processes' events are read every %s", poll)
	}
	for _, want := range []string{"state.db", "once"} {
		if !strings.Contains(durable, want) {
			t.Errorf("section 17 doesn't mention %q", want)
		}
	}

	// Every JSON example parses, and names only real fields.
	known, mapFields := jsonFieldNames(
		robot.AttentionEvent{}, robot.EventsOutput{}, robot.SnapshotOutput{}, robot.SnapshotDeltaOutput{},
		robot.DigestResponse{}, robot.DigestOutput{}, robot.AttentionResponse{}, robot.AttentionOutput{},
		robot.OverlayOutput{}, robot.CapabilitiesOutput{}, robot.AttentionCapabilities{}, robot.WaitResponse{},
		robot.RobotResponse{}, serve.WSMessage{}, serve.WSEvent{}, serve.WSError{}, serve.WSSubscribeRequest{},
	)
	blocks, lines := docJSONBlocks(doc)
	if len(blocks) == 0 {
		t.Fatal("the doc has no JSON examples")
	}
	for i, block := range blocks {
		dec := json.NewDecoder(bytes.NewReader([]byte(block)))
		dec.UseNumber()
		var value any
		if err := dec.Decode(&value); err != nil {
			t.Errorf("the JSON example at line %d doesn't parse: %v", lines[i], err)
			continue
		}
		keys := map[string]bool{}
		collectKeys(value, mapFields, keys)
		for _, key := range sortedKeys(keys) {
			if !known[key] {
				t.Errorf("the JSON example at line %d names %q, which isn't a field of any robot output", lines[i], key)
			}
		}
	}

	// Every flag on an ntm command line in a code block is one
	// --robot-capabilities lists, as a command or a parameter.
	caps, err := robot.GetCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, cmd := range caps.Commands {
		flags[cmd.Flag] = true
		for _, param := range cmd.Parameters {
			flags[param.Flag] = true
		}
	}
	flagPattern := regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	inCode := false
	for i, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if !inCode || !strings.HasPrefix(trimmed, "ntm ") {
			continue
		}
		for _, flag := range flagPattern.FindAllString(trimmed, -1) {
			if !flags[flag] {
				t.Errorf("line %d uses %s, which --robot-capabilities doesn't list: %s", i+1, flag, trimmed)
			}
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
