//go:build !windows

package vcs

import "os/exec"

func hideWindow(*exec.Cmd) {}
