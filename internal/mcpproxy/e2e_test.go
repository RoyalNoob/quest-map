package mcpproxy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// agent is one MCP client talking through its own proxy, like one Claude Code process.
type agent struct {
	cs   *mcp.ClientSession
	done chan error
}

func startAgent(t *testing.T, ctx context.Context, root, cwd, clientSession string) *agent {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	a := &agent{done: make(chan error, 1)}
	env := map[string]string{"CLAUDE_CODE_SESSION_ID": clientSession, "CLAUDE_PID": "4242"}
	go func() {
		a.done <- Run(ctx, Options{Root: root, Stdin: inR, Stdout: outW, Cwd: cwd, Getenv: func(k string) string { return env[k] }})
		outW.Close()
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "2.4.1"}, nil)
	cs, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	a.cs = cs
	t.Cleanup(func() { cs.Close() })
	return a
}

func (a *agent) call(t *testing.T, ctx context.Context, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := a.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// TestTwoAgentsInOneRepo is the M3 acceptance check: two agents in the same git repo call task_claim at once
// and get different tasks, and each can claim → note → review without passing project. It also checks the
// server instructions and that a Claude Code session's measured usage lands on the task it claimed.
func TestTwoAgentsInOneRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	p.LinkRepo("git:github.com/acme/rpg")
	for i := 1; i <= 3; i++ {
		p.Create(&board.Task{Title: fmt.Sprint("job ", i), Doer: board.Agent}, board.Actor{Kind: "person", Name: "me"}, nil)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:acme/rpg.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	srv, err := server.Start(root, server.Options{Port: -1, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a1 := startAgent(t, ctx, root, filepath.Join(repo), "sess-one")
	a2 := startAgent(t, ctx, root, filepath.Join(repo), "sess-two")

	if init := a1.cs.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "task_claim") {
		t.Errorf("instructions missing: %+v", init)
	}

	var wg sync.WaitGroup
	claimed := make([]string, 2)
	for i, a := range []*agent{a1, a2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text, isErr := a.call(t, ctx, "task_claim", map[string]any{})
			if isErr || !strings.HasPrefix(text, "Claimed RPG-") {
				t.Errorf("claim %d: %q", i, text)
				return
			}
			claimed[i] = strings.Fields(text)[1]
		}()
	}
	wg.Wait()
	if claimed[0] == "" || claimed[0] == claimed[1] {
		t.Fatalf("claims: %v", claimed)
	}

	for i, a := range []*agent{a1, a2} {
		id := claimed[i]
		if text, isErr := a.call(t, ctx, "task_note", map[string]any{"id": id, "text": "working on it", "model": "claude-opus-5-5"}); isErr {
			t.Errorf("note: %s", text)
		}
		text, isErr := a.call(t, ctx, "task_edit", map[string]any{"id": id, "status": "review", "note": "done; resume by re-running the test"})
		if isErr || !strings.Contains(text, id+" review") {
			t.Errorf("edit: %q", text)
		}
	}
	// Errors come back as tool errors the agent can read.
	if text, isErr := a1.call(t, ctx, "task_find", map[string]any{"filter": "stauts:doing"}); !isErr || !strings.Contains(text, "unknown filter") {
		t.Errorf("bad filter: %q %v", text, isErr)
	}

	// Measured usage: the usage hook for client session "sess-one" goes to a1's claim.
	a1.call(t, ctx, "task_edit", map[string]any{"id": claimed[0], "status": "todo"})
	text, _ := a1.call(t, ctx, "task_claim", map[string]any{})
	id := strings.Fields(text)[1]
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tr, []byte(`{"type":"user","timestamp":"2026-11-18T10:00:00Z","message":{"content":"go"}}
{"type":"assistant","timestamp":"2026-11-18T10:03:00Z","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":100,"output_tokens":900,"cache_read_input_tokens":5000,"cache_creation_input_tokens":0}}}
`), 0o644)
	info := srv.Info()
	var res mcpserver.HookResult
	if err := server.CallJSON(&info, "POST", "/api/usage-hook", mcpserver.HookRequest{Client: "claude-code", ClientSession: "sess-one", TranscriptPath: tr, Cwd: repo}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Task != id || res.Tokens != 6000 || res.Seconds != 180 {
		t.Errorf("hook: %+v (claimed %s)", res, id)
	}
	text, _ = a1.call(t, ctx, "task_show", map[string]any{"id": id})
	if !strings.Contains(text, "spent 6k tok, 3m") {
		t.Errorf("show: %q", text)
	}
}

func TestEnsureServerNeverStartsATestBinary(t *testing.T) {
	_, err := EnsureServer(t.TempDir(), os.Args[0], time.Second)
	if err == nil || !strings.Contains(err.Error(), "test binary") {
		t.Errorf("err = %v", err)
	}
}

// TestProxySurvivesServerRestart: the server stops and a new one starts (on another port); the agent's next
// call goes through, under the same agent session, so its claim is still its own.
func TestProxySurvivesServerRestart(t *testing.T) {
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	p.Create(&board.Task{Title: "job", Doer: board.Agent}, board.Actor{Kind: "person", Name: "me"}, nil)
	srv, err := server.Start(root, server.Options{Port: -1, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a := startAgent(t, ctx, root, t.TempDir(), "sess-restart")
	text, isErr := a.call(t, ctx, "task_claim", map[string]any{"project": "rpg"})
	if isErr || !strings.HasPrefix(text, "Claimed RPG-1") || !strings.Contains(text, mcpserver.SessionMarker+"claude-code:") {
		t.Fatalf("claim: %q", text)
	}
	srv.Close()
	srv2, err := server.Start(root, server.Options{Port: -1, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()
	text, isErr = a.call(t, ctx, "task_note", map[string]any{"id": "RPG-1", "text": "still mine"})
	if isErr {
		// One failed call is allowed (it was in flight across the restart); the next must work.
		text, isErr = a.call(t, ctx, "task_note", map[string]any{"id": "RPG-1", "text": "still mine"})
	}
	if isErr || !strings.Contains(text, "RPG-1 doing") {
		t.Fatalf("note after restart: %q %v", text, isErr)
	}
	text, isErr = a.call(t, ctx, "task_edit", map[string]any{"id": "RPG-1", "status": "review", "note": "handed off"})
	if isErr || !strings.Contains(text, "RPG-1 review") {
		t.Errorf("edit after restart (claim lost?): %q", text)
	}
}
