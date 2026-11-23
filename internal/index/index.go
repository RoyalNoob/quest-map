// Package index keeps the derived SQLite cache of a board: task rows, full-text search, repos, releases and
// the usage / status-change records from activity.jsonl. Files stay the source of truth; the index is
// rebuilt from them on start and kept fresh by a file watcher. Only the server writes it.
package index

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite with FTS5

	"github.com/RoyalNoob/quest-map/internal/board"
)

// SchemaVersion changes whenever the tables change; an index file with another version is rebuilt.
const SchemaVersion = 7

// DefaultClaimTTL is how long a doing task may go without a write before its claim is stale.
const DefaultClaimTTL = 4 * time.Hour

// systemActor signs changes the index makes itself (renumbering duplicates).
var systemActor = board.Actor{Name: "quest-map"}

const schema = `
CREATE TABLE tasks (
  rowid INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  project TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  archived INTEGER NOT NULL,
  title TEXT, status TEXT, doer TEXT, priority TEXT, prank INTEGER,
  discipline TEXT, milestone TEXT, asset TEXT, asset_name TEXT,
  pipeline_name TEXT, pipeline_at TEXT, found_in TEXT, fixed_in TEXT, moved_from TEXT, url TEXT,
  claim_by TEXT, claim_at TEXT, created TEXT, updated TEXT, body TEXT, log TEXT,
  spent_tokens INTEGER NOT NULL DEFAULT 0, spent_seconds INTEGER NOT NULL DEFAULT 0,
  spent_records INTEGER NOT NULL DEFAULT 0, spent_self INTEGER NOT NULL DEFAULT 0, spent_any INTEGER NOT NULL DEFAULT 0,
  parse_error TEXT, size INTEGER, hash TEXT
);
CREATE INDEX tasks_project ON tasks(project, status);
CREATE TABLE labels (task TEXT NOT NULL, label TEXT NOT NULL);
CREATE INDEX labels_task ON labels(task);
CREATE TABLE assignees (task TEXT NOT NULL, name TEXT NOT NULL);
CREATE INDEX assignees_task ON assignees(task);
CREATE TABLE custom (task TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL);
CREATE INDEX custom_task ON custom(task);
CREATE TABLE blocks (task TEXT NOT NULL, blocker TEXT NOT NULL);
CREATE INDEX blocks_task ON blocks(task);
CREATE VIRTUAL TABLE tasks_fts USING fts5(title, body, log, tokenize='trigram');
CREATE TABLE repos (remote TEXT PRIMARY KEY, project TEXT NOT NULL);
CREATE TABLE releases (project TEXT NOT NULL, id TEXT NOT NULL, target TEXT, revision TEXT, date TEXT, notes TEXT,
  PRIMARY KEY (project, id));
CREATE TABLE usage (project TEXT NOT NULL, task TEXT, owner TEXT, at TEXT, session TEXT, client TEXT, model TEXT, source TEXT,
  input INTEGER, output INTEGER, cache_read INTEGER, cache_creation INTEGER, total INTEGER, time_s INTEGER, cost REAL);
CREATE INDEX usage_owner ON usage(owner);
CREATE TABLE transitions (project TEXT NOT NULL, task TEXT, owner TEXT, at TEXT, from_status TEXT, to_status TEXT,
  actor TEXT, session TEXT, client TEXT, model TEXT);
CREATE INDEX transitions_owner ON transitions(owner);
`

// Options configure an index.
type Options struct {
	ClaimTTL time.Duration    // default DefaultClaimTTL
	Now      func() time.Time // default time.Now
}

// ParseProblem is a task file that does not parse. If it parsed before, its last good row stays in the
// index with the error attached.
type ParseProblem struct {
	Path string
	ID   string // "" if the file never parsed
	Err  string
	hash string // content hash, so the same broken content is not reported again
}

// Renumbered records a duplicate id that was resolved by giving one file a new id.
type Renumbered struct {
	Project string
	OldID   string
	NewID   string
	KeptID  string
	At      time.Time
}

// Index is the SQLite cache of one board.
type Index struct {
	b    *board.Board
	db   *sql.DB
	opts Options

	mu         sync.Mutex
	spent      map[string]board.Spent
	broken     map[string]ParseProblem // by path, for files with no good row
	renumbered []Renumbered
	// last holds the last good parse of each indexed file, by path, to tell what a hand edit changed.
	last map[string]*board.Task
	// parsed says when each path's last good parse was stored (activity generation and time).
	parsed map[string]seenMark
	// notify gets every change (SetNotify).
	notify func(Change)
	// own is set while the index refreshes a file quest-map itself just wrote (no hand-edit records).
	own bool

	// Hand edits wait HandEditWait before they are recorded (see decideLocked).
	pending  map[string]*pendingEdit // by path
	timer    *time.Timer
	deferred error // errors of decisions made by the timer, returned by the next Refresh call
	closed   bool

	// Activity records by project and record id, with when the index first saw each; actGen counts loads.
	actGen  int64
	actSeen map[string]map[string]seenMark
	actGone map[string]time.Time // by project: when records last disappeared from its activity file
}

// HandEditWait is how long a change found in a task file waits before it is recorded as a hand edit. A git
// pull, checkout or branch switch brings the task file together with the activity records the other board
// copy wrote for the same change, and the watcher may read them in either order; the wait lets both arrive.
var HandEditWait = 2 * time.Second

// pendingEdit is a hand edit found in a task file, not yet recorded.
type pendingEdit struct {
	project *board.Project
	path    string
	id      string
	prev    *board.Task // last good parse before the change
	after   *board.Task // latest parse
	base    seenMark    // when prev was stored
	created time.Time
	due     time.Time
}

// seenMark is a point in the index's view of the activity files: the load generation and the time.
type seenMark struct {
	gen int64
	at  time.Time
}

// Open creates a fresh index at path (any existing file is deleted: the index is derived and always
// rebuilt on start). Call Rebuild to fill it.
func Open(path string, b *board.Board, opts Options) (*Index, error) {
	if opts.ClaimTTL <= 0 {
		opts.ClaimTTL = DefaultClaimTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove old index: %w", err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + kbSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create index: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	return &Index{b: b, db: db, opts: opts, spent: map[string]board.Spent{}, broken: map[string]ParseProblem{},
		last: map[string]*board.Task{}, parsed: map[string]seenMark{}, pending: map[string]*pendingEdit{},
		actSeen: map[string]map[string]seenMark{}, actGone: map[string]time.Time{}}, nil
}

// Close records hand edits still waiting and closes the database.
func (ix *Index) Close() error {
	ix.mu.Lock()
	ix.closed = true
	err := ix.flushLocked(true)
	ix.mu.Unlock()
	return errors.Join(err, ix.db.Close())
}

// FlushHandEdits decides every waiting hand edit now instead of after HandEditWait.
func (ix *Index) FlushHandEdits() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.flushLocked(true)
}

// Board returns the board the index reads.
func (ix *Index) Board() *board.Board { return ix.b }

// ClaimTTL returns how long a doing task may go without a write before its claim is stale.
func (ix *Index) ClaimTTL() time.Duration { return ix.opts.ClaimTTL }

// ParseProblems lists task files that do not parse, sorted by path.
func (ix *Index) ParseProblems() ([]ParseProblem, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []ParseProblem
	for _, p := range ix.broken {
		out = append(out, p)
	}
	rows, err := ix.db.Query(`SELECT path, id, parse_error FROM tasks WHERE parse_error IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ParseProblem
		if err := rows.Scan(&p.Path, &p.ID, &p.Err); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pages, err := ix.PageProblems()
	if err != nil {
		return nil, err
	}
	out = append(out, pages...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Renumbered lists duplicate ids resolved since the index was opened.
func (ix *Index) Renumbered() []Renumbered {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return append([]Renumbered(nil), ix.renumbered...)
}

// fileInfo is one task file read from disk.
type fileInfo struct {
	project *board.Project
	path    string
	data    []byte
	hash    string
	mtime   time.Time
	task    *board.Task
	err     error
}

// readRetryFor is how long a failing read is retried on Windows, where another process (an editor, antivirus,
// a search indexer) holding the file can make it fail for a moment.
var readRetryFor = time.Second

// readFileRetry reads a file, retrying with backoff on Windows. A missing file is not retried.
func readFileRetry(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil || errors.Is(err, os.ErrNotExist) || runtime.GOOS != "windows" {
		return data, err
	}
	deadline := time.Now().Add(readRetryFor)
	for wait := 10 * time.Millisecond; time.Now().Before(deadline); wait = min(wait*2, 200*time.Millisecond) {
		time.Sleep(wait)
		if data, err = os.ReadFile(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return data, err
		}
	}
	return nil, err
}

func readTaskFile(p *board.Project, path string, loc *time.Location) (*fileInfo, error) {
	data, err := readFileRetry(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	fi := &fileInfo{project: p, path: path, data: data, hash: hex.EncodeToString(sum[:]), mtime: st.ModTime()}
	fi.task, fi.err = board.ParseIn(data, loc)
	if fi.task != nil {
		fi.task.Archived = filepath.Base(filepath.Dir(path)) == "archive"
	}
	return fi, nil
}

// taskFiles lists the .md files of a project's tasks and archive folders.
func taskFiles(p *board.Project) ([]string, error) {
	var out []string
	for _, sub := range []string{"tasks", "archive"} {
		dir := filepath.Join(p.Dir(), sub)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if isTaskFileName(e.Name()) && !e.IsDir() {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func isTaskFileName(name string) bool {
	return strings.HasSuffix(name, ".md") && !strings.HasPrefix(name, ".")
}

// Rebuild clears the index and reads every project, task file and activity file again. Duplicate ids are
// resolved first (§3.5): the file with the later created time keeps the id, the others are renumbered.
func (ix *Index) Rebuild() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	defer ix.emit(Change{Kind: "all"})
	projects, err := ix.b.Projects()
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := p.ReloadConfig(); err != nil {
			return err
		}
	}
	files, err := ix.resolveDuplicatesLocked(projects)
	if err != nil {
		return err
	}
	led, err := ix.b.Ledger()
	if err != nil {
		return err
	}
	ix.spent = led.Spent
	ix.broken = map[string]ParseProblem{}
	ix.last = map[string]*board.Task{}
	ix.parsed = map[string]seenMark{}
	// A rebuild follows lost events (a branch switch, a large pull): changes seen just before it belong to the
	// same burst and are not hand edits.
	ix.pending = map[string]*pendingEdit{}
	ix.armLocked()
	ix.loadActivityLocked(led.Records, true)

	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{"DELETE FROM tasks", "DELETE FROM labels", "DELETE FROM assignees", "DELETE FROM custom", "DELETE FROM blocks", "DELETE FROM tasks_fts",
		"DELETE FROM repos", "DELETE FROM releases", "DELETE FROM usage", "DELETE FROM transitions"} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, p := range projects {
		if err := insertRepos(tx, p); err != nil {
			return err
		}
		if err := insertReleases(tx, p); err != nil {
			return err
		}
	}
	if err := insertActivity(tx, led); err != nil {
		return err
	}
	if err := loadPagesTx(tx, projects); err != nil {
		return err
	}
	for _, fi := range files {
		if fi.err != nil {
			ix.broken[fi.path] = ParseProblem{Path: fi.path, Err: fi.err.Error(), hash: fi.hash}
			continue
		}
		if err := ix.insertTask(tx, fi); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// resolveDuplicatesLocked reads all task files, renumbers files whose id another file already has, and gives
// files stored under another name their id's file name. It returns the files as they are afterwards.
func (ix *Index) resolveDuplicatesLocked(projects []*board.Project) ([]*fileInfo, error) {
	read := func() ([]*fileInfo, error) {
		var all []*fileInfo
		for _, p := range projects {
			paths, err := taskFiles(p)
			if err != nil {
				return nil, err
			}
			for _, path := range paths {
				fi, err := readTaskFile(p, path, ix.b.Location())
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				all = append(all, fi)
			}
		}
		return all, nil
	}
	all, err := read()
	if err != nil {
		return nil, err
	}
	byID := map[string][]*fileInfo{}
	for _, fi := range all {
		if fi.err == nil {
			byID[fi.task.ID] = append(byID[fi.task.ID], fi)
		}
	}
	changed := false
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		group := byID[id]
		keep := pickKeeper(group)
		for _, fi := range group {
			if fi == keep {
				continue
			}
			newID, err := ix.b.Renumber(fi.project, fi.path, keep.project, keep.path, systemActor)
			if newID == "" {
				return nil, fmt.Errorf("renumber duplicate %s (%s): %w", id, fi.path, err)
			}
			ix.renumbered = append(ix.renumbered, Renumbered{Project: fi.project.Slug(), OldID: id, NewID: newID, KeptID: id, At: ix.opts.Now()})
			changed = true
		}
		if !isCanonical(keep) {
			np, err := keep.project.Adopt(keep.path, id)
			if err != nil {
				return nil, err
			}
			changed = changed || np != keep.path
		}
	}
	if !changed {
		return all, nil
	}
	return read()
}

// pickKeeper chooses which of several files with one id keeps it: the later created; on a tie, the one at
// the id's own file name; then the first path.
func pickKeeper(group []*fileInfo) *fileInfo {
	best := group[0]
	for _, fi := range group[1:] {
		bc, fc := best.task.Created, fi.task.Created
		switch {
		case fc.After(bc):
			best = fi
		case fc.Equal(bc):
			if isCanonical(fi) && !isCanonical(best) {
				best = fi
			}
		}
	}
	return best
}

func isCanonical(fi *fileInfo) bool {
	return strings.TrimSuffix(filepath.Base(fi.path), ".md") == fi.task.ID
}

func insertRepos(tx *sql.Tx, p *board.Project) error {
	for _, r := range p.Config().Repos {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO repos(remote, project) VALUES(?, ?)`, r, p.Slug()); err != nil {
			return err
		}
	}
	return nil
}

// insertActivity adds every project's usage and status-change records, each with the task that owns it now
// (after moves and renumbers).
func insertActivity(tx *sql.Tx, led *board.Ledger) error {
	for slug, recs := range led.Records {
		for _, r := range recs {
			switch r.Kind {
			case board.KindUsage:
				var tok board.Tokens
				if r.Tokens != nil {
					tok = *r.Tokens
				}
				if _, err := tx.Exec(`INSERT INTO usage(project, task, owner, at, session, client, model, source, input, output,
					cache_read, cache_creation, total, time_s, cost) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					slug, nullStr(r.TaskID()), nullStr(led.Owner(r)), ts(r.At), r.Session, r.Client, r.Model, r.Source,
					tok.Input, tok.Output, tok.CacheRead, tok.CacheCreation, tok.Total, r.TimeS, r.CostUSD); err != nil {
					return err
				}
			case board.KindStatus:
				if _, err := tx.Exec(`INSERT INTO transitions(project, task, owner, at, from_status, to_status, actor, session, client, model)
					VALUES(?,?,?,?,?,?,?,?,?,?)`, slug, nullStr(r.TaskID()), nullStr(led.Owner(r)), ts(r.At), r.From, r.To, r.Actor,
					r.Session, r.Client, r.Model); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// insertTask adds a parsed file's row, replacing any row for the same path or id.
func (ix *Index) insertTask(tx *sql.Tx, fi *fileInfo) error {
	t := fi.task
	ix.last[fi.path] = t
	ix.parsed[fi.path] = seenMark{ix.actGen, time.Now()}
	if err := deleteTask(tx, `path = ?`, fi.path); err != nil {
		return err
	}
	if err := deleteTask(tx, `id = ?`, t.ID); err != nil {
		return err
	}
	updated := fi.mtime.UTC()
	if n := len(t.Log); n > 0 {
		updated = t.Log[n-1].Time.UTC()
	}
	var claimBy, claimAt any
	if t.Claim != nil {
		claimBy, claimAt = t.Claim.By, ts(t.Claim.At)
	}
	var pipeName, pipeAt any
	if t.Pipeline != nil {
		pipeName, pipeAt = t.Pipeline.Name, t.Pipeline.At
	}
	var logText strings.Builder
	for _, e := range t.Log {
		logText.WriteString(e.Text)
		logText.WriteByte('\n')
	}
	sp := ix.spent[t.ID]
	res, err := tx.Exec(`INSERT INTO tasks(id, project, path, archived, title, status, doer, priority, prank, discipline,
		milestone, asset, asset_name, pipeline_name, pipeline_at, found_in, fixed_in, moved_from, url, claim_by, claim_at,
		created, updated, body, log, spent_tokens, spent_seconds, spent_records, spent_self, spent_any, parse_error, size, hash)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
		t.ID, fi.project.Slug(), fi.path, t.Archived, t.Title, string(t.Status), string(t.Doer), string(t.Priority),
		effectivePriority(t.Priority).Rank(), nullStr(t.Discipline), nullStr(t.Milestone), nullStr(t.Asset), nullStr(assetName(t.Asset)),
		pipeName, pipeAt, nullStr(t.FoundIn), nullStr(t.FixedIn), nullStr(t.MovedFrom), nullStr(t.URL), claimBy, claimAt,
		ts(t.Created), ts(updated), t.Body, logText.String(), sp.Tokens.Sum(), int64(sp.Time/time.Second), sp.Records, sp.Self,
		sp.Reported, len(fi.data), fi.hash)
	if err != nil {
		return fmt.Errorf("index %s: %w", t.ID, err)
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tasks_fts(rowid, title, body, log) VALUES(?,?,?,?)`, rowid, t.Title, t.Body, logText.String()); err != nil {
		return err
	}
	for k, v := range t.Custom {
		v = strings.NewReplacer("\x1e", " ", "\x1f", " ").Replace(v) // the separators Find uses to read them back
		if _, err := tx.Exec(`INSERT INTO custom(task, key, value) VALUES(?, ?, ?)`, t.ID, k, v); err != nil {
			return err
		}
	}
	for _, a := range t.Assignees {
		if _, err := tx.Exec(`INSERT INTO assignees(task, name) VALUES(?, ?)`, t.ID, a); err != nil {
			return err
		}
	}
	for _, l := range t.Labels {
		if _, err := tx.Exec(`INSERT INTO labels(task, label) VALUES(?, ?)`, t.ID, l); err != nil {
			return err
		}
	}
	for _, bl := range t.BlockedBy {
		if _, err := tx.Exec(`INSERT INTO blocks(task, blocker) VALUES(?, ?)`, t.ID, bl); err != nil {
			return err
		}
	}
	return nil
}

func effectivePriority(p board.Priority) board.Priority {
	if p == "" {
		return board.Normal
	}
	return p
}

// assetName returns the last segment of an asset path without its extension, so asset:SM_Bridge* matches
// "/Game/Props/SM_Bridge_Mid" as well as "SM_Bridge_Mid".
func assetName(a string) string {
	if a == "" {
		return ""
	}
	a = a[strings.LastIndexAny(a, `/\`)+1:]
	if i := strings.LastIndex(a, "."); i > 0 {
		a = a[:i]
	}
	return a
}

// deleteTask removes the rows (task, fts, labels, blocks) matching a condition on tasks.
func deleteTask(tx *sql.Tx, cond string, arg any) error {
	rows, err := tx.Query(`SELECT rowid, id FROM tasks WHERE `+cond, arg)
	if err != nil {
		return err
	}
	type hit struct {
		rowid int64
		id    string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.rowid, &h.id); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, h)
	}
	rows.Close()
	for _, h := range hits {
		for _, q := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM tasks_fts WHERE rowid = ?`, h.rowid},
			{`DELETE FROM labels WHERE task = ?`, h.id},
			{`DELETE FROM assignees WHERE task = ?`, h.id},
			{`DELETE FROM custom WHERE task = ?`, h.id},
			{`DELETE FROM blocks WHERE task = ?`, h.id},
			{`DELETE FROM tasks WHERE rowid = ?`, h.rowid},
		} {
			if _, err := tx.Exec(q.sql, q.arg); err != nil {
				return err
			}
		}
	}
	return nil
}

// projectOfPath returns the project a task file belongs to: root/projects/<slug>/(tasks|archive)/X.md.
func (ix *Index) projectOfPath(path string) (*board.Project, bool) {
	dir := filepath.Dir(path)
	sub := filepath.Base(dir)
	if sub != "tasks" && sub != "archive" && sub != "kb" {
		return nil, false
	}
	projDir := filepath.Dir(dir)
	if filepath.Clean(filepath.Dir(projDir)) != filepath.Clean(filepath.Join(ix.b.Root(), "projects")) {
		return nil, false
	}
	p, err := ix.b.Project(filepath.Base(projDir))
	if err != nil {
		return nil, false
	}
	return p, true
}

// RefreshFile brings one task file's row up to date: added, changed, or removed. A file whose content
// matches the indexed hash is skipped, which is how the server's own writes (already indexed) are not
// processed twice. A file that no longer parses keeps its last good row, flagged with the error. A file
// that now duplicates another file's id is renumbered (§3.5), and a file stored under another name than its
// id gets its id's name. It reports whether the index changed.
func (ix *Index) RefreshFile(path string) (bool, error) {
	path = filepath.Clean(path)
	p, ok := ix.projectOfPath(path)
	if !ok || !isTaskFileName(filepath.Base(path)) {
		return false, nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	changed, err := ix.refreshLocked(p, path, 0)
	if changed {
		ix.emit(Change{Kind: "task", Project: p.Slug(), Path: path})
	}
	return changed, errors.Join(err, ix.takeDeferredLocked())
}

// RefreshAfterWrite is RefreshFile for a file quest-map itself just wrote: the change is already in the
// activity file, so it is not recorded again as a hand edit. Call it right after each write, so the
// watcher's later event for the same content is skipped as an echo. A hand edit of the same file still
// waiting is decided first, so it is not lost under the new content.
func (ix *Index) RefreshAfterWrite(path string) (bool, error) {
	path = filepath.Clean(path)
	p, ok := ix.projectOfPath(path)
	if !ok || !isTaskFileName(filepath.Base(path)) {
		return false, nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var errs []error
	if pe := ix.pending[path]; pe != nil {
		delete(ix.pending, path)
		errs = append(errs, ix.decideLocked(pe))
		ix.armLocked()
	}
	ix.own = true
	defer func() { ix.own = false }()
	changed, err := ix.refreshLocked(p, path, 0)
	if changed {
		ix.emit(Change{Kind: "task", Project: p.Slug(), Path: path})
	}
	return changed, errors.Join(append(errs, err, ix.takeDeferredLocked())...)
}

// maxRefreshDepth bounds the follow-up refreshes one change can cause (renumbering, renames).
const maxRefreshDepth = 6

func (ix *Index) refreshLocked(p *board.Project, path string, depth int) (bool, error) {
	if depth > maxRefreshDepth {
		return false, fmt.Errorf("refresh %s: ids keep changing; run quest-map reindex", path)
	}
	fi, err := readTaskFile(p, path, ix.b.Location())
	if errors.Is(err, os.ErrNotExist) {
		_, wasBroken := ix.broken[path]
		delete(ix.broken, path)
		n, err := ix.deletePaths(path)
		return wasBroken || n > 0, err
	}
	if err != nil {
		return false, err
	}

	var oldHash, oldErr sql.NullString
	err = ix.db.QueryRow(`SELECT hash, parse_error FROM tasks WHERE path = ?`, path).Scan(&oldHash, &oldErr)
	hasRow := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if hasRow && oldHash.String == fi.hash && (!oldErr.Valid || fi.err != nil) {
		return false, nil // an echo of a write already indexed, or the same broken content again
	}
	if bp, ok := ix.broken[path]; ok && bp.hash == fi.hash {
		return false, nil
	}

	if fi.err != nil {
		if hasRow {
			_, err := ix.db.Exec(`UPDATE tasks SET parse_error = ?, hash = ?, size = ? WHERE path = ?`, fi.err.Error(), fi.hash, len(fi.data), path)
			return err == nil, err
		}
		ix.broken[path] = ParseProblem{Path: path, Err: fi.err.Error(), hash: fi.hash}
		return true, nil
	}
	delete(ix.broken, path)

	// Another file already has this id?
	var otherPath string
	err = ix.db.QueryRow(`SELECT path FROM tasks WHERE id = ? AND path != ?`, fi.task.ID, path).Scan(&otherPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		op, _ := ix.projectOfPath(otherPath)
		if op == nil {
			op = p
		}
		other, rerr := readTaskFile(op, otherPath, ix.b.Location())
		switch {
		case errors.Is(rerr, os.ErrNotExist):
			// The other file is gone; insertTask below replaces its row.
		case rerr != nil:
			return false, rerr
		case other.err != nil:
			// The other file holds this id's last good row but does not parse now. Keep that row; this file
			// waits until the other one is fixed.
			ix.broken[path] = ParseProblem{Path: path, ID: fi.task.ID, hash: fi.hash,
				Err: fmt.Sprintf("id %s is also used by %s, which cannot be read; fix that file first", fi.task.ID, otherPath)}
			return true, nil
		case other.task.ID != fi.task.ID:
			// The other file's id changed since it was indexed (a checkout, a hand edit): update its row first.
			if _, err := ix.refreshLocked(op, otherPath, depth+1); err != nil {
				return false, err
			}
			return ix.refreshLocked(p, path, depth+1)
		default:
			return true, ix.renumberLocked(other, fi, depth)
		}
	}

	if !isCanonical(fi) {
		np, err := p.Adopt(path, fi.task.ID)
		if err != nil {
			return false, err
		}
		if np != path {
			_, err1 := ix.refreshLocked(p, path, depth+1)
			_, err2 := ix.refreshLocked(p, np, depth+1)
			return true, errors.Join(err1, err2)
		}
	}

	// A change not written by quest-map: compared with the last good parse (also when the file was broken in
	// between), it may be a person's hand edit. It is queued after the row is committed and recorded later,
	// unless it turns out to come from another board copy (decideLocked).
	var cand *pendingEdit
	if depth == 0 && !ix.own && hasRow {
		if prev := ix.last[path]; prev != nil && prev.ID == fi.task.ID {
			cand = &pendingEdit{project: p, path: path, id: prev.ID, prev: prev, after: fi.task, base: ix.parsed[path]}
		}
	}

	prevLast := ix.last[path]
	prevParsed, hadParsed := ix.parsed[path]
	tx, err := ix.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	err = ix.insertTask(tx, fi)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		// The row did not change, so neither does what the next event is compared with.
		if prevLast != nil {
			ix.last[path] = prevLast
		} else {
			delete(ix.last, path)
		}
		if hadParsed {
			ix.parsed[path] = prevParsed
		} else {
			delete(ix.parsed, path)
		}
		return false, err
	}
	if cand != nil {
		ix.queueHandEditLocked(cand)
	}
	return true, nil
}

// queueHandEditLocked adds a possible hand edit to the waiting list. A later change of the same file joins
// the waiting one (one record set for prev → latest), unless quest-map wrote the file in between.
func (ix *Index) queueHandEditLocked(c *pendingEdit) {
	now := time.Now()
	if pe := ix.pending[c.path]; pe != nil {
		delete(ix.pending, c.path)
		if pe.after == c.prev && pe.id == c.id {
			c.prev, c.base, c.created = pe.prev, pe.base, pe.created
		} else {
			ix.deferred = errors.Join(ix.deferred, ix.decideLocked(pe))
		}
	}
	if c.created.IsZero() {
		c.created = now
	}
	c.due = now.Add(HandEditWait)
	if HandEditWait <= 0 || ix.closed {
		ix.deferred = errors.Join(ix.deferred, ix.decideLocked(c))
		return
	}
	ix.pending[c.path] = c
	ix.armLocked()
}

// armLocked sets the timer for the earliest waiting hand edit (or stops it when none waits).
func (ix *Index) armLocked() {
	if ix.timer != nil {
		ix.timer.Stop()
		ix.timer = nil
	}
	if ix.closed || len(ix.pending) == 0 {
		return
	}
	var first time.Time
	for _, pe := range ix.pending {
		if first.IsZero() || pe.due.Before(first) {
			first = pe.due
		}
	}
	ix.timer = time.AfterFunc(max(time.Until(first), 0), func() {
		ix.mu.Lock()
		defer ix.mu.Unlock()
		if !ix.closed {
			ix.deferred = errors.Join(ix.deferred, ix.flushLocked(false))
		}
	})
}

// flushLocked decides the waiting hand edits that are due (all of them if all is set).
func (ix *Index) flushLocked(all bool) error {
	now := time.Now()
	paths := make([]string, 0, len(ix.pending))
	for path, pe := range ix.pending {
		if all || !pe.due.After(now) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var errs []error
	for _, path := range paths {
		pe := ix.pending[path]
		delete(ix.pending, path)
		errs = append(errs, ix.decideLocked(pe))
	}
	ix.armLocked()
	if all {
		errs = append(errs, ix.takeDeferredLocked())
	}
	return errors.Join(errs...)
}

func (ix *Index) takeDeferredLocked() error {
	err := ix.deferred
	ix.deferred = nil
	return err
}

// noteTextMax is how much of a log line a note record keeps (board's limit).
const noteTextMax = 200

// decideLocked records a waiting hand edit, unless the activity file shows it came from another copy of the
// board. The project's activity file is read from disk now; a record about the task counts as arriving with
// the change when the index first saw it after the previous version of the file was indexed and no earlier
// than HandEditWait before the change was found.
//   - Such a status, note or edit record written by another origin: the change was synced (git pull,
//     checkout, merge) and that copy already recorded it. Nothing is written.
//   - Records the index had seen disappeared from the activity file in that time: the history was rewound
//     (checkout of an older commit, branch switch, reset). Nothing is written.
//   - Otherwise records arriving with the change explain parts of it (a status record to the same status, a
//     note with the same text, an edit record naming the field); the rest is recorded as a person's hand edit.
func (ix *Index) decideLocked(pe *pendingEdit) error {
	h := board.DiffHandEdit(pe.prev, pe.after)
	if h.IsZero() {
		return nil
	}
	recs, _, err := pe.project.Activity()
	if err != nil {
		return err
	}
	slug := pe.project.Slug()
	ix.loadActivityLocked(map[string][]board.Record{slug: recs}, false)
	arrived := func(m seenMark) bool {
		return m.gen > pe.base.gen && !m.at.Before(pe.created.Add(-HandEditWait))
	}
	if gone, ok := ix.actGone[slug]; ok && gone.After(pe.base.at) && !gone.Before(pe.created.Add(-HandEditWait)) {
		return nil
	}
	known := ix.actSeen[slug]
	self := ix.b.Origin()
	for _, r := range recs {
		if r.TaskID() != pe.id || r.Kind == board.KindUsage || !arrived(known[r.ID]) {
			continue
		}
		if r.Origin != "" && r.Origin != self {
			return nil
		}
		switch r.Kind {
		case board.KindStatus:
			if h.From != "" && r.To == string(h.To) {
				h.From, h.To = "", ""
			}
		case board.KindNote:
			for i, n := range h.Notes {
				if truncRunes(n, noteTextMax) == r.Text {
					h.Notes = append(h.Notes[:i:i], h.Notes[i+1:]...)
					break
				}
			}
		case board.KindEdit:
			var keep []string
			for _, f := range h.Fields {
				if !containsStr(r.Fields, f) {
					keep = append(keep, f)
				}
			}
			h.Fields = keep
		}
	}
	return pe.project.RecordHandEdit(pe.id, h)
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// loadActivityLocked notes the activity records the index now sees: new ones get the current generation and
// time, ones that disappeared mark their project's actGone. With full set, projects not in recs are dropped.
func (ix *Index) loadActivityLocked(recs map[string][]board.Record, full bool) {
	ix.actGen++
	mark := seenMark{ix.actGen, time.Now()}
	for slug, rs := range recs {
		known := ix.actSeen[slug]
		if known == nil {
			known = make(map[string]seenMark, len(rs))
			ix.actSeen[slug] = known
		}
		present := make(map[string]bool, len(rs))
		for _, r := range rs {
			present[r.ID] = true
			if _, ok := known[r.ID]; !ok {
				known[r.ID] = mark
			}
		}
		if len(present) < len(known) {
			for id := range known {
				if !present[id] {
					delete(known, id)
					ix.actGone[slug] = mark.at
				}
			}
		}
	}
	if full {
		for slug := range ix.actSeen {
			if _, ok := recs[slug]; !ok {
				delete(ix.actSeen, slug)
				delete(ix.actGone, slug)
			}
		}
	}
}

// renumberLocked resolves two files with one id: the later created keeps it (§3.5), the other is renumbered,
// and every file involved is refreshed.
func (ix *Index) renumberLocked(a, b *fileInfo, depth int) error {
	keep := pickKeeper([]*fileInfo{a, b})
	lose := a
	if keep == a {
		lose = b
	}
	id := keep.task.ID
	newID, err := ix.b.Renumber(lose.project, lose.path, keep.project, keep.path, systemActor)
	if newID == "" {
		return err
	}
	ix.renumbered = append(ix.renumbered, Renumbered{Project: lose.project.Slug(), OldID: id, NewID: newID, KeptID: id, At: ix.opts.Now()})
	errs := []error{err}
	keptPath, aerr := keep.project.Adopt(keep.path, id)
	errs = append(errs, aerr)
	seen := map[string]bool{}
	for _, f := range []struct {
		p    *board.Project
		path string
	}{{keep.project, keep.path}, {lose.project, lose.path}, {keep.project, keptPath},
		{lose.project, lose.project.TaskPath(newID, lose.task.Archived)}} {
		if seen[f.path] {
			continue
		}
		seen[f.path] = true
		_, err := ix.refreshLocked(f.p, f.path, depth+1)
		errs = append(errs, err)
	}
	errs = append(errs, ix.refreshActivityLocked())
	return errors.Join(errs...)
}

// deletePaths removes the rows of task files at these paths and reports how many there were.
func (ix *Index) deletePaths(paths ...string) (int, error) {
	for _, path := range paths {
		delete(ix.last, path)
		delete(ix.parsed, path)
	}
	tx, err := ix.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, path := range paths {
		var c int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE path = ?`, path).Scan(&c); err != nil {
			return 0, err
		}
		if c == 0 {
			continue
		}
		n += c
		if err := deleteTask(tx, `path = ?`, path); err != nil {
			return 0, err
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, tx.Commit()
}

// RefreshGone updates the index after a folder under projects/ was removed or moved away: rows of task files
// under it are dropped, and for a project folder its repos and activity go too. A folder that still exists is
// left alone.
func (ix *Index) RefreshGone(dir string) error {
	dir = filepath.Clean(dir)
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	defer ix.emit(Change{Kind: "all"})
	prefix := dir + string(filepath.Separator)
	rows, err := ix.db.Query(`SELECT path FROM tasks WHERE substr(path, 1, ?) = ?`, len(prefix), prefix)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, path)
	}
	rows.Close()
	for path := range ix.broken {
		if strings.HasPrefix(path, prefix) {
			delete(ix.broken, path)
		}
	}
	if _, err := ix.deletePaths(paths...); err != nil {
		return err
	}
	for _, stmt := range []string{`DELETE FROM kb_fts WHERE rowid IN (SELECT id FROM kb WHERE substr(path, 1, ?) = ?)`,
		`DELETE FROM kb WHERE substr(path, 1, ?) = ?`} {
		if _, err := ix.db.Exec(stmt, len(prefix), prefix); err != nil {
			return err
		}
	}
	if filepath.Dir(dir) != filepath.Clean(filepath.Join(ix.b.Root(), "projects")) {
		return nil
	}
	slug := filepath.Base(dir)
	ix.b.Forget(slug)
	if _, err := ix.db.Exec(`DELETE FROM repos WHERE project = ?`, slug); err != nil {
		return err
	}
	return ix.refreshActivityLocked()
}

// RefreshActivity re-reads the activity files after one changed. All of them are read again, because moves
// and renumbers link records across projects: usage and status-change rows, and every task's spent totals.
func (ix *Index) RefreshActivity(slug string) error {
	if _, err := ix.b.Project(slug); err != nil {
		return err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	defer ix.emit(Change{Kind: "activity", Project: slug})
	return errors.Join(ix.refreshActivityLocked(), ix.takeDeferredLocked())
}

func (ix *Index) refreshActivityLocked() error {
	led, err := ix.b.Ledger()
	if err != nil {
		return err
	}
	ix.loadActivityLocked(led.Records, true)
	ix.spent = led.Spent
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM usage`, `DELETE FROM transitions`,
		`UPDATE tasks SET spent_tokens = 0, spent_seconds = 0, spent_records = 0, spent_self = 0, spent_any = 0`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	if err := insertActivity(tx, led); err != nil {
		return err
	}
	for id, sp := range led.Spent {
		if _, err := tx.Exec(`UPDATE tasks SET spent_tokens = ?, spent_seconds = ?, spent_records = ?, spent_self = ?, spent_any = ? WHERE id = ?`,
			sp.Tokens.Sum(), int64(sp.Time/time.Second), sp.Records, sp.Self, sp.Reported, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RefreshProject re-reads a project's project.toml (repos) after a hand edit.
func (ix *Index) RefreshProject(slug string) error {
	p, err := ix.b.Project(slug)
	if err != nil {
		return err
	}
	if err := p.ReloadConfig(); err != nil {
		return err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM repos WHERE project = ?`, slug); err != nil {
		return err
	}
	if err := insertRepos(tx, p); err != nil {
		return err
	}
	defer ix.emit(Change{Kind: "project", Project: slug})
	return tx.Commit()
}

// ProjectForRemote returns the project slug linked to a remote key, or "".
func (ix *Index) ProjectForRemote(remote string) (string, error) {
	var slug string
	err := ix.db.QueryRow(`SELECT project FROM repos WHERE remote = ?`, remote).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return slug, err
}

// SetNotify sets a function called after every change to the index: a task row (Kind "task"), activity
// totals ("activity"), a project's repos ("project"), or everything ("all"). It is called with the index
// lock held, so it must not call back into the index; hand the change off (for example to a channel).
func (ix *Index) SetNotify(fn func(Change)) {
	ix.mu.Lock()
	ix.notify = fn
	ix.mu.Unlock()
}

// Notify sends a change that did not come from the index itself (suite files) to the SetNotify function.
func (ix *Index) Notify(c Change) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.emit(c)
}

func (ix *Index) emit(c Change) {
	if ix.notify != nil {
		ix.notify(c)
	}
}

// insertReleases loads a project's releases.toml into the releases table. A file that does not parse is
// skipped (the task rows do not depend on it); the error shows up when releases are listed.
func insertReleases(tx *sql.Tx, p *board.Project) error {
	rels, err := p.Releases()
	if err != nil {
		return nil
	}
	for _, r := range rels {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO releases(project, id, target, revision, date, notes) VALUES(?,?,?,?,?,?)`,
			p.Slug(), r.ID, r.Target, r.Revision, ts(r.Date), r.Notes); err != nil {
			return err
		}
	}
	return nil
}

// RefreshReleases re-reads a project's releases.toml.
func (ix *Index) RefreshReleases(slug string) error {
	p, err := ix.b.Project(slug)
	if err != nil {
		return err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM releases WHERE project = ?`, slug); err != nil {
		return err
	}
	if err := insertReleases(tx, p); err != nil {
		return err
	}
	defer ix.emit(Change{Kind: "releases", Project: slug})
	return tx.Commit()
}
