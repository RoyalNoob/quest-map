package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		args     []string
		code     int
		inStdout string
		inStderr string
	}{
		{[]string{"version"}, 0, "quest-map dev", ""},
		{[]string{"help"}, 0, "Usage:", ""},
		{nil, 2, "", "Usage:"},
		{[]string{"export", "--format", "pdf"}, 1, "", "use md or json"},
		{[]string{"autostart", "maybe"}, 1, "", "on|off|status"},
		{[]string{"bogus"}, 2, "", "unknown command"},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		code := run(tt.args, &out, &errOut)
		if code != tt.code {
			t.Errorf("run(%v) = %d, want %d", tt.args, code, tt.code)
		}
		if !strings.Contains(out.String(), tt.inStdout) {
			t.Errorf("run(%v) stdout = %q, want it to contain %q", tt.args, out.String(), tt.inStdout)
		}
		if !strings.Contains(errOut.String(), tt.inStderr) {
			t.Errorf("run(%v) stderr = %q, want it to contain %q", tt.args, errOut.String(), tt.inStderr)
		}
	}
}
