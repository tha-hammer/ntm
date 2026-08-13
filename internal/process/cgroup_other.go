//go:build !linux

package process

// CgroupPathForPID always reports ok=false on non-Linux platforms; cgroup
// v2 is a Linux-only kernel feature.
func CgroupPathForPID(pid int) (path string, ok bool) {
	return "", false
}
