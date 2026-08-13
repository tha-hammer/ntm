//go:build windows

package pressure

import (
	"os"
	"path/filepath"
)

// acquireMemorySampleLock acquires the in-process mutex only. Windows file
// locking is complex and this store's only cross-process writers are
// internal-monitor processes, which are rare and short-lived enough that
// the plain mutex (correct within one process) plus atomic rename (correct
// enough across processes for a best-effort store) is an acceptable
// simplification — the same trade-off internal/session/lock_windows.go
// already makes.
func acquireMemorySampleLock(bool) (func(), error) {
	memorySampleStoreMu.Lock()

	if err := os.MkdirAll(filepath.Dir(MemorySampleStorePath()), 0755); err != nil {
		memorySampleStoreMu.Unlock()
		return nil, err
	}

	return func() {
		memorySampleStoreMu.Unlock()
	}, nil
}
