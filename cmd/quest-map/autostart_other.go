//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitName = "quest-map.service"

func unitPath() (string, error) {
	dir, err := os.UserConfigDir() // $XDG_CONFIG_HOME or ~/.config
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "systemd", "user", unitName), nil
}

// unitFile is the systemd user unit that runs the server for one board.
func unitFile(exe, root string) string {
	return fmt.Sprintf(`[Unit]
Description=quest-map board server (%[2]s)

[Service]
ExecStart=%[1]s serve --board %[2]s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, systemdQuote(exe), systemdQuote(root))
}

// systemdQuote quotes a path for a unit file's command line.
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s) + `"`
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func autostartOn(exe, root string) (string, error) {
	path, err := unitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(unitFile(exe, root)), 0o644); err != nil {
		return "", err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return "", err
	}
	// enable, not --now: a server may already be running for this board; it is used until the next login.
	if err := systemctl("enable", unitName); err != nil {
		return "", err
	}
	return "systemd user unit " + path, nil
}

func autostartOff() (bool, error) {
	path, err := unitPath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err := systemctl("disable", unitName); err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, systemctl("daemon-reload")
}

func autostartStatus() (string, error) {
	path, err := unitPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "systemd user unit " + path, nil
}
