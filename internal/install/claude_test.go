package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallClaudeCodeWithoutCLI(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "bin", "quest-map.exe")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	// Existing settings with someone else's Stop hook and an older quest-map hook.
	existing := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"notify-send done"}]},
	  {"hooks":[{"type":"command","command":"\"/old/quest-map\" usage-hook"}]}]}}`
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(existing), 0o644)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups":3,"mcpServers":{"other":{"command":"x"}}}`), 0o644)

	inst := ClaudeCode{Home: home, Exe: exe}
	for i := 0; i < 2; i++ { // twice: must stay the same
		if err := inst.Install(); err != nil {
			t.Fatal(err)
		}
	}
	var settings struct {
		Theme string `json:"theme"`
		Hooks struct {
			Stop []struct {
				Hooks []struct{ Command string } `json:"hooks"`
			} `json:"Stop"`
		} `json:"hooks"`
	}
	data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	json.Unmarshal(data, &settings)
	var cmds []string
	for _, g := range settings.Hooks.Stop {
		for _, h := range g.Hooks {
			cmds = append(cmds, h.Command)
		}
	}
	want := HookCommand(exe, "")
	if settings.Theme != "dark" || len(cmds) != 2 || cmds[0] != "notify-send done" || cmds[1] != want {
		t.Errorf("settings = %s", data)
	}
	var cfg map[string]any
	data, _ = os.ReadFile(filepath.Join(home, ".claude.json"))
	json.Unmarshal(data, &cfg)
	servers := cfg["mcpServers"].(map[string]any)
	qm, _ := servers["quest-map"].(map[string]any)
	if cfg["numStartups"] != float64(3) || servers["other"] == nil || qm["command"] != exe || qm["args"].([]any)[0] != "mcp" {
		t.Errorf(".claude.json = %s", data)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json.quest-map.bak")); err != nil {
		t.Error("no backup")
	}
}

func TestInstallUsesClaudeCLI(t *testing.T) {
	home := t.TempDir()
	var calls []string
	inst := ClaudeCode{Home: home, Exe: filepath.Join(home, "quest-map"), Claude: func(a ...string) error {
		calls = append(calls, strings.Join(a, " "))
		return nil
	}}
	if err := inst.Install(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "mcp add --scope user quest-map -- ") || !strings.HasSuffix(calls[1], " mcp") {
		t.Errorf("calls = %q", calls)
	}
}

func TestInstallRefusesGoRunBinary(t *testing.T) {
	err := ClaudeCode{Home: t.TempDir(), Exe: filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "quest-map")}.Install()
	if err == nil || !strings.Contains(err.Error(), "go run") {
		t.Errorf("err = %v", err)
	}
}

func TestInstallKeepsLargeNumbersAndPassesBoard(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "bin", "quest-map")
	board := filepath.Join(home, "boards", "team")
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"userID":12345678901234567890,"firstStartTime":1731900000123}`), 0o644)
	if err := (ClaudeCode{Home: home, Exe: exe, Board: board}).Install(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(data), "12345678901234567890") || !strings.Contains(string(data), "1731900000123") {
		t.Errorf("numbers changed: %s", data)
	}
	if !strings.Contains(string(data), `"--board"`) {
		t.Errorf("no --board in MCP args: %s", data)
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(string(settings), "usage-hook --board") {
		t.Errorf("hook without board: %s", settings)
	}
	// Installing again for the default board replaces the hook rather than adding a second one.
	if err := (ClaudeCode{Home: home, Exe: exe}).Install(); err != nil {
		t.Fatal(err)
	}
	settings, _ = os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if n := strings.Count(string(settings), "usage-hook"); n != 1 {
		t.Errorf("%d usage hooks: %s", n, settings)
	}
}
