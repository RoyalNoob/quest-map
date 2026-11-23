// Package install registers quest-map with agent clients.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeCode installs quest-map for Claude Code: the stdio MCP server (user scope) and the Stop hook that
// reports measured usage after each turn.
type ClaudeCode struct {
	Home string // user home folder
	Exe  string // absolute path of the quest-map binary
	// Board is the board folder to pass as --board to the MCP server and the hook; "" uses their default
	// (QUEST_MAP_BOARD, else ~/.quest-map).
	Board string
	// Claude runs the `claude` CLI; nil when it is not installed, in which case ~/.claude.json is edited.
	Claude func(args ...string) error
	Out    io.Writer
}

// HookCommand is the Stop hook command line for a binary and board ("" for the default board).
func HookCommand(exe, board string) string {
	cmd := `"` + filepath.ToSlash(exe) + `" usage-hook`
	if board != "" {
		cmd += ` --board "` + filepath.ToSlash(board) + `"`
	}
	return cmd
}

// mcpArgs are the arguments of the MCP server command.
func (c ClaudeCode) mcpArgs() []string {
	if c.Board != "" {
		return []string{"mcp", "--board", c.Board}
	}
	return []string{"mcp"}
}

// Install does both steps. It is safe to run again.
func (c ClaudeCode) Install() error {
	if c.Out == nil {
		c.Out = io.Discard
	}
	if !filepath.IsAbs(c.Exe) {
		return fmt.Errorf("quest-map binary path %q is not absolute", c.Exe)
	}
	if strings.Contains(strings.ToLower(filepath.ToSlash(c.Exe)), "/go-build") {
		return errors.New("this quest-map is a temporary `go run` build; build or install it first, then run install from the real binary")
	}
	if c.Board != "" && !filepath.IsAbs(c.Board) {
		return fmt.Errorf("board folder %q is not absolute", c.Board)
	}
	if err := c.addServer(); err != nil {
		return fmt.Errorf("register MCP server: %w", err)
	}
	if err := c.addHook(); err != nil {
		return fmt.Errorf("add usage hook: %w", err)
	}
	return nil
}

func (c ClaudeCode) addServer() error {
	if c.Claude != nil {
		c.Claude("mcp", "remove", "--scope", "user", "quest-map") // fine if it was not there
		if err := c.Claude(append([]string{"mcp", "add", "--scope", "user", "quest-map", "--", c.Exe}, c.mcpArgs()...)...); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Registered the quest-map MCP server for Claude Code (user scope).")
		return nil
	}
	path := filepath.Join(c.Home, ".claude.json")
	// Claude Code rewrites this file while it runs; editJSON re-reads it right before writing, but a running
	// Claude Code can still overwrite the change afterwards.
	fmt.Fprintf(c.Out, "Editing %s: close Claude Code first (it rewrites this file while running), or run install again after closing it.\n", path)
	args := []any{}
	for _, a := range c.mcpArgs() {
		args = append(args, a)
	}
	err := editJSON(path, func(cfg map[string]any) {
		servers, _ := cfg["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["quest-map"] = map[string]any{"type": "stdio", "command": c.Exe, "args": args, "env": map[string]any{}}
		cfg["mcpServers"] = servers
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Registered the quest-map MCP server in %s (the claude CLI was not found).\n", path)
	return nil
}

// isOurHook reports whether a hook command is a quest-map usage hook (of any binary path or board).
func isOurHook(cmd string) bool {
	return strings.Contains(cmd, "quest-map") && strings.Contains(cmd, " usage-hook")
}

func (c ClaudeCode) addHook() error {
	path := filepath.Join(c.Home, ".claude", "settings.json")
	want := HookCommand(c.Exe, c.Board)
	err := editJSON(path, func(cfg map[string]any) {
		hooks, _ := cfg["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		stop, _ := hooks["Stop"].([]any)
		// Drop earlier quest-map usage hooks (an older binary path), keep everything else.
		var kept []any
		for _, group := range stop {
			g, ok := group.(map[string]any)
			if !ok {
				kept = append(kept, group)
				continue
			}
			inner, _ := g["hooks"].([]any)
			var rest []any
			for _, h := range inner {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); isOurHook(cmd) {
						continue
					}
				}
				rest = append(rest, h)
			}
			if len(rest) > 0 || len(inner) == 0 {
				g["hooks"] = rest
				kept = append(kept, g)
			}
		}
		kept = append(kept, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": want, "timeout": 30}}})
		hooks["Stop"] = kept
		cfg["hooks"] = hooks
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Added the usage hook to %s: %s\n", path, want)
	return nil
}

// editJSON applies change to a JSON object file. Right before writing it reads the file again; if another
// program changed it in the meantime, the change is applied to the new content instead (a few tries).
func editJSON(path string, change func(map[string]any)) error {
	for try := 0; ; try++ {
		before, err := readFile(path)
		if err != nil {
			return err
		}
		cfg, err := decodeJSON(path, before)
		if err != nil {
			return err
		}
		change(cfg)
		now, err := readFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, now) && try < 3 {
			continue
		}
		return writeJSON(path, cfg, now)
	}
}

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// decodeJSON decodes a JSON object, keeping numbers as json.Number so large integers survive unchanged.
func decodeJSON(path string, data []byte) (map[string]any, error) {
	cfg := map[string]any{}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (fix or move it, then run install again): %w", path, err)
	}
	return cfg, nil
}

// writeJSON writes the file atomically, keeping one backup of the previous version (old).
func writeJSON(path string, cfg map[string]any, old []byte) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if old != nil {
		if err := os.WriteFile(path+".quest-map.bak", old, 0o600); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
