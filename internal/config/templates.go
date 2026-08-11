package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/template"
)

// AgentTemplateVars contains variables available for agent command templates
type AgentTemplateVars struct {
	Model            string // Resolved full model name (e.g., "claude-opus-4-20250514")
	ModelAlias       string // Original alias as specified (e.g., "opus")
	ModelRequested   bool   // True when the user explicitly requested a non-default model
	SessionName      string // NTM session name
	PaneIndex        int    // Pane number (1-based)
	AgentType        string // Agent type: "cc", "cod", "gmi"
	ProjectDir       string // Project directory path
	SystemPrompt     string // System prompt content (if any)
	SystemPromptFile string // Path to system prompt file (if any)
	PersonaName      string // Name of persona (if any)
	// ReasoningEffort sets the model's reasoning budget. Currently
	// consumed by the Codex template (passes `-c
	// model_reasoning_effort=...`). Empty falls back to the
	// template-level default. See ntm#140.
	ReasoningEffort string
}

// ShellQuote safely quotes a string for use in shell commands.
// It uses single quotes and escapes any single quotes within the string.
// Example: "hello 'world'" becomes "'hello '\”world'\”'"
func ShellQuote(s string) string {
	// Empty string gets empty quotes
	if s == "" {
		return "''"
	}
	// Replace single quotes with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// systemMemoryMB returns total system RAM in MB, or 0 if unknown.
func systemMemoryMB() uint64 {
	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, err := strconv.ParseUint(fields[1], 10, 64)
					if err == nil {
						return kb / 1024
					}
				}
			}
		}
	case "darwin":
		// On macOS, sysctl is the standard way but we avoid exec;
		// use a safe default for Macs (typically 16-64GB)
		return 0
	}
	return 0
}

// nodeHeapMB computes a safe Node.js heap size based on system RAM.
// Uses 25% of total RAM, clamped between 2048 MB and 16384 MB.
// Deprecated: Claude Code is a native binary; NODE_OPTIONS has no effect.
// Kept for backward compatibility with user-customized templates.
func nodeHeapMB() string {
	return memLimitMB()
}

// memLimitMBValue computes a per-agent memory limit in MB based on system
// RAM. Uses 25% of total RAM, clamped between 2048 MB and 16384 MB.
func memLimitMBValue() uint64 {
	totalMB := systemMemoryMB()
	if totalMB == 0 {
		return 8192 // safe default for unknown systems
	}
	limitMB := totalMB / 4
	if limitMB < 2048 {
		limitMB = 2048
	}
	if limitMB > 16384 {
		limitMB = 16384
	}
	return limitMB
}

// PerAgentMemLimitMB is the exported form of memLimitMBValue: the per-agent
// memory cap (in MB) that memLimitPrefix enforces via systemd-run's
// MemoryMax. Exported so spawn admission checks can size a predictive
// aggregate-memory budget (requested agents * this value) using the exact
// same number agents are actually capped at, rather than re-deriving it.
func PerAgentMemLimitMB() uint64 {
	return memLimitMBValue()
}

// memLimitMB computes a per-agent memory limit based on system RAM.
// Uses 25% of total RAM, clamped between 2048 MB and 16384 MB.
func memLimitMB() string {
	return fmt.Sprintf("%d", memLimitMBValue())
}

// memHighFraction is the MemoryHigh soft-throttle threshold as a fraction of
// the MemoryMax hard cap applied by memLimitPrefix. Cgroup v2 reclaims and
// throttles a scope as its usage approaches MemoryHigh instead of jumping
// straight to an OOM kill at MemoryMax, giving a transient spike (e.g. an
// in-pane `cargo build`) a chance to finish rather than being killed outright.
const memHighFraction = 0.8

// cargoBuildJobsPerJobMB is the assumed peak memory footprint of a single
// rustc codegen worker, used to size cargoBuildJobs. Based on observed
// OOM-killer log data for rustc processes killed inside an agent pane's
// cgroup (anon-rss ~1.3-1.4GB, total-vm ~2.1-2.4GB); rounded up for headroom.
const cargoBuildJobsPerJobMB = 2048

// cargoBuildJobsReserveFraction is the fraction of a pane's memory budget
// reserved for the agent process itself, left unavailable to cargo/rustc
// when sizing cargoBuildJobs.
const cargoBuildJobsReserveFraction = 0.25

// cargoBuildJobs computes a default CARGO_BUILD_JOBS value sized to fit
// within the pane's memory budget (memLimitMBValue) rather than scaling with
// CPU count — this bounds the actual memory driver of an in-pane `cargo
// build` (parallel rustc workers), which is what pushes a pane's cgroup over
// its MemoryMax cap, rather than just capping concurrency by core count.
func cargoBuildJobs() string {
	budgetMB := float64(memLimitMBValue()) * (1 - cargoBuildJobsReserveFraction)
	jobs := int(budgetMB / cargoBuildJobsPerJobMB)
	if jobs < 1 {
		jobs = 1
	}
	return fmt.Sprintf("%d", jobs)
}

// hasSystemdUserSession checks whether a systemd user session is available.
// In Docker containers, devcontainers, WSL1, etc., systemd-run --user fails
// because there is no user session bus. We detect this by checking for the
// per-user systemd private socket.
var hasSystemdUserSession = sync.OnceValue(func() bool {
	uid := os.Getuid()
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/systemd/private", uid))
	return err == nil
})

// memLimitPrefix returns a command prefix that enforces a real memory limit.
// On Linux with a systemd user session, uses systemd-run --user --scope -p
// MemoryMax=/-p MemoryHigh= (cgroup v2): MemoryHigh throttles via reclaim as
// usage approaches the soft threshold, MemoryMax is the hard OOM-kill
// backstop. On other platforms or when systemd is unavailable (e.g., Docker
// containers, WSL1), returns an empty string.
func memLimitPrefix() string {
	if runtime.GOOS == "linux" && hasSystemdUserSession() {
		maxMB := memLimitMBValue()
		highMB := uint64(float64(maxMB) * memHighFraction)
		return fmt.Sprintf("systemd-run --user --scope -q -p MemoryMax=%dM -p MemoryHigh=%dM", maxMB, highMB)
	}
	return ""
}

// templateFuncs contains custom functions available in templates
var templateFuncs = template.FuncMap{
	// default returns the fallback if value is empty
	"default": func(fallback, value string) string {
		if value == "" {
			return fallback
		}
		return value
	},
	// eq checks string equality
	"eq": func(a, b string) bool {
		return a == b
	},
	// ne checks string inequality
	"ne": func(a, b string) bool {
		return a != b
	},
	// contains checks if string contains substring
	"contains": func(s, substr string) bool {
		return strings.Contains(s, substr)
	},
	// hasPrefix checks if string has prefix
	"hasPrefix": func(s, prefix string) bool {
		return strings.HasPrefix(s, prefix)
	},
	// hasSuffix checks if string has suffix
	"hasSuffix": func(s, suffix string) bool {
		return strings.HasSuffix(s, suffix)
	},
	// lower converts to lowercase
	"lower": func(s string) string {
		return strings.ToLower(s)
	},
	// upper converts to uppercase
	"upper": func(s string) string {
		return strings.ToUpper(s)
	},
	// shellQuote safely quotes a string for shell command usage
	// Use this when inserting untrusted values into shell commands
	"shellQuote": ShellQuote,
	// nodeHeapMB returns a safe Node.js heap size based on system RAM
	// Deprecated: kept for backward compat, use memLimitMB instead
	"nodeHeapMB": nodeHeapMB,
	// memLimitMB returns a per-agent memory limit in MB based on system RAM
	"memLimitMB": memLimitMB,
	// memLimitPrefix returns an OS-appropriate command prefix that enforces
	// a real memory limit (systemd-run on Linux, empty on other platforms)
	"memLimitPrefix": memLimitPrefix,
	// cargoBuildJobs returns a default CARGO_BUILD_JOBS value sized to the
	// per-agent memory budget, so an in-pane `cargo build` can't blow past
	// the pane's own memory cap by scaling parallelism with core count
	"cargoBuildJobs": cargoBuildJobs,
}

func templateReferencesModel(tmpl string) bool {
	return strings.Contains(tmpl, ".Model") || strings.Contains(tmpl, ".ModelAlias")
}

// GenerateAgentCommand renders an agent command template with the given variables.
// Legacy commands without template syntax are returned as-is unless they would
// silently drop an explicitly requested model selection.
// Returns an error if template parsing or execution fails.
func GenerateAgentCommand(tmpl string, vars AgentTemplateVars) (string, error) {
	if vars.ModelRequested && !templateReferencesModel(tmpl) {
		requestedModel := vars.Model
		if requestedModel == "" {
			requestedModel = vars.ModelAlias
		}
		if requestedModel == "" {
			requestedModel = "<requested>"
		}
		if !strings.Contains(tmpl, "{{") {
			return "", fmt.Errorf(
				"model override %q was specified but agent command has no template syntax (no {{.Model}} or {{.ModelAlias}} placeholder); "+
					"the model would be silently ignored. Convert the command to template format or remove the model override. "+
					"Command: %s", requestedModel, tmpl)
		}
		return "", fmt.Errorf(
			"model override %q was specified but agent command template does not reference .Model or .ModelAlias; "+
				"the model would be silently ignored. Update the template or remove the model override. "+
				"Command: %s", requestedModel, tmpl)
	}

	// Fast path: if no template syntax, return as-is (legacy mode)
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}

	t, err := template.New("agent").Funcs(templateFuncs).Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}

	result := strings.TrimSpace(buf.String())

	return result, nil
}

// IsTemplateCommand checks if a command string uses template syntax
func IsTemplateCommand(cmd string) bool {
	return strings.Contains(cmd, "{{")
}

// DefaultAgentTemplates returns default agent command templates with model injection support.
// These templates show the recommended format for model-aware agent commands.
// System prompt injection is supported via SystemPromptFile for persona agents.
func DefaultAgentTemplates() AgentConfig {
	// Claude, Codex, and Gemini all carry {{cargoBuildJobs}}/{{memLimitPrefix}}
	// so an in-pane `cargo build` inherits a bounded CARGO_BUILD_JOBS and the
	// same per-pane cgroup cap regardless of which agent's pane it runs in —
	// these render inline into the command string (rather than being
	// injected the way AGENT_NAME is, in internal/cli/spawn.go) so they take
	// effect on both the `ntm spawn` and `--robot-spawn` paths, which share
	// GenerateAgentCommand but not the CLI path's post-render env-prepending.
	return AgentConfig{
		Claude: `CARGO_BUILD_JOBS={{cargoBuildJobs}} {{memLimitPrefix}} claude --dangerously-skip-permissions{{if .Model}} --model {{shellQuote .Model}}{{end}}{{if .ReasoningEffort}} --effort {{shellQuote .ReasoningEffort}}{{end}}{{if .SystemPromptFile}} --system-prompt-file {{shellQuote .SystemPromptFile}}{{end}}`,
		Codex:  `CARGO_BUILD_JOBS={{cargoBuildJobs}} {{memLimitPrefix}} {{if .SystemPromptFile}}CODEX_SYSTEM_PROMPT="$(cat {{shellQuote .SystemPromptFile}})" {{end}}codex --dangerously-bypass-approvals-and-sandbox -m {{shellQuote (.Model | default "gpt-5.6-sol")}} -c model_reasoning_effort={{shellQuote (.ReasoningEffort | default "xhigh")}} -c model_reasoning_summary_format=experimental --search`,
		Gemini: `CARGO_BUILD_JOBS={{cargoBuildJobs}} {{memLimitPrefix}} gemini{{if .Model}} --model {{shellQuote .Model}}{{end}} --yolo`,
		// Antigravity (agy): the model is hard-pinned to "Gemini 3.1 Pro (High)"
		// by ResolveModel, so --model is always injected. --dangerously-skip-permissions
		// is agy's autonomous (auto-approve) flag — the equivalent of gemini's --yolo —
		// which the dcg agy guard (F5) backstops.
		Antigravity: `agy --model {{shellQuote .Model}} --dangerously-skip-permissions`,
		Ollama:      `ollama run {{shellQuote (.Model | default "codellama:latest")}}`,
		Cursor:      `cursor{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Windsurf:    `windsurf{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		Aider:       `aider{{if .Model}} --model {{shellQuote .Model}}{{end}}`,
		// Opencode (oc): the upstream `opencode` binary takes `-m/--model
		// provider/model`. Without the {{.Model}} placeholder a
		// `--oc=N:provider/model` spawn is rejected ("agent command has no
		// template syntax") and Agent Mail registration fails ("model cannot
		// be empty"). NOTE: `--variant` (reasoning effort) is NOT a flag on the
		// root `opencode` TUI command an interactive pane launches — it exists
		// only on the `opencode run` subcommand (anomalyco/opencode#7354, PR
		// #7358 still open/unmerged), so injecting it here would make the pane
		// fail to launch whenever an effort is supplied. See ntm#116, ntm#193.
		Opencode: DefaultOpencodeCommand,
	}
}

// DefaultOpencodeCommand is the launch command used when [agents] oc is not
// configured. It mirrors DefaultAgentTemplates().Opencode so that the spawn,
// add, and restart dispatch paths inject the model the same way a freshly
// generated config does. Only `--model` is injected: it is the lone
// model/reasoning flag the root `opencode` TUI command accepts (the
// `--variant` effort flag lives on the `opencode run` subcommand only — see
// the note in DefaultAgentTemplates). See ntm#193.
const DefaultOpencodeCommand = `opencode{{if .Model}} --model {{shellQuote .Model}}{{end}}`
