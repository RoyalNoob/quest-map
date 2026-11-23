//go:build !windows

package mcpproxy

import (
	"os/exec"
	"syscall"
)

// startDetached starts the server in its own session, so it outlives the agent.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}
