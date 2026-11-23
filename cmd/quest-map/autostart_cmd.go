package main

import (
	"fmt"
	"io"
	"os"
)

// cmdAutostart turns starting the server at login on or off for the current user (spec §9): a Windows
// HKCU\…\Run entry or a Linux systemd user unit. "status" shows what is registered.
func cmdAutostart(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "quest-map autostart:", err)
		return 1
	}
	root, rest, err := boardRoot(args)
	if err != nil {
		return fail(err)
	}
	if len(rest) != 1 || (rest[0] != "on" && rest[0] != "off" && rest[0] != "status") {
		return fail(fmt.Errorf("use: quest-map autostart on|off|status [--board DIR]"))
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	switch rest[0] {
	case "on":
		where, err := autostartOn(exe, root)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "the quest-map server for %s now starts at login (%s)\n", root, where)
	case "off":
		removed, err := autostartOff()
		if err != nil {
			return fail(err)
		}
		if removed {
			fmt.Fprintln(stdout, "the quest-map server no longer starts at login; a running server keeps running (quest-map stop)")
		} else {
			fmt.Fprintln(stdout, "autostart was not on")
		}
	default:
		cmd, err := autostartStatus()
		if err != nil {
			return fail(err)
		}
		if cmd == "" {
			fmt.Fprintln(stdout, "autostart is off")
		} else {
			fmt.Fprintln(stdout, "autostart is on:", cmd)
		}
	}
	return 0
}
