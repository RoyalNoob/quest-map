package main

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "quest-map"
)

// autostartCommand is what the Run entry executes at login: `restart` starts the server detached and exits.
// ponytail: the short-lived restart process shows a console window for a moment at login; a GUI-subsystem
// launcher would hide it if that bothers anyone.
func autostartCommand(exe, root string) string {
	return syscall.EscapeArg(exe) + " restart --board " + syscall.EscapeArg(root) // a trailing \ stays a path
}

func autostartOn(exe, root string) (string, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	if err := k.SetStringValue(runValue, autostartCommand(exe, root)); err != nil {
		return "", err
	}
	return `HKCU\` + runKey + `\` + runValue, nil
}

func autostartOff() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	if err := k.DeleteValue(runValue); errors.Is(err, registry.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func autostartStatus() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}
