// Package mcpproxy is `quest-map mcp`: the stdio MCP server agents launch. It is a thin bridge to the one
// running board server's /mcp endpoint (starting the server if none runs), adding the agent's working
// folder and session ids to every request so the server can pick the project and record claims. When the
// server restarts (or drops an idle session), the proxy connects again with the same agent session id, so
// the agent keeps its claims and never notices more than one failed call.
package mcpproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RoyalNoob/quest-map/internal/mcpserver"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// Options configure Run.
type Options struct {
	Root   string        // board folder
	Exe    string        // quest-map binary used to start the server; "" never starts one
	Stdin  io.ReadCloser // MCP messages from the client
	Stdout io.WriteCloser
	Cwd    string
	Getenv func(string) string
	// StartTimeout bounds waiting for a server this proxy started. Default 20s.
	StartTimeout time.Duration
	Logf         func(format string, args ...any)
}

// EnsureServer returns the running server's info, starting `exe serve` in the background if none runs.
func EnsureServer(root, exe string, timeout time.Duration) (*server.Info, error) {
	if info := server.Running(root); info != nil {
		return info, nil
	}
	if exe == "" {
		return nil, errors.New("no quest-map server is running for " + root)
	}
	if base := strings.ToLower(filepath.Base(exe)); strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe") {
		// A Go test binary would run the tests again instead of serving, and those could start it again.
		return nil, errors.New("no quest-map server is running for " + root + " (a test binary does not start one)")
	}
	if err := os.MkdirAll(filepath.Join(root, ".run"), 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(root, ".run", "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd, err := startDetached(func() *exec.Cmd {
		cmd := exec.Command(exe, "serve", "--board", root)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		return cmd
	})
	logFile.Close()
	if err != nil {
		return nil, fmt.Errorf("start quest-map serve: %w", err)
	}
	go cmd.Wait() // reap it if it exits (for example because another proxy's server won the lock)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if info := server.Running(root); info != nil {
			return info, nil
		}
	}
	return nil, fmt.Errorf("quest-map serve did not come up within %s; see %s", timeout, filepath.Join(root, ".run", "serve.log"))
}

// headerTransport adds fixed headers to every request.
type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header[k] = v
	}
	return t.base.RoundTrip(r)
}

// Headers builds the headers the proxy adds: token, the agent session id (fixed for the proxy's life), the
// working folder, and the client's own session id (Claude Code passes CLAUDE_CODE_SESSION_ID to the servers
// it starts).
func Headers(token, session, cwd string, getenv func(string) string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set(mcpserver.HeaderSession, session)
	if cwd != "" {
		h.Set(mcpserver.HeaderCwd, cwd)
	}
	if cs := getenv("CLAUDE_CODE_SESSION_ID"); cs != "" {
		h.Set(mcpserver.HeaderClientSession, cs)
	}
	return h
}

// bridge forwards messages between the client and the current server connection, replacing the connection
// when the server goes away.
type bridge struct {
	opts    Options
	ctx     context.Context
	session string
	client  mcp.Connection

	mu          sync.Mutex
	conn        mcp.Connection
	gen         int
	changed     chan struct{} // closed when conn is replaced
	init        *jsonrpc.Request
	initialized *jsonrpc.Request
	reinit      map[jsonrpc.ID]bool // ids of replayed initialize requests; their responses are dropped
	inflight    map[jsonrpc.ID]bool // client calls sent to the server and not answered yet
	reinits     int

	clientMu sync.Mutex // serializes writes to the client
}

// connect finds (or starts) the server and opens a new MCP connection to it.
func (b *bridge) connect() (mcp.Connection, error) {
	info, err := EnsureServer(b.opts.Root, b.opts.Exe, b.opts.StartTimeout)
	if err != nil {
		return nil, err
	}
	remote := &mcp.StreamableClientTransport{
		Endpoint:             fmt.Sprintf("http://127.0.0.1:%d/mcp", info.Port),
		HTTPClient:           &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: Headers(info.Token, b.session, b.opts.Cwd, b.opts.Getenv)}},
		DisableStandaloneSSE: true, // the server sends nothing unprompted; no long-lived request
		MaxRetries:           -1,   // a lost server is handled here, by connecting again
	}
	rc, err := remote.Connect(b.ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to the server: %w", err)
	}
	return rc, nil
}

func (b *bridge) current() (mcp.Connection, int, chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn, b.gen, b.changed
}

// reconnect replaces connection generation gen (if it is still the current one) with a new connection, on
// which the client's initialize handshake is replayed. Calls that were in flight get an error reply.
func (b *bridge) reconnect(gen int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gen != gen {
		return nil // someone else already reconnected
	}
	b.opts.Logf("server connection lost; connecting again")
	go b.conn.Close() // best effort; may wait for a dead server
	conn, err := b.connect()
	if err != nil {
		b.failInflightLocked("the quest-map server is not reachable (" + err.Error() + "); try again")
		return err
	}
	if b.init != nil {
		b.reinits++
		id, _ := jsonrpc.MakeID(fmt.Sprintf("quest-map-reinit-%d", b.reinits))
		b.reinit[id] = true
		if err := conn.Write(b.ctx, &jsonrpc.Request{ID: id, Method: b.init.Method, Params: b.init.Params}); err != nil {
			conn.Close()
			b.failInflightLocked("the quest-map server is not reachable; try again")
			return err
		}
		if b.initialized != nil {
			conn.Write(b.ctx, b.initialized)
		}
	}
	b.conn = conn
	b.gen++
	close(b.changed)
	b.changed = make(chan struct{})
	b.failInflightLocked("the quest-map server restarted while handling this call; call it again")
	return nil
}

func (b *bridge) failInflightLocked(msg string) {
	for id := range b.inflight {
		b.toClient(&jsonrpc.Response{ID: id, Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: msg}})
	}
	clear(b.inflight)
}

func (b *bridge) toClient(msg jsonrpc.Message) error {
	b.clientMu.Lock()
	defer b.clientMu.Unlock()
	return b.client.Write(b.ctx, msg)
}

// fromClient forwards client messages to the server until the client closes.
func (b *bridge) fromClient() error {
	for {
		msg, err := b.client.Read(b.ctx)
		if err != nil {
			return err
		}
		req, _ := msg.(*jsonrpc.Request)
		if req != nil {
			b.mu.Lock()
			switch req.Method {
			case "initialize":
				cp := *req
				b.init = &cp
			case "notifications/initialized":
				cp := *req
				b.initialized = &cp
			}
			b.mu.Unlock()
		}
		call := req != nil && req.IsCall()
		for attempt := 0; ; attempt++ {
			conn, gen, _ := b.current()
			if call {
				b.mu.Lock()
				b.inflight[req.ID] = true
				b.mu.Unlock()
			}
			err := conn.Write(b.ctx, msg)
			if err == nil {
				break
			}
			if b.ctx.Err() != nil {
				return b.ctx.Err()
			}
			if call {
				b.mu.Lock()
				delete(b.inflight, req.ID)
				b.mu.Unlock()
			}
			rerr := b.reconnect(gen)
			if rerr == nil && attempt < 2 {
				continue
			}
			if call {
				if rerr == nil {
					rerr = err
				}
				if err := b.toClient(&jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError,
					Message: "the quest-map server is not reachable (" + rerr.Error() + "); try again"}}); err != nil {
					return err
				}
			}
			break
		}
	}
}

// fromServer forwards server messages to the client, following the connection across reconnects.
func (b *bridge) fromServer() error {
	for {
		conn, gen, changed := b.current()
		msg, err := conn.Read(b.ctx)
		if err != nil {
			if b.ctx.Err() != nil {
				return b.ctx.Err()
			}
			if _, g, _ := b.current(); g != gen {
				continue
			}
			if b.reconnect(gen) != nil {
				// No server now; the next client call tries again. Wait for a new connection.
				select {
				case <-changed:
				case <-b.ctx.Done():
					return b.ctx.Err()
				}
			}
			continue
		}
		if resp, ok := msg.(*jsonrpc.Response); ok {
			b.mu.Lock()
			drop := b.reinit[resp.ID]
			delete(b.reinit, resp.ID)
			delete(b.inflight, resp.ID)
			b.mu.Unlock()
			if drop {
				continue
			}
		}
		if err := b.toClient(msg); err != nil {
			return fmt.Errorf("to client: %w", err)
		}
	}
}

// Run bridges MCP messages between stdio and the server until the client closes.
func Run(ctx context.Context, opts Options) error {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 20 * time.Second
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &bridge{opts: opts, ctx: ctx, session: mcpserver.NewSessionID(), changed: make(chan struct{}),
		reinit: map[jsonrpc.ID]bool{}, inflight: map[jsonrpc.ID]bool{}}
	rc, err := b.connect()
	if err != nil {
		return err
	}
	b.conn = rc
	lc, err := (&mcp.IOTransport{Reader: opts.Stdin, Writer: opts.Stdout}).Connect(ctx)
	if err != nil {
		rc.Close()
		return err
	}
	b.client = lc
	errc := make(chan error, 2)
	go func() { errc <- b.fromClient() }()
	go func() { errc <- b.fromServer() }()
	err = <-errc
	cancel()
	lc.Close()
	conn, _, _ := b.current()
	conn.Close()
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
