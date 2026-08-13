//go:build linux

package process

import (
	"fmt"
	"os"
)

// CgroupPathForPID resolves the single unified (0::) cgroup v2 path for pid
// from /proc/<pid>/cgroup. ok=false on missing/malformed/non-unified entries
// or a PID that has exited.
func CgroupPathForPID(pid int) (path string, ok bool) {
	if pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", false
	}
	return parseCgroupProcFileContent(string(data))
}
