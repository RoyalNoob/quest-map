package main

import "testing"

func TestAutostartCommandQuoting(t *testing.T) {
	for root, want := range map[string]string{
		`C:\`:                    `"C:\Program Files\qm.exe" restart --board C:\`,
		`D:\My Boards\`:          `"C:\Program Files\qm.exe" restart --board "D:\My Boards\\"`,
		`C:\Users\me\.quest-map`: `"C:\Program Files\qm.exe" restart --board C:\Users\me\.quest-map`,
	} {
		if got := autostartCommand(`C:\Program Files\qm.exe`, root); got != want {
			t.Errorf("autostartCommand(%q) = %s, want %s", root, got, want)
		}
	}
}
