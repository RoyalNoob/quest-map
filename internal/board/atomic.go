package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// WriteGate holds board file writes back while a board sync commits or merges (spec §3.5): writers take it
// shared around each file operation, the sync takes it alone, so git never commits or merges over a write in
// progress. ponytail: one gate for every board in the process; make it per board if several boards contend.
var WriteGate sync.RWMutex

var lastWrite atomic.Int64

// LastWrite is when quest-map last wrote a board file (zero if it has not since the process started).
func LastWrite() time.Time {
	if n := lastWrite.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// gated runs one board file operation under WriteGate and notes the time.
func gated(op func() error) error {
	WriteGate.RLock()
	defer WriteGate.RUnlock()
	lastWrite.Store(time.Now().UnixNano())
	return op()
}

// renameRetryFor is how long a failing rename is retried on Windows, where another process (an editor,
// antivirus, a search indexer) holding the target open makes the rename fail for a moment.
var renameRetryFor = 2 * time.Second

// WriteFileAtomic is writeFileAtomic for other packages (plugin storage, §8.5).
func WriteFileAtomic(path string, data []byte) error { return writeFileAtomic(path, data, 0o644) }

// RemoveFile removes a file, retrying on Windows; a file that is already gone counts as removed.
func RemoveFile(path string) error { return removeWithRetry(path) }

// writeFileAtomic writes data to path by writing a temp file in the same folder and renaming it over path,
// so readers never see a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	return gated(func() error { return writeFileAtomicNow(path, data, perm) })
}

func writeFileAtomicNow(path string, data []byte, perm os.FileMode) error {
	dir, base := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := renameWithRetry(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// renameWithRetry renames oldpath to newpath, retrying with backoff on Windows for up to renameRetryFor.
func renameWithRetry(oldpath, newpath string) error {
	return withRetry("rename "+filepath.Base(newpath), func() error { return os.Rename(oldpath, newpath) })
}

// removeWithRetry removes a file, retrying with backoff on Windows for up to renameRetryFor. A file that is
// already gone counts as removed.
func removeWithRetry(path string) error {
	return gated(func() error { return removeNow(path) })
}

func removeNow(path string) error {
	return withRetry("remove "+filepath.Base(path), func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// withRetry runs op, and on Windows runs it again with backoff until it succeeds or renameRetryFor passes.
func withRetry(what string, op func() error) error {
	err := op()
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	deadline := time.Now().Add(renameRetryFor)
	wait := 10 * time.Millisecond
	for time.Now().Before(deadline) {
		time.Sleep(wait)
		if err = op(); err == nil {
			return nil
		}
		if wait < 200*time.Millisecond {
			wait *= 2
		}
	}
	return fmt.Errorf("%s: still failing after %s: %w", what, renameRetryFor, err)
}
