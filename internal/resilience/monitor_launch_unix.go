//go:build unix

package resilience

import (
	"os/exec"
	"syscall"
)

// SetDetachedProcess configures cmd to run in a new session, detached from
// the terminal so it survives when the parent (the spawning `ntm`
// invocation) exits.
func SetDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
