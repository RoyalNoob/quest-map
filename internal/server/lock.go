package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

// Info is the content of .run/server.json: how to reach the running server.
type Info struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// ErrRunning is returned when another process holds the board lock.
type ErrRunning struct {
	Info *Info // nil if server.json could not be read
}

func (e *ErrRunning) Error() string {
	if e.Info == nil {
		return "another quest-map server is already using this board"
	}
	return fmt.Sprintf("another quest-map server is already using this board (pid %d, port %d)", e.Info.PID, e.Info.Port)
}

func runDir(root string) string   { return filepath.Join(root, ".run") }
func infoPath(root string) string { return filepath.Join(runDir(root), "server.json") }

// Lock is the exclusive OS lock on .run/board.lock held by the one process that may write the board.
type Lock struct {
	fl *flock.Flock
}

// AcquireLock takes the board lock without waiting. If another process holds it, the error is *ErrRunning.
func AcquireLock(root string) (*Lock, error) {
	if err := os.MkdirAll(runDir(root), 0o700); err != nil {
		return nil, err
	}
	if err := restrictDir(runDir(root)); err != nil {
		return nil, fmt.Errorf("make %s private: %w", runDir(root), err)
	}
	fl := flock.New(filepath.Join(runDir(root), "board.lock"))
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("board lock: %w", err)
	}
	if !ok {
		info, _ := ReadInfo(root)
		return nil, &ErrRunning{Info: info}
	}
	return &Lock{fl: fl}, nil
}

// Release gives the lock up.
func (l *Lock) Release() error { return l.fl.Unlock() }

// newToken returns a random 32-byte hex token.
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// WriteInfo writes .run/server.json, readable by the current user only: .run is made private when the lock
// is taken (restrictDir), and on Linux the file itself is 0600.
func WriteInfo(root string, info Info) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := infoPath(root) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	// On Windows the rename fails while a CLI has the old file open to read it; retry for a moment.
	err = os.Rename(tmp, infoPath(root))
	for wait, deadline := 10*time.Millisecond, time.Now().Add(2*time.Second); err != nil && runtime.GOOS == "windows" && time.Now().Before(deadline); wait = min(wait*2, 200*time.Millisecond) {
		time.Sleep(wait)
		err = os.Rename(tmp, infoPath(root))
	}
	return err
}

// ReadInfo reads .run/server.json. It does not check that the server is still running; use Running.
func ReadInfo(root string) (*Info, error) {
	data, err := os.ReadFile(infoPath(root))
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("server.json: %w", err)
	}
	return &info, nil
}

func removeInfo(root string) { os.Remove(infoPath(root)) }
