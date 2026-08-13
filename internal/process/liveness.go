// Package process provides PID-based process liveness checks.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	gopsutilprocess "github.com/shirou/gopsutil/v4/process"
)

// IsAlive checks whether a process with the given PID is still running.
// It uses /proc on Linux for an efficient, non-racy check and falls back
// to kill(pid, 0) plus a `ps` zombie-state probe on other platforms.
//
// Zombies are deliberately treated as not-alive: a zombie process is
// terminated but unreaped, and from any practical liveness standpoint
// (e.g. the memory-daemon supervisor) it is gone. Without this, the
// kill(pid, 0) fallback used on macOS would happily report a zombie as
// alive — that's the macOS-only failure in
// TestCheckMemoryDaemon_ZombiePIDFileIsCleanedUp.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	// Fast path: inspect /proc state directly on Linux. Zombie and dead
	// processes still have a /proc entry until reaped, but they are not alive.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "State:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				state := fields[1]
				return state != "Z" && state != "X" && state != "x"
			}
			break
		}
		return true
	}

	// Fallback: signal 0 check (works on all POSIX systems).
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}

	// Signal 0 succeeded — but on macOS that includes zombies, which
	// /proc would have rejected on Linux. Cross-check the ps state so
	// the two platforms agree on what "alive" means.
	if cmd := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)); cmd != nil {
		if out, perr := cmd.Output(); perr == nil {
			state := strings.TrimSpace(string(out))
			if state != "" {
				short := string(state[0])
				if short == "Z" || short == "X" || short == "x" {
					return false
				}
			}
		}
	}
	return true
}

// ErrProcessNotRunning is returned by CaptureProcessIdentity when pid does
// not correspond to a currently running process. Callers building a live
// snapshot treat this as "the process is gone" rather than an unexpected
// lookup failure.
var ErrProcessNotRunning = errors.New("process: not running")

// ProcessIdentity is the immutable identity of a process: its PID paired
// with its creation time. The OS reuses PIDs, but a given PID+CreateTime
// pair is not reused, so a stored ProcessIdentity can be revalidated
// against a fresh capture immediately before signaling it, ruling out a
// signal landing on an unrelated process that inherited the same PID.
type ProcessIdentity struct {
	PID              int
	CreateTimeMillis int64
}

// CaptureProcessIdentity captures the current identity of the process with
// the given PID. It always constructs a fresh gopsutil process handle — a
// retained handle caches its creation time, which would defeat the purpose
// of revalidating identity on every call — so every capture reflects the
// process table at call time.
//
// It returns ErrProcessNotRunning if pid is invalid or does not correspond
// to a running process (using the same zombie-aware liveness semantics as
// IsAlive), and a wrapped error for any other lookup failure, including
// context cancellation.
func CaptureProcessIdentity(ctx context.Context, pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("%w: pid %d", ErrProcessNotRunning, pid)
	}
	if err := ctx.Err(); err != nil {
		return ProcessIdentity{}, err
	}
	if !IsAlive(pid) {
		return ProcessIdentity{}, fmt.Errorf("%w: pid %d", ErrProcessNotRunning, pid)
	}

	proc, err := gopsutilprocess.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("%w: pid %d: %v", ErrProcessNotRunning, pid, err)
	}
	createMs, err := proc.CreateTimeWithContext(ctx)
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("process: create time for pid %d: %w", pid, err)
	}

	return ProcessIdentity{PID: pid, CreateTimeMillis: createMs}, nil
}

// HasChildAlive returns true if the given shell PID has at least one
// living child process. This is useful for detecting whether an agent
// launched inside a tmux shell pane is still running.
//
// We enumerate ALL children of the parent and return true on the first
// living one. The original implementation returned only GetChildPID's
// first-child answer and then checked its liveness; under heavy
// parallel-test load, that first child could be a zombie of an
// unrelated parallel test even though plenty of healthy children
// remained, which flapped TestDetectProcessStatus_PIDWithChildren and
// TestDetectProcessStatus_PIDBasedCurrentProcess on busy runners.
func HasChildAlive(shellPID int) bool {
	if shellPID <= 0 {
		return false
	}
	for _, child := range getChildPIDs(shellPID) {
		if IsAlive(child) {
			return true
		}
	}
	return false
}

// GetChildPID returns the first child PID of the given parent, or 0 if
// no child is found. It reads /proc on Linux and falls back to pgrep.
// Kept for backward compatibility — callers that need
// "is at least one child alive" should use HasChildAlive directly.
func GetChildPID(parentPID int) int {
	if parentPID <= 0 {
		return 0
	}
	children := getChildPIDs(parentPID)
	if len(children) == 0 {
		return 0
	}
	return children[0]
}

// getChildPIDs returns every direct child PID of parentPID. It uses
// /proc on Linux (walking every task/<tid>/children entry to catch
// children adopted by non-main threads, which Go's runtime can do
// when ForkExec runs on a non-main goroutine) and falls back to pgrep
// on systems without /proc.
func getChildPIDs(parentPID int) []int {
	if parentPID <= 0 {
		return nil
	}

	seen := make(map[int]struct{})
	var pids []int
	add := func(pid int) {
		if pid <= 0 {
			return
		}
		if _, ok := seen[pid]; ok {
			return
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}

	// /proc walk: every thread of the parent can be the recorded
	// fork() caller in Linux's accounting, so read every
	// task/<tid>/children file.
	taskDir := fmt.Sprintf("/proc/%d/task", parentPID)
	if entries, err := os.ReadDir(taskDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := fmt.Sprintf("%s/%s/children", taskDir, entry.Name())
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			for _, raw := range strings.Fields(string(data)) {
				if pid, perr := strconv.Atoi(raw); perr == nil {
					add(pid)
				}
			}
		}
	}

	// Fallback / supplement: pgrep also picks up children where the
	// parent's /proc layout is absent (macOS) or where /proc lists
	// have raced behind the kernel.
	if cmd := exec.Command("pgrep", "-P", strconv.Itoa(parentPID)); cmd != nil {
		if out, err := cmd.Output(); err == nil {
			for _, raw := range strings.Fields(string(out)) {
				if pid, perr := strconv.Atoi(raw); perr == nil {
					add(pid)
				}
			}
		}
	}

	return pids
}

// IsChildAlive is an alias for HasChildAlive for backward compatibility.
var IsChildAlive = HasChildAlive

// GetCmdline returns the full argv of the process with the given PID,
// or an empty slice if it can't be read. On Linux this uses
// `/proc/<pid>/cmdline`; on other systems it falls back to `ps -o
// command=`.
//
// This is useful for distinguishing wrapper processes (`bun`, `node`,
// `npx`, `python`) from the agent binary they launched: the wrapper
// shows up as the process's basename, but the agent it's running is
// usually visible in argv (e.g. `bun /home/.../codex ...`).
func GetCmdline(pid int) []string {
	if pid <= 0 {
		return nil
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		// /proc/.../cmdline is NUL-separated, with a trailing NUL.
		raw := strings.TrimRight(string(data), "\x00")
		if raw == "" {
			return nil
		}
		return strings.Split(raw, "\x00")
	}
	// Fallback for non-Linux. `ps -o command=` returns a single
	// space-separated string with no easy way to recover argv0
	// boundaries inside arguments, but it's enough for substring
	// matching against agent binary names.
	cmd := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil
	}
	return strings.Fields(line)
}

// GetChildPIDs returns up to `limit` direct children of `parentPID`.
// Linux fast path reads `/proc/<pid>/task/<pid>/children`; falls back
// to `pgrep -P`. Returns nil on failure.
func GetChildPIDs(parentPID int, limit int) []int {
	if parentPID <= 0 {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}

	collect := func(parts []string) []int {
		var out []int
		for _, p := range parts {
			pid, err := strconv.Atoi(p)
			if err != nil || pid <= 0 {
				continue
			}
			out = append(out, pid)
			if len(out) >= limit {
				return out
			}
		}
		return out
	}

	taskPath := fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID)
	if data, err := os.ReadFile(taskPath); err == nil {
		return collect(strings.Fields(string(data)))
	}
	cmd := exec.Command("pgrep", "-P", strconv.Itoa(parentPID))
	if out, err := cmd.Output(); err == nil {
		return collect(strings.Fields(string(out)))
	}
	return nil
}

// GetChildPIDsContext returns up to limit direct children of parentPID,
// same as GetChildPIDs, but reports enumeration failures instead of
// silently returning nil. This lets a live-snapshot caller distinguish "the
// parent legitimately has zero children right now" from "enumeration
// couldn't be completed" (e.g. a real filesystem error or a cancelled
// context), so it can decide whether a refresh is safe to commit.
func GetChildPIDsContext(ctx context.Context, parentPID int, limit int) ([]int, error) {
	if parentPID <= 0 {
		return nil, fmt.Errorf("process: invalid parent pid %d", parentPID)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 8
	}

	collect := func(parts []string) []int {
		var out []int
		for _, p := range parts {
			pid, err := strconv.Atoi(p)
			if err != nil || pid <= 0 {
				continue
			}
			out = append(out, pid)
			if len(out) >= limit {
				return out
			}
		}
		return out
	}

	taskPath := fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID)
	data, err := os.ReadFile(taskPath)
	if err == nil {
		return collect(strings.Fields(string(data))), nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("process: read %s: %w", taskPath, err)
	}

	cmd := exec.CommandContext(ctx, "pgrep", "-P", strconv.Itoa(parentPID))
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// pgrep exits non-zero with no output when nothing matches —
			// a legitimate zero-children result, not an enumeration failure.
			return nil, nil
		}
		return nil, fmt.Errorf("process: pgrep -P %d: %w", parentPID, err)
	}
	return collect(strings.Fields(string(out))), nil
}

// processStateNames maps single-character /proc state codes to human names.
var processStateNames = map[string]string{
	"R": "running",
	"S": "sleeping",
	"D": "disk sleep",
	"Z": "zombie",
	"T": "stopped",
	"t": "tracing stop",
	"X": "dead",
	"x": "dead",
	"K": "wakekill",
	"W": "waking",
	"P": "parked",
	"I": "idle",
}

// GetProcessState reads the process state.
// It tries /proc/<pid>/status on Linux and falls back to ps on macOS/Linux.
// Returns the single-character state code (R, S, D, Z, T, etc.),
// a human-readable name, and any error.
func GetProcessState(pid int) (string, string, error) {
	if pid <= 0 {
		return "", "", fmt.Errorf("invalid pid: %d", pid)
	}

	// Try /proc first (Linux)
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "State:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					state := fields[1]
					name := processStateNames[state]
					if name == "" {
						name = "unknown"
					}
					return state, name, nil
				}
			}
		}
	}

	// Fallback to ps (macOS and Linux)
	// 'state' column provides the process state
	cmd := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err == nil {
		state := strings.TrimSpace(string(out))
		if state != "" {
			// ps state can have multiple chars (e.g., S+ on macOS), we take the first
			shortState := string(state[0])
			name := processStateNames[shortState]
			if name == "" {
				// macOS specific states
				switch shortState {
				case "I":
					name = "idle"
				case "U":
					name = "uninterruptible"
				default:
					name = "unknown"
				}
			}
			return shortState, name, nil
		}
	}

	return "", "", fmt.Errorf("failed to get process state for pid %d", pid)
}
