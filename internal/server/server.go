// Package server is the one process that writes a board: it holds the board lock, publishes how to reach
// it in .run/server.json, keeps the index fresh, and serves the local HTTP API.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/boardsync"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// MCPSessionTimeout is how long an MCP session may be idle before the server closes it.
const MCPSessionTimeout = 30 * time.Minute

// DefaultPort is the server's port when board.toml sets none. If it is taken, a free port is used.
const DefaultPort = 47321

// Settings are the board.toml keys the server reads.
type Settings struct {
	Port     int            `toml:"port"`
	ClaimTTL string         `toml:"claim_ttl"` // duration such as "4h"
	Intake   IntakeSettings `toml:"intake"`
}

// LoadSettings reads the server keys of board.toml (missing file or keys: defaults).
func LoadSettings(root string) (Settings, time.Duration, error) {
	s := Settings{Port: DefaultPort}
	data, err := os.ReadFile(filepath.Join(root, "board.toml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, 0, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &s); err != nil {
			return s, 0, fmt.Errorf("board.toml: %w", err)
		}
	}
	if s.Port == 0 {
		s.Port = DefaultPort
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, 0, fmt.Errorf("board.toml: port %d: use a number from 1 to 65535", s.Port)
	}
	ttl := index.DefaultClaimTTL
	if s.ClaimTTL != "" {
		d, err := time.ParseDuration(s.ClaimTTL)
		if err != nil || d <= 0 {
			return s, 0, fmt.Errorf("board.toml: claim_ttl %q: use a duration such as \"4h\"", s.ClaimTTL)
		}
		ttl = d
	}
	return s, ttl, nil
}

// Options configure Start.
type Options struct {
	// Port to listen on: 0 uses board.toml (default 47321); -1 picks any free port.
	Port int
	// Logf receives log lines; nil discards them.
	Logf func(format string, args ...any)
	// Version is reported to MCP clients.
	Version string
	// CustomCSS is the per-machine stylesheet the web UI loads after its own (spec §8.3); "" means
	// DefaultCustomCSS().
	CustomCSS string
}

// DefaultCustomCSS is <user config dir>/quest-map/custom.css (%AppData% on Windows, ~/.config on Linux): per
// machine, so it never travels with a board synced by git.
func DefaultCustomCSS() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "quest-map", "custom.css")
}

// Server is a running board server.
type Server struct {
	Board *board.Board
	Index *index.Index
	MCP   *mcpserver.Service

	mcpServer *mcp.Server

	root        string
	lock        *Lock
	info        Info
	ln          net.Listener
	http        *http.Server
	cancel      context.CancelFunc
	watcher     *index.Watcher
	logf        func(string, ...any)
	busy        sync.WaitGroup     // HTTP requests in progress
	archiveStop context.CancelFunc // ends the auto-archive loop
	archiveDone chan struct{}      // closed when it has ended

	hub      *hub
	person   board.Actor // who web UI changes are made by
	css      string      // custom.css path ("" if there is no config dir)
	version  string
	claimTTL time.Duration
	uiSecret string // value of the quest_map_session cookie the UI page sets

	archiveMu sync.Mutex // one auto-archive sweep at a time

	pluginMu    sync.Mutex // guards the plugin list cache
	pluginCache pluginList

	intake     IntakeSettings
	intakeSrv  *http.Server
	intakeAddr string

	stopReq  chan struct{} // closed when `quest-map stop` asks the server to shut down
	stopOnce sync.Once

	syncer   *boardsync.Syncer // commits, pulls and pushes a board kept in git (§3.5)
	syncCtx  context.Context   // syncs started from the UI run until the server stops, not the request
	syncDone chan struct{}     // closed when the auto-sync loop has ended
}

// StopRequested is closed when a client with the board token asked the server to stop (POST /api/shutdown).
// The process that runs the server then calls Close.
func (s *Server) StopRequested() <-chan struct{} { return s.stopReq }

// Start takes the board lock, opens the board, rebuilds the index, starts the file watcher and the HTTP
// listener, and writes .run/server.json. If another server holds the lock, the error is *ErrRunning.
func Start(root string, opts Options) (s *Server, err error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// Files in a Lore workspace are read-only until checked out, which breaks atomic writes (spec §3.1).
	if ws := vcs.LoreRoot(root); ws != "" {
		return nil, fmt.Errorf("the board %s is inside the Lore workspace %s; keep the board outside any workspace (default: ~/.quest-map)", root, ws)
	}
	lock, err := AcquireLock(root)
	if err != nil {
		return nil, err
	}
	s = &Server{root: root, lock: lock, logf: logf, hub: newHub(), person: personActor(), css: opts.CustomCSS, stopReq: make(chan struct{})}
	if s.css == "" {
		s.css = DefaultCustomCSS()
	}
	// A server.json left by a crashed server points at a dead port; remove it now, so CLIs do not use it while
	// this server starts.
	removeInfo(root)
	defer func() {
		if err != nil {
			s.Close()
			s = nil
		}
	}()

	settings, ttl, err := LoadSettings(root)
	if err != nil {
		return s, err
	}
	if s.Board, err = board.Open(root); err != nil {
		return s, err
	}
	if s.Index, err = index.Open(filepath.Join(root, ".cache", "index.sqlite"), s.Board, index.Options{ClaimTTL: ttl}); err != nil {
		return s, err
	}
	s.MCP = mcpserver.New(s.Board, s.Index, vcs.NewDetector())
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	s.mcpServer = s.MCP.MCPServer(version)
	s.version, s.claimTTL, s.intake = version, ttl, settings.Intake
	s.Index.SetNotify(func(c index.Change) { s.hub.publish(eventFromChange(c)) })
	if err = s.Index.Rebuild(); err != nil {
		return s, fmt.Errorf("build index: %w", err)
	}
	for _, r := range s.Index.Renumbered() {
		logf("renumbered duplicate %s in %s to %s", r.OldID, r.Project, r.NewID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	archiveCtx, archiveStop := context.WithCancel(ctx)
	s.archiveStop, s.archiveDone = archiveStop, make(chan struct{})
	go s.autoArchive(archiveCtx, s.archiveDone)
	if s.watcher, err = s.Index.Watch(ctx, nil, func(e error) { logf("watch: %v", e) }); err != nil {
		return s, err
	}
	s.syncer, s.syncCtx, s.syncDone = boardsync.New(root, logf), ctx, make(chan struct{})
	s.syncer.OnSync = func(boardsync.Status) { s.hub.publish(Event{Kind: "sync"}) }
	go func() { defer close(s.syncDone); s.syncer.Loop(ctx) }()

	port := opts.Port
	if port == 0 {
		port = settings.Port
	}
	if port < 0 {
		port = 0
	}
	if s.ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err != nil {
		logf("port %d is taken; using a free port", port)
		if s.ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return s, err
		}
	}
	token, err := newToken()
	if err != nil {
		return s, err
	}
	s.info = Info{PID: os.Getpid(), Port: s.ln.Addr().(*net.TCPAddr).Port, Token: token}
	if s.uiSecret, err = newToken(); err != nil {
		return s, err
	}
	s.http = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	// The intake listener starts first: a bad [intake] setting then fails Start before anything serves.
	if err = s.startIntake(); err != nil {
		return s, err
	}
	// The goroutine gets its own copies: on a failed Start the deferred cleanup sets s to nil.
	srv, ln := s.http, s.ln
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("http: %v", err)
		}
	}()
	if err = WriteInfo(root, s.info); err != nil {
		return s, err
	}
	logf("serving board %s on 127.0.0.1:%d", root, s.info.Port)
	return s, nil
}

// Info returns how to reach this server.
func (s *Server) Info() Info { return s.info }

// Close stops the server and releases the board lock. It waits for HTTP requests and watcher refreshes that
// are still running, so nothing writes the board or the index after the lock is released.
func (s *Server) Close() error {
	var errs []error
	if s.hub != nil {
		s.hub.close() // ends the UI's event streams, so shutdown does not wait for them
	}
	if s.intakeSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.intakeSrv.Shutdown(ctx); errors.Is(err, context.DeadlineExceeded) {
			s.intakeSrv.Close()
		}
		cancel()
		// Intake requests are counted in s.busy, which is waited for below before the lock is released.
	}
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.http.Shutdown(ctx); errors.Is(err, context.DeadlineExceeded) {
			s.http.Close() // long-lived streams (an MCP client's event stream) end here
		} else if err != nil {
			errs = append(errs, err)
		}
		cancel()
		removeInfo(s.root)
	}
	if s.archiveStop != nil {
		s.archiveStop()
		<-s.archiveDone // a sweep may be moving files
	}
	s.busy.Wait() // a request (a reindex, a playtest note) may still be writing; let it finish first
	if s.cancel != nil {
		s.cancel()
		if s.watcher != nil {
			<-s.watcher.Done()
		}
		if s.syncDone != nil {
			<-s.syncDone // a sync may be merging
		}
	}
	if s.Index != nil {
		errs = append(errs, s.Index.Close())
	}
	if s.lock != nil {
		errs = append(errs, s.lock.Release())
	}
	return errors.Join(errs...)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "pid": s.info.PID})
	})
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rows, err := s.Index.FindString(q.Get("q"), index.FindOptions{Project: q.Get("project"), IncludeArchived: q.Get("archived") == "yes",
			Fields: s.fieldKeys(q.Get("project"))})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if rows == nil {
			rows = []index.Row{}
		}
		writeJSON(w, rows)
	})
	// Idle MCP sessions end after MCPSessionTimeout; the stdio proxy opens a new one when that happens.
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcpServer },
		&mcp.StreamableHTTPOptions{SessionTimeout: MCPSessionTimeout}))
	mux.HandleFunc("POST /api/usage-hook", func(w http.ResponseWriter, r *http.Request) {
		var req mcpserver.HookRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := s.MCP.IngestTranscript(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, res)
	})
	s.routes(mux)
	s.m5Routes(mux)
	s.insightsRoutes(mux)
	s.i18nRoutes(mux)
	s.kbRoutes(mux)
	s.projectRoutes(mux)
	s.uiRoutes(mux)
	s.pluginRoutes(mux)
	s.syncRoutes(mux)
	// Only the board token may stop the server (the CLI): the UI's cookie is not enough.
	mux.HandleFunc("POST /api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.info.Token)) != 1 {
			http.Error(w, "stopping the server needs the board token", http.StatusForbidden)
			return
		}
		s.logf("stop requested")
		s.stopOnce.Do(func() { close(s.stopReq) })
		writeJSON(w, map[string]any{"ok": true, "pid": s.info.PID})
	})
	mux.HandleFunc("POST /api/reindex", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Index.Rebuild(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "renumbered": s.Index.Renumbered()})
	})
	return s.guard(s.track(mux))
}

// track counts requests in progress, so Close can wait for them.
func (s *Server) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.busy.Add(1)
		defer s.busy.Done()
		next.ServeHTTP(w, r)
	})
}

// guard enforces §8.1: Host must be this server (DNS rebinding), non-GET requests need no Origin or our own
// (CSRF), and /api/ needs the bearer token.
func (s *Server) guard(next http.Handler) http.Handler {
	port := strconv.Itoa(s.info.Port)
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	origins := map[string]bool{"http://127.0.0.1:" + port: true, "http://localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !origins[o] {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if !isUIAsset(r) && !s.authorized(r) {
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Running returns the info of the server running for this board, checked by calling it, or nil.
func Running(root string) *Info {
	info, err := ReadInfo(root)
	if err != nil {
		return nil
	}
	if err := Call(info, http.MethodGet, "/api/health", nil); err != nil {
		return nil
	}
	return info
}

// Call sends a request to a running server and decodes a JSON reply into out (if not nil). path may carry
// a query string.
func Call(info *Info, method, path string, out any) error {
	return CallJSON(info, method, path, nil, out)
}

// CallJSON is Call with a JSON request body (in, if not nil). GET requests time out after 2 s, others after
// 60 s.
func CallJSON(info *Info, method, path string, in, out any) error {
	timeout := 60 * time.Second
	if method == http.MethodGet {
		timeout = 2 * time.Second
	}
	return CallJSONTimeout(info, method, path, in, out, timeout)
}

// CallJSONTimeout is CallJSON with a given timeout for the whole request.
func CallJSONTimeout(info *Info, method, path string, in, out any, timeout time.Duration) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", info.Port, path), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+info.Token)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sessionCookie is the cookie the UI page sets; it lets the page's own requests through (§8.1).
const sessionCookie = "quest_map_session"

// authorized accepts the bearer token (agents, CLI) or the UI's session cookie.
func (s *Server) authorized(r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.info.Token)) == 1 {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.uiSecret != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.uiSecret)) == 1
}
