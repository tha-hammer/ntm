package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupRoot is the mount point of the unified cgroup v2 hierarchy. It is a
// package var so tests can point it at a fixture directory instead of the
// real /sys/fs/cgroup.
var cgroupRoot = "/sys/fs/cgroup"

// parseCgroupProcFileContent parses the content of a /proc/<pid>/cgroup file
// and returns the single unified (0::) cgroup v2 path. ok=false if the
// content has no 0:: line, an empty path, more than one 0:: line, or any
// non-zero hierarchy line (a legacy or hybrid cgroup v1 mount, which this
// package does not support).
func parseCgroupProcFileContent(content string) (path string, ok bool) {
	var unifiedPath string
	found := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] != "0" || parts[1] != "" {
			return "", false
		}
		if found {
			return "", false
		}
		unifiedPath = parts[2]
		found = true
	}
	if !found || unifiedPath == "" {
		return "", false
	}
	return unifiedPath, true
}

// ReadCgroupMemoryPeakBytes reads memory.peak under cgroupRoot+path, falling
// back to memory.current if memory.peak doesn't exist (older kernels). ok is
// false on any read/parse failure or an unsigned-overflow value.
func ReadCgroupMemoryPeakBytes(path string) (bytes uint64, ok bool) {
	for _, file := range []string{"memory.peak", "memory.current"} {
		data, err := os.ReadFile(filepath.Join(cgroupRoot, path, file))
		if err != nil {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			continue
		}
		return v, true
	}
	return 0, false
}
