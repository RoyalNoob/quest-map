package board

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// CheckBlockers normalizes a blocked_by list for task id (upper case, trimmed, duplicates and empty entries
// dropped) and checks it: every entry must name an existing task (open or archived, in any project), a task
// cannot wait on itself, and the list must not close a cycle of waits (RPG-2 waits on RPG-3, which waits on
// RPG-2), because no task in a cycle could ever be claimed. id is "" for a task not created yet.
//
// Reading the board stays tolerant: a hand-edited file with an unknown blocker is still accepted, and the
// unknown blocker does not block. The check runs without project locks, so two concurrent edits can still
// close a cycle; it guards against mistakes, not races.
func (b *Board) CheckBlockers(id string, blockers []string) ([]string, error) {
	id = normID(id)
	var out []string
	for _, bl := range blockers {
		bl = normID(bl)
		if bl == "" || slices.Contains(out, bl) {
			continue
		}
		if bl == id {
			return nil, fmt.Errorf("%s cannot be blocked by itself", id)
		}
		if _, err := b.findTask(bl); err != nil {
			if errors.Is(err, ErrNotFound) {
				if to := b.MovedTo(bl); to != "" {
					return nil, fmt.Errorf("blocked_by: no task %s; it was moved to %s", bl, to)
				}
				return nil, fmt.Errorf("blocked_by: no task %s", bl)
			}
			return nil, fmt.Errorf("blocked_by: %w", err)
		}
		out = append(out, bl)
	}
	if id == "" {
		return out, nil
	}
	for _, bl := range out {
		if path := b.waitPath(bl, id, map[string]bool{}); path != nil {
			return nil, fmt.Errorf("blocked_by: %s would wait on itself: %s", id, strings.Join(append([]string{id}, path...), " → "))
		}
	}
	return out, nil
}

// waitPath returns the chain of waits from from to target (from first, target last), or nil if from does not
// wait on target directly or through other tasks. Unknown or unreadable tasks end a chain.
func (b *Board) waitPath(from, target string, seen map[string]bool) []string {
	if from == target {
		return []string{target}
	}
	if seen[from] {
		return nil
	}
	seen[from] = true
	t, err := b.findTask(from)
	if err != nil {
		return nil
	}
	for _, next := range t.BlockedBy {
		if rest := b.waitPath(normID(next), target, seen); rest != nil {
			return append([]string{from}, rest...)
		}
	}
	return nil
}

// findTask reads a task, open or archived, by id in whichever project has its key.
func (b *Board) findTask(id string) (*Task, error) {
	p, err := b.projectOfID(id)
	if err != nil {
		return nil, err
	}
	return p.Get(id)
}

func (b *Board) projectOfID(id string) (*Project, error) {
	i := strings.LastIndex(id, "-")
	if i <= 0 {
		return nil, fmt.Errorf("%q is not a task id like RPG-12", id)
	}
	ps, err := b.Projects()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Key() == id[:i] {
			return p, nil
		}
	}
	return nil, fmt.Errorf("task %s: no project has the key %s: %w", id, id[:i], ErrNotFound)
}

func normID(id string) string { return strings.ToUpper(strings.TrimSpace(id)) }

// MovedTo returns the id a task has now if it was moved away from id (following later moves too), or "".
// It reads the "moved" records of the projects' activity files.
func (b *Board) MovedTo(id string) string {
	id = normID(id)
	to := ""
	for hop := 0; hop < maxMoveHops; hop++ {
		p, err := b.projectOfID(id)
		if err != nil {
			break
		}
		recs, _, err := readRecords(p.activityPath()) // append-only; read without the project lock
		if err != nil {
			break
		}
		next := ""
		for _, r := range recs {
			if r.Kind == KindEdit && r.TaskID() == id && r.To != "" && slices.Contains(r.Fields, "moved") {
				next = r.To // the last move of this id wins
			}
		}
		if next == "" || next == id {
			break
		}
		to, id = next, next
	}
	return to
}

// movedHint adds the new id to a not-found error for a task that was moved away.
func (b *Board) movedHint(id string, err error) error {
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if to := b.MovedTo(id); to != "" {
		return fmt.Errorf("%w; it was moved to %s", err, to)
	}
	return err
}

// Retargeted is a task whose blocked_by was rewritten because a blocker moved.
type Retargeted struct {
	Project *Project
	Task    *Task
}

// RetargetBlockers rewrites blocked_by entries that name oldID to newID in every open task that is not done,
// in all projects, with a log line on each. Move calls it after a task changes id, so tasks that waited on
// the moved task keep waiting on it. Projects are locked one at a time.
func (b *Board) RetargetBlockers(oldID, newID string, by Actor) ([]Retargeted, error) {
	oldID, newID = normID(oldID), normID(newID)
	ps, err := b.Projects()
	if err != nil {
		return nil, err
	}
	var out []Retargeted
	var errs []error
	for _, p := range ps {
		ts, err := p.retargetBlockers(oldID, newID, by)
		for _, t := range ts {
			out = append(out, Retargeted{Project: p, Task: t})
		}
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

func (p *Project) retargetBlockers(oldID, newID string, by Actor) ([]*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tasks, _ := p.listLocked(false) // a file that does not parse cannot be rewritten; List reports it elsewhere
	var out []*Task
	var errs []error
	for _, t := range tasks {
		if t.Status == Done || !slices.ContainsFunc(t.BlockedBy, func(s string) bool { return normID(s) == oldID }) {
			continue
		}
		var next []string
		for _, s := range t.BlockedBy {
			if normID(s) == oldID {
				s = newID
			}
			if !slices.Contains(next, s) {
				next = append(next, s)
			}
		}
		t.BlockedBy = next
		if err := p.commitLocked(t, by, []string{"blocked_by"}, "", fmt.Sprintf("blocker %s moved to %s", oldID, newID), nil); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.ID, err))
			continue
		}
		out = append(out, t)
	}
	return out, errors.Join(errs...)
}
