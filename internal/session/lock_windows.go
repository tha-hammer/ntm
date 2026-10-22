//go:build windows

package session

import (
	"os"
	"sync"
)

var localMu sync.Mutex

// acquireLock acquires thread-level (mutex) lock only.
// Windows file locking is complex, skipping for now (CLI usage is low concurrency).
func acquireLock() (func(), error) {
	return acquireLockAt(StorageDir())
}

// acquireLockAt is the dir-taking core behind acquireLock so tests can
// exercise lock-serialized read-modify-write cycles in a temp dir.
func acquireLockAt(dir string) (func(), error) {
	localMu.Lock()

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		localMu.Unlock()
		return nil, err
	}

	return func() {
		localMu.Unlock()
	}, nil
}
