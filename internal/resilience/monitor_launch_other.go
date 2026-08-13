//go:build !unix

package resilience

import "os/exec"

// SetDetachedProcess is a no-op on non-Unix platforms. Process detachment
// requires a platform-specific implementation on Windows; the resilience
// monitor is primarily designed for Unix environments where tmux runs.
func SetDetachedProcess(*exec.Cmd) {
}
