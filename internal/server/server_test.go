package server

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
)

// A board inside a Lore workspace is refused (spec §3.1): its files are read-only until checked out.
func TestBoardInsideLoreWorkspaceRefused(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".lore"), 0o755)
	os.WriteFile(filepath.Join(ws, ".lore", "config.toml"), []byte("remote_url = \"\"\n"), 0o644)
	root := filepath.Join(ws, "board")
	if _, err := Start(root, Options{Port: -1}); err == nil || !strings.Contains(err.Error(), "Lore workspace") {
		t.Fatalf("Start inside a Lore workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".run")); err == nil {
		t.Error("refused board still got a .run folder")
	}
}

func TestSecondServerFailsCleanly(t *testing.T) {
	root := t.TempDir()
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Start(root, Options{Port: -1})
	var running *ErrRunning
	if !errors.As(err, &running) || running.Info == nil || running.Info.PID != os.Getpid() {
		t.Fatalf("second start: %v", err)
	}
	if !strings.Contains(err.Error(), "already using this board") {
		t.Errorf("message: %v", err)
	}
	if info := Running(root); info == nil || info.Port != s.Info().Port {
		t.Errorf("Running = %+v", info)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if Running(root) != nil {
		t.Error("still running after Close")
	}
	s2, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	s2.Close()
}

// TestSecondProcessFailsCleanly is the M2 acceptance check across processes: while one process serves the
// board, a second one gets ErrRunning; once the first stops, the second can serve.
func TestSecondProcessFailsCleanly(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperServe$")
	cmd.Env = append(os.Environ(), "QM_HELPER_ROOT="+root)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "ready ") {
				ready <- strings.TrimPrefix(sc.Text(), "ready ")
				return
			}
		}
		close(ready)
	}()
	var pid string
	select {
	case pid = <-ready:
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatal("helper did not start")
	}
	_, err := Start(root, Options{Port: -1})
	var running *ErrRunning
	if !errors.As(err, &running) {
		t.Fatalf("start while another process serves: %v", err)
	}
	if running.Info == nil || strconv.Itoa(running.Info.PID) != pid {
		t.Errorf("reported server %+v, helper pid %s", running.Info, pid)
	}
	stdin.Close() // helper stops
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatalf("start after helper stopped: %v", err)
	}
	s.Close()
}

// TestHelperServe runs in the helper process of TestSecondProcessFailsCleanly.
func TestHelperServe(t *testing.T) {
	root := os.Getenv("QM_HELPER_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("ready " + strconv.Itoa(os.Getpid()) + "\n")
	io.Copy(io.Discard, os.Stdin) // until the parent closes stdin
	s.Close()
}

func TestHTTPGuards(t *testing.T) {
	root := t.TempDir()
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	port := strconv.Itoa(s.Info().Port)
	do := func(method, host, origin, token string) int {
		req, _ := http.NewRequest(method, "http://127.0.0.1:"+port+"/api/health", nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	tok := s.Info().Token
	for _, c := range []struct {
		name                        string
		method, host, origin, token string
		want                        int
	}{
		{"ok", "GET", "127.0.0.1:" + port, "", tok, 200},
		{"localhost ok", "GET", "localhost:" + port, "", tok, 200},
		{"rebinding host", "GET", "evil.example:" + port, "", tok, 403},
		{"no token", "GET", "127.0.0.1:" + port, "", "", 401},
		{"wrong token", "GET", "127.0.0.1:" + port, "", "nope", 401},
		{"cross-origin post", "POST", "127.0.0.1:" + port, "https://evil.example", tok, 403},
		{"same-origin post", "POST", "127.0.0.1:" + port, "http://127.0.0.1:" + port, tok, 405},
		{"host in capitals", "GET", "LOCALHOST:" + port, "", tok, 200},
	} {
		if got := do(c.method, c.host, c.origin, c.token); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
	// A POST without an Origin header (a CLI, an agent) reaches the route.
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+port+"/api/reindex", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("POST without Origin: status %d", resp.StatusCode)
	}
}

func TestFindAndReindexOverHTTP(t *testing.T) {
	root := t.TempDir()
	b, _ := board.Open(root)
	p, _ := b.CreateProject("rpg", "RPG", "RPG")
	p.Create(&board.Task{Title: "Bridge collision", Labels: []string{"physics"}}, board.Actor{Kind: "person", Name: "me"}, nil)

	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info := s.Info()
	var rows []index.Row
	if err := Call(&info, http.MethodGet, "/api/tasks?q=label%3Aphysics", &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "RPG-1" {
		t.Errorf("rows = %+v", rows)
	}
	if err := Call(&info, http.MethodGet, "/api/tasks?q=stauts%3Adoing", nil); err == nil || !strings.Contains(err.Error(), "unknown filter") {
		t.Errorf("bad filter err = %v", err)
	}
	if err := Call(&info, http.MethodPost, "/api/reindex", nil); err != nil {
		t.Errorf("reindex: %v", err)
	}
}

func TestLoadSettings(t *testing.T) {
	root := t.TempDir()
	s, ttl, err := LoadSettings(root)
	if err != nil || s.Port != DefaultPort || ttl != index.DefaultClaimTTL {
		t.Errorf("defaults: %+v %s %v", s, ttl, err)
	}
	os.WriteFile(root+"/board.toml", []byte("port = 50000\nclaim_ttl = \"90m\"\n"), 0o644)
	s, ttl, err = LoadSettings(root)
	if err != nil || s.Port != 50000 || ttl != 90*time.Minute {
		t.Errorf("custom: %+v %s %v", s, ttl, err)
	}
	for _, bad := range []string{"claim_ttl = \"soon\"\n", "port = 70000\n", "port = -5\n"} {
		os.WriteFile(root+"/board.toml", []byte(bad), 0o644)
		if _, _, err := LoadSettings(root); err == nil {
			t.Errorf("board.toml %q accepted", bad)
		}
	}
}

func TestStartRemovesStaleServerJSON(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".run"), 0o700)
	WriteInfo(root, Info{PID: 1, Port: 1, Token: "dead"})
	s, err := Start(root, Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(root)
	if err != nil || info.Token == "dead" || info.PID != os.Getpid() {
		t.Errorf("server.json = %+v %v", info, err)
	}
	s.Close()
	if _, err := ReadInfo(root); err == nil {
		t.Error("server.json left after Close")
	}
}
