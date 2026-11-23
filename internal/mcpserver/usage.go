package mcpserver

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
)

// HookRequest is what the client's usage hook sends after a turn: which session, where its transcript is.
type HookRequest struct {
	Client         string `json:"client"`          // "claude-code"
	ClientSession  string `json:"session_id"`      // the client's session id
	TranscriptPath string `json:"transcript_path"` // the session's JSONL transcript
	Cwd            string `json:"cwd"`
}

// HookResult says what a hook call recorded.
type HookResult struct {
	Session string `json:"session,omitempty"` // the board session the transcript is linked to
	Task    string `json:"task,omitempty"`
	Project string `json:"project,omitempty"`
	Tokens  int64  `json:"tokens"`
	Seconds int64  `json:"seconds"`
	Note    string `json:"note,omitempty"`
}

// SessionMarker starts the last line of every tool reply: "quest-map session: claude-code:7f3a2c". The
// client stores tool results in its transcript, so the usage hook can find which board session a transcript
// belongs to, also after /clear starts a new client session on the same MCP connection.
const SessionMarker = "quest-map session: "

var markerRe = regexp.MustCompile(`quest-map session: ([a-z0-9._-]{1,64}:[A-Za-z0-9]{1,32})`)

// fileCursor remembers how far one transcript file has been counted.
type fileCursor struct {
	Offset  int64    `json:"offset"`
	HeadLen int      `json:"head_len,omitempty"` // the first HeadLen bytes hash to HeadSum; if not, the file was replaced
	HeadSum string   `json:"head_sum,omitempty"`
	Recent  []string `json:"recent,omitempty"` // message ids counted lately; a message's lines can span two calls
}

// clientCursor is the state of one client session: its link and a cursor per transcript file (the main one
// and its subagents').
type clientCursor struct {
	Link  string                 `json:"link,omitempty"` // board session (Caller.ID) the transcript belongs to
	Seen  time.Time              `json:"seen"`
	Files map[string]*fileCursor `json:"files"` // by pathKey
}

type cursorStore struct {
	Version  int                      `json:"version"`
	Sessions map[string]*clientCursor `json:"sessions"` // by client session id
}

const (
	recentIDs     = 64
	headBytes     = 256
	cursorMaxAge  = 30 * 24 * time.Hour // client sessions not seen for this long are forgotten
	cursorVersion = 2
)

func (s *Service) cursorPath() string { return filepath.Join(s.b.Root(), ".run", "usage-cursors.json") }

// loadCursorsLocked reads the cursor file once. s.cursorMu is held. Version 1 files (a cursor per client
// session) are migrated, so an upgrade does not count old transcripts again.
func (s *Service) loadCursorsLocked() *cursorStore {
	if s.store != nil {
		return s.store
	}
	st := &cursorStore{Version: cursorVersion, Sessions: map[string]*clientCursor{}}
	s.store = st
	data, err := os.ReadFile(s.cursorPath())
	if err != nil {
		return st
	}
	var probe struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &probe) == nil && probe.Version == cursorVersion {
		if json.Unmarshal(data, st) == nil && st.Sessions != nil {
			return st
		}
		st.Sessions = map[string]*clientCursor{}
		return st
	}
	var old map[string]struct {
		Path   string   `json:"path"`
		Offset int64    `json:"offset"`
		Recent []string `json:"recent"`
	}
	if json.Unmarshal(data, &old) == nil {
		for cs, c := range old {
			if c.Path != "" {
				st.Sessions[cs] = &clientCursor{Seen: s.now(), Files: map[string]*fileCursor{pathKey(c.Path): {Offset: c.Offset, Recent: c.Recent}}}
			}
		}
	}
	return st
}

// saveCursorsLocked writes the cursor file atomically. s.cursorMu is held.
func (s *Service) saveCursorsLocked() error {
	st := s.loadCursorsLocked()
	for cs, c := range st.Sessions {
		if s.now().Sub(c.Seen) > cursorMaxAge {
			delete(st.Sessions, cs)
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.cursorPath()), 0o700); err != nil {
		return err
	}
	tmp := s.cursorPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.cursorPath())
}

// clientState returns a copy of a client session's cursor state (nil if it has none).
func (s *Service) clientState(clientSession string) *clientCursor {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	c := s.loadCursorsLocked().Sessions[clientSession]
	if c == nil {
		return nil
	}
	cp := &clientCursor{Link: c.Link, Seen: c.Seen, Files: map[string]*fileCursor{}}
	for k, f := range c.Files {
		fc := *f
		fc.Recent = append([]string(nil), f.Recent...)
		cp.Files[k] = &fc
	}
	return cp
}

// storeClientState saves a client session's cursor state.
func (s *Service) storeClientState(clientSession string, c *clientCursor) error {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	s.loadCursorsLocked().Sessions[clientSession] = c
	return s.saveCursorsLocked()
}

// ingestLock returns the lock for one client session's transcripts. Different sessions are read in parallel;
// nothing service-wide is held while a transcript is read.
func (s *Service) ingestLock(clientSession string) *sync.Mutex {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	if s.ingestMu == nil {
		s.ingestMu = map[string]*sync.Mutex{}
	}
	m := s.ingestMu[clientSession]
	if m == nil {
		m = &sync.Mutex{}
		s.ingestMu[clientSession] = m
	}
	return m
}

// pathKey makes the cursor key of a file: cleaned, and case-folded on Windows, so a case or slash
// difference in the path the client sends does not read the file from the start again.
func pathKey(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// transcriptLine is the part of a Claude Code transcript line that usage needs.
type transcriptLine struct {
	Type        string    `json:"type"`
	IsSidechain bool      `json:"isSidechain"`
	Timestamp   time.Time `json:"timestamp"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// userKind tells a person's prompt from a tool result. Sidechain (subagent) lines are never prompts.
func (l transcriptLine) userKind() (prompt, toolResult bool) {
	if l.Type != "user" {
		return false, false
	}
	var str string
	if json.Unmarshal(l.Message.Content, &str) == nil {
		return !l.IsSidechain, false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return false, false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false, true
		}
	}
	return !l.IsSidechain, false
}

// entry is one transcript line that matters for usage.
type entry struct {
	kind   byte // 'p' prompt, 'a' assistant reply, 'm' tool result carrying a session marker
	at     time.Time
	model  string
	tok    board.Tokens // zero for a message already counted, or one without usage
	marker string
}

// scanTranscript reads the complete lines after the cursor and returns the entries and the moved cursor.
// A file that is shorter than the cursor, or whose first bytes changed, was replaced: it is read from the
// start. Each message id counts once; messages with no tokens (such as "<synthetic>" ones) count nothing.
func scanTranscript(path string, cur fileCursor) ([]entry, fileCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, cur, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, cur, err
	}
	head := make([]byte, min(int64(headBytes), st.Size()))
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, cur, err
	}
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:8]) }
	if cur.Offset > st.Size() || (cur.HeadLen > 0 && (cur.HeadLen > len(head) || sum(head[:cur.HeadLen]) != cur.HeadSum)) {
		cur = fileCursor{}
	}
	cur.HeadLen, cur.HeadSum = len(head), sum(head)
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return nil, cur, err
	}
	recent := map[string]bool{}
	for _, id := range cur.Recent {
		recent[id] = true
	}
	var out []entry
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			break // an incomplete last line: count it next time
		}
		cur.Offset += int64(len(raw))
		if len(raw) > 1 {
			var l transcriptLine
			if json.Unmarshal(raw, &l) == nil {
				prompt, toolResult := l.userKind()
				switch {
				case prompt:
					out = append(out, entry{kind: 'p', at: l.Timestamp})
				case toolResult:
					if bytes.Contains(raw, []byte(SessionMarker)) {
						if m := markerRe.FindAllSubmatch(raw, -1); len(m) > 0 {
							out = append(out, entry{kind: 'm', at: l.Timestamp, marker: string(m[len(m)-1][1])})
						}
					}
				case l.Type == "assistant" && l.Message.Model == "<synthetic>":
					// written by the client itself (errors, interruptions): no model work, no tokens
				case l.Type == "assistant":
					e := entry{kind: 'a', at: l.Timestamp, model: l.Message.Model}
					if u := l.Message.Usage; u != nil && l.Message.ID != "" && !recent[l.Message.ID] {
						tok := board.Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheCreation: u.CacheCreation}
						if !tok.IsZero() {
							recent[l.Message.ID] = true
							cur.Recent = append(cur.Recent, l.Message.ID)
							e.tok = tok
						}
					}
					out = append(out, e)
				}
			}
		}
		if err != nil {
			break
		}
	}
	if n := len(cur.Recent); n > recentIDs {
		cur.Recent = cur.Recent[n-recentIDs:]
	}
	return out, cur, nil
}

// transcriptUsage is what a stretch of transcript cost.
type transcriptUsage struct {
	byModel map[string]board.Tokens
	active  time.Duration
}

// add sums token usage per model and active time (from each prompt to the last reply after it).
func (tu *transcriptUsage) add(es []entry) {
	if tu.byModel == nil {
		tu.byModel = map[string]board.Tokens{}
	}
	var turnStart, lastReply time.Time
	closeTurn := func() {
		if !turnStart.IsZero() && lastReply.After(turnStart) {
			tu.active += lastReply.Sub(turnStart)
		}
		turnStart, lastReply = time.Time{}, time.Time{}
	}
	for _, e := range es {
		switch e.kind {
		case 'p':
			closeTurn()
			turnStart = e.at
		case 'a':
			if !e.at.IsZero() {
				lastReply = e.at
			}
			if !e.tok.IsZero() {
				m := tu.byModel[e.model]
				m.Input += e.tok.Input
				m.Output += e.tok.Output
				m.CacheRead += e.tok.CacheRead
				m.CacheCreation += e.tok.CacheCreation
				tu.byModel[e.model] = m
			}
		}
	}
	closeTurn() // the Stop hook runs at the end of a turn
}

// clientName makes a short, safe client name: "claude-code".
func clientName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			return r
		case r == ' ' || r == '/':
			return '-'
		}
		return -1
	}, s)
	if s == "" {
		return "agent"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// transcriptFile is one file of a client session: the main transcript or a subagent's.
type transcriptFile struct {
	path    string
	main    bool
	entries []entry
	cur     fileCursor
}

// subagentFiles lists the subagent transcripts Claude Code keeps next to a session transcript:
// <dir>/<session>/subagents/agent-*.jsonl.
func subagentFiles(mainPath string) []string {
	base := strings.TrimSuffix(filepath.Base(mainPath), filepath.Ext(mainPath))
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(mainPath), base, "subagents", "agent-*.jsonl"))
	sort.Strings(files)
	return files
}

// checkTranscriptPath accepts only an absolute path to a regular .jsonl file.
func checkTranscriptPath(p string) error {
	if !filepath.IsAbs(p) || !strings.EqualFold(filepath.Ext(p), ".jsonl") {
		return errorf("transcript_path must be an absolute path to a .jsonl file")
	}
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errorf("transcript_path %s is not a regular file", p)
	}
	return nil
}

// IngestTranscript records the usage a client session's transcripts (main and subagents) gained since the
// last call, as measured usage of the board session they are linked to. The link comes from the session
// markers in tool results (newest wins), else from the client session id the proxy forwarded, else from an
// earlier call. A transcript with no link never talked to quest-map: nothing is recorded for it. The usage
// goes to the one task the session held a claim on during the turn, else to its project as overhead.
func (s *Service) IngestTranscript(req HookRequest) (HookResult, error) {
	if req.ClientSession == "" || req.TranscriptPath == "" {
		return HookResult{}, errorf("session_id and transcript_path are required")
	}
	if err := checkTranscriptPath(req.TranscriptPath); err != nil {
		return HookResult{}, err
	}
	lock := s.ingestLock(req.ClientSession)
	lock.Lock()
	defer lock.Unlock()

	state := s.clientState(req.ClientSession)
	isNew := state == nil
	if isNew {
		state = &clientCursor{Files: map[string]*fileCursor{}}
	}
	state.Seen = s.now()

	// Read every file from its cursor, outside any service-wide lock.
	paths := append([]string{req.TranscriptPath}, subagentFiles(req.TranscriptPath)...)
	var files []*transcriptFile
	for i, p := range paths {
		cur := fileCursor{}
		if c := state.Files[pathKey(p)]; c != nil {
			cur = *c
		}
		es, next, err := scanTranscript(p, cur)
		if err != nil {
			if i == 0 {
				return HookResult{}, err
			}
			continue // a subagent file that went away
		}
		files = append(files, &transcriptFile{path: p, main: i == 0, entries: es, cur: next})
	}

	// The link: the newest marker, else the forwarded client session id, else the stored link.
	link, linkAt := "", time.Time{}
	var firstMarker time.Time
	for _, f := range files {
		for _, e := range f.entries {
			if e.kind != 'm' {
				continue
			}
			if link == "" || !e.at.Before(linkAt) {
				link, linkAt = e.marker, e.at
			}
			if firstMarker.IsZero() || e.at.Before(firstMarker) {
				firstMarker = e.at
			}
		}
	}
	s.mu.Lock()
	if link == "" {
		link = s.byClient[req.ClientSession]
		if ss := s.sessions[link]; link != "" && ss != nil {
			firstMarker = ss.started
		}
	}
	if link == "" {
		link = state.Link
	}
	if link != "" {
		s.byClient[req.ClientSession] = link
	}
	s.mu.Unlock()
	state.Link = link

	// A client session seen for the first time may have history from before it used quest-map (a resumed
	// session): count from the turn in which it first talked to the board.
	var tu transcriptUsage
	if link != "" {
		var startAt time.Time
		for _, f := range files {
			if !f.main {
				continue
			}
			es := f.entries
			if isNew {
				start := len(es)
				if !firstMarker.IsZero() {
					start = 0
					for i, e := range es {
						if !e.at.IsZero() && e.at.After(firstMarker) {
							break
						}
						if e.kind == 'p' {
							start, startAt = i, e.at
						}
					}
					if startAt.IsZero() {
						startAt = firstMarker
					}
				}
				es = es[start:]
			}
			tu.add(es)
		}
		for _, f := range files {
			if f.main {
				continue
			}
			es := f.entries
			if isNew {
				es = nil
				for _, e := range f.entries {
					if !startAt.IsZero() && !e.at.Before(startAt) {
						es = append(es, e)
					}
				}
			}
			tu.add(es)
		}
	}
	for _, f := range files {
		cur := f.cur
		state.Files[pathKey(f.path)] = &cur
	}

	res := HookResult{Session: link}
	var total board.Tokens
	models := make([]string, 0, len(tu.byModel))
	for m, t := range tu.byModel {
		models = append(models, m)
		total.Input += t.Input
		total.Output += t.Output
		total.CacheRead += t.CacheRead
		total.CacheCreation += t.CacheCreation
	}
	res.Tokens, res.Seconds = total.Sum(), int64(tu.active/time.Second)
	if link == "" {
		res.Tokens, res.Seconds = 0, 0
		res.Note = "this session has not used quest-map; nothing recorded"
		return res, s.storeClientState(req.ClientSession, state)
	}
	if total.IsZero() && tu.active == 0 {
		return res, s.storeClientState(req.ClientSession, state)
	}

	ss := s.session(Caller{ID: link, Client: clientName(firstNonEmpty(req.Client, "claude-code")), ClientSession: req.ClientSession, Cwd: req.Cwd})
	s.mu.Lock()
	client, cwd, since := ss.caller.Client, firstNonEmpty(req.Cwd, ss.caller.Cwd), ss.lastIngest
	s.mu.Unlock()
	p, task := s.attribute(link, cwd, since)
	if p == nil {
		res.Note = "the session holds no claim and its folder has no project; usage not recorded"
		return res, s.storeClientState(req.ClientSession, state)
	}
	res.Project, res.Task = p.Slug(), task
	// The model with most output tokens carries the turn's active time.
	sort.Slice(models, func(i, j int) bool { return tu.byModel[models[i]].Output > tu.byModel[models[j]].Output })
	var recErr error
	recorded := false
	for i, m := range models {
		u := &board.Usage{Tokens: tu.byModel[m], Source: board.SourceMeasured, Model: m}
		if i == 0 {
			u.Time = tu.active
		}
		if err := p.RecordUsage(task, board.Actor{Kind: "agent", Session: link, Client: client, Model: m}, u); err != nil {
			recErr = errors.Join(recErr, err)
			continue
		}
		recorded = true
	}
	if len(models) == 0 && tu.active > 0 {
		if err := p.RecordUsage(task, board.Actor{Kind: "agent", Session: link, Client: client}, &board.Usage{Time: tu.active, Source: board.SourceMeasured}); err != nil {
			recErr = errors.Join(recErr, err)
		} else {
			recorded = true
		}
	}
	if recorded {
		s.mu.Lock()
		ss.measured = true
		ss.lastIngest = s.now()
		for id, at := range ss.ended {
			if at.Before(since) {
				delete(ss.ended, id)
			}
		}
		s.mu.Unlock()
		if task != "" {
			if t, err := p.Get(task); err == nil {
				s.refresh(p, t)
			}
		}
	}
	if recErr != nil && !recorded {
		// Nothing was written: keep the cursors, so the next call tries this stretch again.
		return res, fmt.Errorf("record usage: %w", recErr)
	}
	if err := s.storeClientState(req.ClientSession, state); err != nil {
		return res, errors.Join(recErr, err)
	}
	if recErr != nil {
		return res, fmt.Errorf("record usage (partly recorded): %w", recErr)
	}
	return res, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// attribute finds where a linked session's usage goes: the one task it held a claim on during the turn (a
// claim it still holds, or one it ended since the last ingest); several such tasks (subagents sharing the
// session) cannot be split, so the usage is overhead of their project; no claim: overhead of the working
// folder's project. Tasks claimed by other sessions are never used.
func (s *Service) attribute(session, cwd string, since time.Time) (*board.Project, string) {
	type cand struct{ project, id string }
	var cands []cand
	seen := map[string]bool{}
	if rows, err := s.ix.FindString("status:doing", index.FindOptions{}); err == nil {
		for _, r := range rows {
			if r.ClaimBy == session && !seen[r.ID] {
				seen[r.ID] = true
				cands = append(cands, cand{r.Project, r.ID})
			}
		}
	}
	s.mu.Lock()
	var ended []string
	if ss := s.sessions[session]; ss != nil {
		for id, at := range ss.ended {
			if !at.Before(since) && !seen[id] {
				ended = append(ended, id)
			}
		}
	}
	s.mu.Unlock()
	sort.Strings(ended)
	for _, id := range ended {
		if p, tid, err := s.projectOfID(id); err == nil {
			seen[tid] = true
			cands = append(cands, cand{p.Slug(), tid})
		}
	}
	if len(cands) > 0 {
		p, err := s.b.Project(cands[0].project)
		if err != nil {
			return nil, ""
		}
		if len(cands) == 1 {
			return p, cands[0].id
		}
		return p, ""
	}
	folder, err := s.project(Caller{ID: session, Cwd: cwd}, "")
	if err != nil {
		return nil, ""
	}
	return folder, ""
}
