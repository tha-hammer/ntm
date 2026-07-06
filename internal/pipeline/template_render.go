package pipeline

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

var declaredParamPattern = regexp.MustCompile(`\*\*Parameters:\*\*\s*(.+)`)
var placeholderPattern = regexp.MustCompile(`<([A-Z][A-Z0-9_]*)>`)
var envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// RenderTemplate substitutes <KEY> placeholders in content with values from
// params. Reserved placeholders (<TIMESTAMP_UTC>, <WORKSPACE_PATH>,
// <SESSION_ID>) are auto-populated from the provided reserved map. Args values
// are used as fallback when a key is not found in params.
//
// After substitution, any declared placeholder (listed on a **Parameters:**
// line) that remains unresolved causes an error. Instructional placeholders
// like <NNN> that are NOT declared in Parameters survive validation.
func RenderTemplate(content string, params, args map[string]interface{}, reserved map[string]string) (string, error) {
	merged := make(map[string]string)
	for k, v := range args {
		merged[strings.ToUpper(k)] = fmt.Sprintf("%v", v)
	}
	for k, v := range params {
		merged[strings.ToUpper(k)] = fmt.Sprintf("%v", v)
	}
	for k, v := range reserved {
		merged[strings.ToUpper(k)] = v
	}

	rendered := placeholderPattern.ReplaceAllStringFunc(content, func(match string) string {
		key := match[1 : len(match)-1]
		if val, ok := merged[key]; ok {
			return val
		}
		return match
	})

	declared := declaredPlaceholders(content)
	var unresolved []string
	for _, key := range declared {
		if _, ok := merged[key]; !ok {
			if strings.Contains(rendered, "<"+key+">") {
				unresolved = append(unresolved, key)
			}
		}
	}
	if len(unresolved) > 0 {
		return "", fmt.Errorf("unresolved declared placeholders: %s", strings.Join(unresolved, ", "))
	}

	return rendered, nil
}

// declaredPlaceholders extracts placeholder names from a **Parameters:** line.
func declaredPlaceholders(content string) []string {
	m := declaredParamPattern.FindStringSubmatch(content)
	if len(m) < 2 {
		return nil
	}
	var names []string
	for _, pm := range placeholderPattern.FindAllStringSubmatch(m[1], -1) {
		if len(pm) >= 2 {
			names = append(names, pm[1])
		}
	}
	return names
}

// ReservedPlaceholders returns the standard reserved placeholders for template
// rendering based on the current execution context.
func ReservedPlaceholders(projectDir, sessionID string) map[string]string {
	return map[string]string{
		"TIMESTAMP_UTC":  time.Now().UTC().Format(time.RFC3339),
		"WORKSPACE_PATH": projectDir,
		"SESSION_ID":     sessionID,
	}
}

func argsToEnv(args map[string]interface{}) ([]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	env := make([]string, 0, len(args))
	for key, value := range args {
		if !envVarNamePattern.MatchString(key) {
			return nil, fmt.Errorf("invalid env var name %q: use POSIX identifier syntax [A-Za-z_][A-Za-z0-9_]*", key)
		}
		stringValue, err := argValueString(value)
		if err != nil {
			return nil, fmt.Errorf("arg %q: %w", key, err)
		}
		env = append(env, fmt.Sprintf("%s=%s", key, stringValue))
	}
	sort.Strings(env)
	return env, nil
}

func argValueString(value interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return fmt.Sprintf("%v", v), nil
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%v", v), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%v", v), nil
	case float32, float64:
		return fmt.Sprintf("%v", v), nil
	}

	kind := reflect.TypeOf(value).Kind()
	if kind == reflect.Slice || kind == reflect.Array || kind == reflect.Map {
		data, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("json encode: %w", err)
		}
		return string(data), nil
	}
	return fmt.Sprintf("%v", value), nil
}
