//go:build unix

package pressure

import (
	"os"
	"path/filepath"
	"syscall"
)

// acquireMemorySampleLock acquires both the in-process mutex and an
// OS-level flock on a sibling lock file, mirroring
// internal/history/lock_unix.go. shared=true takes LOCK_SH (Load — many
// readers, including across processes, may proceed concurrently);
// shared=false takes LOCK_EX (Append — exclusive, spanning its
// read-modify-write-rename).
func acquireMemorySampleLock(shared bool) (func(), error) {
	memorySampleStoreMu.Lock()

	path := MemorySampleStorePath()
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		memorySampleStoreMu.Unlock()
		return nil, err
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		memorySampleStoreMu.Unlock()
		return nil, err
	}

	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		memorySampleStoreMu.Unlock()
		return nil, err
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		memorySampleStoreMu.Unlock()
	}, nil
}
