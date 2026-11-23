package web

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// TestReportsMatchFixture runs the reports plugin's numbers against a hand calculation
// (plugintest/reports.test.mjs) with Node. Skipped where node is not installed; CI runners have it.
func TestReportsMatchFixture(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	// Name the files: Node 22 and 24 read a folder argument as one module to load and fail on it.
	files, err := filepath.Glob("plugintest/*.test.mjs")
	if err != nil || len(files) == 0 {
		t.Fatalf("no plugin tests in plugintest/: %v", err)
	}
	cmd := exec.Command(node, append([]string{"--test"}, files...)...)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}

// TestBuiltinPluginsUseOnlyTheAPI: built-in plugins reach the board only through the plugin API (spec §8.5),
// never by talking to the server or the page themselves.
func TestBuiltinPluginsUseOnlyTheAPI(t *testing.T) {
	banned := regexp.MustCompile(`\bfetch\(|XMLHttpRequest|WebSocket|EventSource|\bparent\b|\btop\b\.|postMessage|document\.cookie|localStorage|/api/`)
	n := 0
	fs.WalkDir(Files, "plugins", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !regexp.MustCompile(`\.m?js$`).MatchString(p) {
			return err
		}
		n++
		data, _ := fs.ReadFile(Files, p)
		if m := banned.Find(data); m != nil {
			t.Errorf("%s uses %q: go through the plugin API", p, m)
		}
		return nil
	})
	if n == 0 {
		t.Fatal("no built-in plugin scripts found")
	}
}
