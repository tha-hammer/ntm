//go:build linux

package process

import (
	"os"
	"testing"
)

func TestCgroupPathForPID_CurrentProcess(t *testing.T) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skipf("cannot read /proc/self/cgroup: %v", err)
	}
	wantPath, wantOK := parseCgroupProcFileContent(string(data))
	if !wantOK {
		t.Skip("this host's own cgroup is not a plain unified (0::) mount")
	}

	gotPath, gotOK := CgroupPathForPID(os.Getpid())
	if !gotOK {
		t.Fatalf("ok = false, want true for the current process")
	}
	if gotPath != wantPath {
		t.Fatalf("path = %q, want %q", gotPath, wantPath)
	}
}

func TestCgroupPathForPID_NonexistentPID(t *testing.T) {
	// pid_max defaults to 4194304 on Linux, so a PID this large is
	// guaranteed to have no /proc entry - no reused-PID race is possible.
	const impossiblePID = 999999999
	if _, ok := CgroupPathForPID(impossiblePID); ok {
		t.Fatalf("ok = true, want false for a PID with no /proc entry")
	}
}

func TestCgroupPathForPID_InvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if _, ok := CgroupPathForPID(pid); ok {
			t.Fatalf("ok = true for pid %d, want false", pid)
		}
	}
}
