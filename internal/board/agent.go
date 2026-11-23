package board

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// ErrClaimLost is returned by ClaimIf when the task can no longer be claimed: it is done, archived, claimed
// by another session, or (ClaimUnblockedIf) waits on an open task. A caller picking among candidates moves on
// to the next one; any other error is a real failure.
var ErrClaimLost = errors.New("no longer claimable")

// ClaimIf claims a task like Claim, but only if check accepts the task as it is on disk at that moment.
// Both run under the project lock, so picking and claiming is one check-and-set.
func (p *Project) ClaimIf(id string, by Actor, check func(*Task) error) (*Task, error) {
	return p.claimIf(id, by, check, false)
}

// ClaimUnblockedIf is ClaimIf that also refuses a task whose blocked_by lists a task that is not done (or
// archived), checked under the lock. Unknown blockers do not block, as in the index.
func (p *Project) ClaimUnblockedIf(id string, by Actor, check func(*Task) error) (*Task, error) {
	return p.claimIf(id, by, check, true)
}

func (p *Project) claimIf(id string, by Actor, check func(*Task) error, unblocked bool) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	if err != nil {
		return nil, err
	}
	if err := check(t); err != nil {
		return nil, err
	}
	session := by.Session
	if session == "" {
		session = by.who()
	}
	switch {
	case t.Archived || t.Status == Done:
		return nil, fmt.Errorf("%s is done: %w", id, ErrClaimLost)
	case t.Claim != nil && t.Claim.By != session:
		return nil, fmt.Errorf("%s is claimed by %s: %w", id, t.Claim.By, ErrClaimLost)
	}
	if unblocked {
		for _, b := range t.BlockedBy {
			if p.blockerOpenLocked(b) {
				return nil, fmt.Errorf("%s waits on %s: %w", id, b, ErrClaimLost)
			}
		}
	}
	from := Status("")
	if t.Status != Doing {
		from = t.Status
		t.Status = Doing
	}
	t.Claim = &Claim{By: session, At: p.board.now()}
	return t, p.commitLocked(t, by, []string{"claim"}, from, "claimed", nil)
}

// blockerOpenLocked reports whether blocker id is a task that is neither done nor archived. p.mu is held.
// A blocker in another project is read without that project's lock: task files are replaced atomically,
// so the read sees a whole version, and not taking a second lock avoids lock-order deadlocks.
func (p *Project) blockerOpenLocked(id string) bool {
	id = strings.ToUpper(strings.TrimSpace(id))
	i := strings.LastIndex(id, "-")
	if i <= 0 {
		return false
	}
	q := p
	if id[:i] != p.cfg.Key {
		q = nil
		if ps, err := p.board.Projects(); err == nil {
			for _, o := range ps {
				if o != p && o.Key() == id[:i] {
					q = o
				}
			}
		}
		if q == nil {
			return false
		}
	}
	t, err := q.readLocked(id)
	if err != nil {
		return !errors.Is(err, ErrNotFound) // an unreadable blocker blocks; a missing one does not
	}
	return !t.Archived && t.Status != Done
}

// UpdateIf applies an edit like Update, but only if check accepts the task as it is on disk at that moment.
// Both run under the project lock, so a rule checked by check cannot be undone by a concurrent write. An
// empty note with no edit and no usage is an error, as in Note.
func (p *Project) UpdateIf(id string, by Actor, e Edit, note string, u *Usage, check func(*Task) error) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, err := p.readLocked(id)
	if err != nil {
		return nil, p.board.movedHint(id, err) // p.mu before b.mu, as in blockerOpenLocked
	}
	if t.Archived {
		return nil, fmt.Errorf("%s is archived", id)
	}
	if check != nil {
		if err := check(t); err != nil {
			return nil, err
		}
	}
	changed, from, err := e.apply(t)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 && from == "" && strings.TrimSpace(note) == "" && u.IsZero() {
		return nil, errors.New("nothing to change: pass a field, a note or usage")
	}
	return t, p.commitLocked(t, by, changed, from, note, u)
}

// LinkRepo adds a remote key to the project's repos (no-op if present).
func (p *Project) LinkRepo(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.Contains(p.cfg.Repos, key) {
		return nil
	}
	p.cfg.Repos = append(p.cfg.Repos, key)
	return p.saveConfig()
}

// UnlinkRepo removes a remote key from the project's repos. It reports whether the key was there.
func (p *Project) UnlinkRepo(key string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := slices.Index(p.cfg.Repos, key)
	if i < 0 {
		return false, nil
	}
	p.cfg.Repos = slices.Delete(p.cfg.Repos, i, i+1)
	return true, p.saveConfig()
}

// MilestoneDate returns a milestone's date, or false if it has none (or is unknown).
func (p *Project) MilestoneDate(name string) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.cfg.Milestones {
		if strings.EqualFold(m.Name, name) && m.Date != nil {
			return m.Date.AsTime(time.UTC), true
		}
	}
	return time.Time{}, false
}

// HandEdit describes changes a person made by editing a task file directly, found by comparing the file
// with its previous version.
type HandEdit struct {
	Fields []string // changed front-matter fields and "body" (status not included)
	From   Status   // status before, if it changed
	To     Status
	Notes  []string // log lines added by hand
}

// IsZero reports whether nothing changed.
func (h HandEdit) IsZero() bool { return len(h.Fields) == 0 && h.From == "" && len(h.Notes) == 0 }

// DiffHandEdit compares two versions of a task file.
func DiffHandEdit(before, after *Task) HandEdit {
	var h HandEdit
	add := func(name string, changed bool) {
		if changed {
			h.Fields = append(h.Fields, name)
		}
	}
	add("title", before.Title != after.Title)
	add("doer", before.Doer != after.Doer)
	add("priority", before.Priority != after.Priority)
	add("discipline", before.Discipline != after.Discipline)
	add("milestone", before.Milestone != after.Milestone)
	add("assignees", !slices.Equal(before.Assignees, after.Assignees))
	add("labels", !slices.Equal(before.Labels, after.Labels))
	add("asset", before.Asset != after.Asset)
	add("pipeline", !samePipeline(before.Pipeline, after.Pipeline))
	add("found_in", before.FoundIn != after.FoundIn)
	add("fixed_in", before.FixedIn != after.FixedIn)
	add("blocked_by", !slices.Equal(before.BlockedBy, after.BlockedBy))
	add("url", before.URL != after.URL)
	add("fields", !maps.Equal(before.Custom, after.Custom))
	add("claim", !sameClaim(before.Claim, after.Claim))
	add("moved_from", before.MovedFrom != after.MovedFrom)
	add("body", before.Body != after.Body)
	if before.Status != after.Status {
		h.From, h.To = before.Status, after.Status
	}
	h.Notes = addedLogLines(before.Log, after.Log)
	return h
}

// addedLogLines returns the texts of log entries in after that were not in before. Entries are matched as a
// multiset on (time, who, text), so a line added anywhere – not only at the end – counts, and an entry
// removed by hand does not hide one added in the same save.
func addedLogLines(before, after []LogEntry) []string {
	type key struct {
		at        int64
		who, text string
	}
	k := func(e LogEntry) key { return key{e.Time.Unix(), e.Who, e.Text} }
	seen := map[key]int{}
	for _, e := range before {
		seen[k(e)]++
	}
	var out []string
	for _, e := range after {
		if seen[k(e)] > 0 {
			seen[k(e)]--
			continue
		}
		out = append(out, e.Text)
	}
	return out
}

func samePipeline(a, b *Pipeline) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameClaim(a, b *Claim) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.By == b.By && a.At.Equal(b.At)
}

// RecordHandEdit writes activity records for changes a person made to a task file by hand, so they count
// like the same changes made through quest-map. The file itself is not touched.
func (p *Project) RecordHandEdit(id string, h HandEdit) error {
	if h.IsZero() {
		return nil
	}
	by := Actor{Kind: "person", Name: "hand edit"}
	// Record ids are made under the lock, so they grow in the order the records are appended.
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.board.now()
	var recs []Record
	if len(h.Fields) > 0 {
		recs = append(recs, by.record(now, KindEdit, id, func(r *Record) { r.Fields = h.Fields }))
	}
	if h.From != "" {
		recs = append(recs, by.record(now, KindStatus, id, func(r *Record) { r.From, r.To = string(h.From), string(h.To) }))
	}
	for _, n := range h.Notes {
		if r := []rune(n); len(r) > noteTextMax {
			n = string(r[:noteTextMax])
		}
		recs = append(recs, by.record(now, KindNote, id, func(r *Record) { r.Text = n }))
	}
	return p.board.appendActivity(p.activityPath(), recs...)
}

// ErrNotAllowed is returned when an agent tries a change the board rules do not allow it.
var ErrNotAllowed = errors.New("not allowed")
