package vcs

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console window from flashing up when the server (which may have none) runs a tool.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
