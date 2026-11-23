package mcpproxy

import (
	"errors"
	"os/exec"
	"syscall"
)

const (
	detachedProcess        = 0x00000008
	newProcessGroup        = 0x00000200
	createBreakawayFromJob = 0x01000000
	createNoWindow         = 0x08000000
)

// startDetached starts the server outside the agent's console, process group and job object, so it outlives
// the agent (a client that runs its tools in a job would otherwise take the server down with it). A job
// that does not allow breakaway makes CreateProcess fail with access denied; then the server is started
// inside the job.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | newProcessGroup | createNoWindow | createBreakawayFromJob}
	err := cmd.Start()
	if err != nil && errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		cmd = newCmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | newProcessGroup | createNoWindow}
		err = cmd.Start()
	}
	return cmd, err
}
