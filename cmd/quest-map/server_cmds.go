package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/RoyalNoob/quest-map/internal/mcpproxy"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// cmdStop stops the board's server: it asks the server to shut down (so it finishes its writes, removes
// server.json and releases the lock), and ends the process only if that does not work – a server from an
// older build, or one that hangs.
func cmdStop(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("unexpected arguments %q (stop takes only --board DIR)", rest)
	}
	if err != nil {
		fmt.Fprintln(stderr, "quest-map stop:", err)
		return 1
	}
	pid, err := stopServer(root)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map stop:", err)
		return 1
	}
	if pid == 0 {
		fmt.Fprintf(stdout, "no quest-map server is running for %s\n", root)
	} else {
		fmt.Fprintf(stdout, "stopped the quest-map server for %s (pid %d)\n", root, pid)
	}
	return 0
}

// cmdRestart stops the board's server if one runs and starts it again in the background from this binary,
// the way `quest-map mcp` starts it. Use it after installing a new build.
func cmdRestart(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("unexpected arguments %q (restart takes only --board DIR)", rest)
	}
	if err != nil {
		fmt.Fprintln(stderr, "quest-map restart:", err)
		return 1
	}
	if pid, err := stopServer(root); err != nil {
		fmt.Fprintln(stderr, "quest-map restart:", err)
		return 1
	} else if pid != 0 {
		fmt.Fprintf(stdout, "stopped the quest-map server (pid %d)\n", pid)
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "quest-map restart:", err)
		return 1
	}
	info, err := mcpproxy.EnsureServer(root, exe, 20*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map restart:", err)
		return 1
	}
	fmt.Fprintf(stdout, "quest-map serving %s on http://127.0.0.1:%d (pid %d; log: %s)\n", root, info.Port, info.PID,
		filepath.Join(root, ".run", "serve.log"))
	return 0
}

// stopServer stops the server holding the board lock and waits until the lock is free. It returns the pid
// that was stopped, or 0 if no server was running.
func stopServer(root string) (int, error) {
	if lockFree(root) {
		return 0, nil // also when a crashed server left server.json behind
	}
	info, err := server.ReadInfo(root)
	if err != nil {
		return 0, errors.New("another process holds the board lock but left no .run/server.json; stop it by hand")
	}
	if err := server.CallJSONTimeout(info, http.MethodPost, "/api/shutdown", nil, nil, 5*time.Second); err == nil {
		if waitLockFree(root, 15*time.Second) {
			return info.PID, nil
		}
	}
	// A server from a build without /api/shutdown, or one that did not stop: end the process. Only while the
	// lock is still held, so a pid that has since been reused is not touched.
	if !lockFree(root) {
		if p, err := os.FindProcess(info.PID); err == nil {
			p.Kill()
		}
		if !waitLockFree(root, 5*time.Second) {
			return info.PID, fmt.Errorf("the server (pid %d) still holds the board lock", info.PID)
		}
	}
	return info.PID, nil
}

// lockFree reports whether no process holds the board lock (taking and releasing it to find out).
func lockFree(root string) bool {
	l, err := server.AcquireLock(root)
	if err != nil {
		return false
	}
	l.Release()
	return true
}

func waitLockFree(root string, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if lockFree(root) {
			return true
		}
	}
	return false
}
