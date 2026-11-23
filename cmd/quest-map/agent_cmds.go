package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/install"
	"github.com/RoyalNoob/quest-map/internal/mcpproxy"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// cmdMCP runs the stdio MCP proxy. stdout carries MCP messages only; diagnostics go to stderr.
func cmdMCP(args []string, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("unexpected arguments %q (mcp takes only --board DIR)", rest)
	}
	if err != nil {
		fmt.Fprintln(stderr, "quest-map mcp:", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "quest-map mcp:", err)
		return 1
	}
	cwd, _ := os.Getwd()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = mcpproxy.Run(ctx, mcpproxy.Options{Root: root, Exe: exe, Stdin: os.Stdin, Stdout: os.Stdout, Cwd: cwd})
	if err != nil {
		fmt.Fprintln(stderr, "quest-map mcp:", err)
		return 1
	}
	return 0
}

// cmdInstall registers quest-map with a client. With --board DIR (or QUEST_MAP_BOARD) naming a board other
// than ~/.quest-map, the MCP server and the usage hook are registered with that --board.
func cmdInstall(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map install:", err)
		return 1
	}
	if len(rest) != 1 || (rest[0] != "claude-code" && rest[0] != "claude") {
		fmt.Fprintln(stderr, "usage: quest-map install claude-code [--board DIR]")
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "quest-map install:", err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "quest-map install:", err)
		return 1
	}
	inst := install.ClaudeCode{Home: home, Exe: exe, Out: stdout}
	if def, _ := filepath.Abs(filepath.Join(home, ".quest-map")); !sameDir(root, def) {
		inst.Board = root
	}
	if claude, err := exec.LookPath("claude"); err == nil {
		inst.Claude = func(a ...string) error {
			cmd := exec.Command(claude, a...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("claude %v: %v: %s", a, err, out)
			}
			return nil
		}
	}
	if err := inst.Install(); err != nil {
		fmt.Fprintln(stderr, "quest-map install:", err)
		return 1
	}
	fmt.Fprintln(stdout, "Done. Restart Claude Code, then ask it to use quest-map (try: \"claim a task from quest-map\").")
	return 0
}

// cmdUsageHook is Claude Code's Stop hook: it passes the session's transcript to the running server, which
// records the turn's measured usage. It never fails the client's turn: problems are only printed.
func cmdUsageHook(args []string, stdin io.Reader, stderr io.Writer) int {
	root, _, err := boardRoot(args)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map usage-hook:", err)
		return 0
	}
	var in struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		fmt.Fprintln(stderr, "quest-map usage-hook: reading hook input:", err)
		return 0
	}
	info := server.Running(root)
	if info == nil {
		return 0 // no board server: this session did not use quest-map; the transcript is counted later
	}
	req := mcpserver.HookRequest{Client: "claude-code", ClientSession: in.SessionID, TranscriptPath: in.TranscriptPath, Cwd: in.Cwd}
	// Well under the hook's own timeout (30 s, set by install), so the client never has to kill the hook.
	if err := server.CallJSONTimeout(info, "POST", "/api/usage-hook", req, nil, hookCallTimeout); err != nil {
		fmt.Fprintln(stderr, "quest-map usage-hook:", err)
	}
	return 0
}

// hookCallTimeout bounds the usage hook's call to the server.
const hookCallTimeout = 10 * time.Second

// sameDir compares two cleaned absolute folder paths, ignoring case on Windows.
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
