package board

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// originFile holds the id of this copy of the board. It lives in .run/, which is never synced (.gitignore),
// so each machine that opens a synced board gets its own id.
const originFile = "origin"

// loadOrigin reads root/.run/origin, creating it with a new random id if it is missing.
func loadOrigin(root string) (string, error) {
	path := filepath.Join(root, ".run", originFile)
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// Origin returns the id of this copy of the board, stamped on every activity record it writes.
func (b *Board) Origin() string { return b.origin }

// appendActivity adds records to an activity file, stamping them with this board copy's origin.
func (b *Board) appendActivity(path string, recs ...Record) error {
	for i := range recs {
		if recs[i].Origin == "" {
			recs[i].Origin = b.origin
		}
	}
	return appendRecords(path, recs...)
}

// ensureLines makes sure a text file in the board root holds each line, creating it with header first if
// it is missing and appending only the missing lines otherwise. A user's own lines are left alone.
func ensureLines(path, header string, lines ...string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeFileAtomic(path, []byte(header+strings.Join(lines, "\n")+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, l := range lines {
		if !have[l] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += strings.Join(missing, "\n") + "\n"
	return writeFileAtomic(path, []byte(out), 0o644)
}

// Ledger is the whole board's activity read in one pass: each task's spent total, and the task each record
// belongs to once moves and renumbers are followed.
type Ledger struct {
	// Spent holds the usage totals by task id.
	Spent map[string]Spent
	// Records holds each project's activity records in time order, by project slug.
	Records map[string][]Record
	owner   map[string]string // record id → owning task, where it differs from the record's task
	counts  map[string]bool   // usage record id → counts (not a self-report replaced by measured data)
}

// Counts reports whether a usage record counts towards totals: a self-report from a session that has measured
// records in the same activity file does not.
func (l *Ledger) Counts(r Record) bool { return l.counts[r.ID] }

// Owner returns the task a record belongs to: its own task, unless the task was later moved or renumbered.
func (l *Ledger) Owner(r Record) string {
	if o, ok := l.owner[r.ID]; ok {
		return o
	}
	return r.TaskID()
}

// link passes records of one task id on to another: a move (all earlier records) or a renumber (earlier
// records written by takeOrigin).
type link struct {
	to, takeOrigin, cutoff string
	renumber               bool
}

// Ledger reads every project's activity file. Spent rules (§3.6): a self-reported record does not count when
// its session has measured records in the same activity file; records follow a task through moves (to its new
// id) and through a duplicate-id renumber (only the records written by the renumbered copy's machine).
func (b *Board) Ledger() (*Ledger, error) {
	ps, err := b.Projects()
	if err != nil {
		return nil, err
	}
	counts := map[string]bool{}
	l := &Ledger{Spent: map[string]Spent{}, Records: map[string][]Record{}, owner: map[string]string{}, counts: counts}
	links := map[string][]link{}
	for _, p := range ps {
		recs, _, err := p.Activity()
		if err != nil {
			return nil, err
		}
		l.Records[p.slug] = recs
		measured := measuredSessions(recs)
		for _, r := range recs {
			switch {
			case r.Kind == KindUsage:
				counts[r.ID] = countsUsage(r, measured)
			case r.Kind == KindEdit && r.From != "" && r.TaskID() != "" && slices.Contains(r.Fields, "moved_from"):
				links[r.From] = append(links[r.From], link{to: r.TaskID(), takeOrigin: r.TakeOrigin, cutoff: r.ID,
					renumber: slices.Contains(r.Fields, "id")})
			}
		}
	}
	for _, p := range ps {
		for _, r := range l.Records[p.slug] {
			id := r.TaskID()
			if id == "" {
				continue
			}
			owner := resolveOwner(r, links)
			if owner != id {
				l.owner[r.ID] = owner
			}
			if r.Kind == KindUsage {
				s := l.Spent[owner]
				s.add(r, counts[r.ID])
				l.Spent[owner] = s
			}
		}
	}
	return l, nil
}

// resolveOwner follows links from a record's task to the task that owns it now.
func resolveOwner(r Record, links map[string][]link) string {
	id := r.TaskID()
	for hop := 0; hop < maxMoveHops; hop++ {
		next := ""
		for _, ln := range links[id] {
			if r.ID >= ln.cutoff {
				continue // written after the move or renumber: it stays
			}
			if ln.renumber && (ln.takeOrigin == "" || r.Origin != ln.takeOrigin) {
				continue
			}
			next = ln.to
			break
		}
		if next == "" {
			return id
		}
		id = next
	}
	return id
}

// birthOrigin returns the origin of the record that started task t (id) in recs: its creation, or the move or
// renumber that gave it this id. ok is false when there is none or the match is ambiguous.
func birthOrigin(recs []Record, id string, t *Task) (origin string, ok bool) {
	found := map[string]bool{}
	for _, r := range recs {
		if r.TaskID() != id {
			continue
		}
		switch {
		case t.MovedFrom != "" && r.Kind == KindEdit && r.From == t.MovedFrom && slices.Contains(r.Fields, "moved_from"):
			found[r.Origin] = true
		case t.MovedFrom == "" && r.Kind == KindStatus && r.From == "" && r.At.Equal(t.Created):
			found[r.Origin] = true
		}
	}
	if len(found) != 1 {
		return "", false
	}
	for o := range found {
		return o, o != ""
	}
	return "", false
}

// Renumber resolves a duplicate id (§3.5): the task file at path – one whose id the file at keptPath also
// has – gets the next free id of its project lp, its moved_from set to the old id and a log line, and is
// renamed to its new file name. It is rewritten in place first, so a crash never leaves the task twice. The
// kept file (in project kp) gets a log line too. Earlier activity of the old id written by the renumbered
// copy's machine follows it (the renumber record's take_origin); when the two copies cannot be told apart by
// machine, it all stays with the kept task and both log lines say so.
func (b *Board) Renumber(lp *Project, path string, kp *Project, keptPath string, by Actor) (string, error) {
	first, second := lp, kp
	if second.slug < first.slug {
		first, second = second, first
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	if second != first {
		second.mu.Lock()
		defer second.mu.Unlock()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	t, err := ParseIn(data, b.loc)
	if err != nil {
		return "", err
	}
	t.Archived = filepath.Base(filepath.Dir(path)) == "archive"
	oldID := t.ID
	keptData, err := os.ReadFile(keptPath)
	if err != nil {
		return "", err
	}
	kept, err := ParseIn(keptData, b.loc)
	if err != nil {
		return "", err
	}
	if kept.ID != oldID {
		return "", fmt.Errorf("renumber %s: %s no longer has that id", oldID, filepath.Base(keptPath))
	}

	lrecs, _, err := readRecords(lp.activityPath())
	if err != nil {
		return "", err
	}
	krecs := lrecs
	if kp != lp {
		if krecs, _, err = readRecords(kp.activityPath()); err != nil {
			return "", err
		}
	}
	takeOrigin := ""
	if lo, ok := birthOrigin(lrecs, oldID, t); ok {
		if ko, ok := birthOrigin(krecs, oldID, kept); ok && ko != lo {
			takeOrigin = lo
		}
	}

	newID, err := lp.allocIDLocked()
	if err != nil {
		return "", err
	}
	now := b.now()
	usageNote := "; its earlier activity came along"
	if takeOrigin == "" {
		usageNote = "; its earlier activity could not be told apart and stays with " + oldID
	}
	t.ID, t.MovedFrom = newID, oldID
	t.appendLog(now, by.who(), "", fmt.Sprintf("renumbered from %s: another task already had that id%s", oldID, usageNote))
	out, err := t.Render()
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, out, 0o644); err != nil {
		return "", err
	}
	var errs []error
	if newPath := lp.taskPath(newID, t.Archived); filepath.Clean(path) != filepath.Clean(newPath) {
		// If this fails the file keeps its old name with the new id inside; Adopt renames it later.
		errs = append(errs, renameWithRetry(path, newPath))
	}
	errs = append(errs, b.appendActivity(lp.activityPath(), by.record(now, KindEdit, newID, func(r *Record) {
		r.Fields, r.From, r.TakeOrigin, r.Text = []string{"id", "moved_from"}, oldID, takeOrigin, "renumbered from "+oldID
	})))

	keptNote := fmt.Sprintf("a duplicate of %s was renumbered to %s", oldID, newID)
	if takeOrigin == "" {
		keptNote += "; activity recorded under " + oldID + " before this stays here"
	}
	kept.appendLog(now, by.who(), "", keptNote)
	if out, err := kept.Render(); err != nil {
		errs = append(errs, err)
	} else if err := writeFileAtomic(keptPath, out, 0o644); err != nil {
		errs = append(errs, fmt.Errorf("log the renumber on %s: %w", oldID, err))
	} else {
		errs = append(errs, b.appendActivity(kp.activityPath(), by.record(now, KindNote, oldID, func(r *Record) {
			r.Text = keptNote
		})))
	}
	return newID, errors.Join(errs...)
}
