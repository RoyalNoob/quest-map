package mcpserver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
)

// digestMax is how many update lines a reply shows before "+N more".
const digestMax = 5

// markSeen sets the session's cursor for a task to the newest activity record, so later replies only report
// what changed after this moment.
func (s *Service) markSeen(c Caller, p *board.Project, id string) {
	ss := s.session(c)
	mu := s.taskLock(ss, id)
	mu.Lock()
	defer mu.Unlock()
	recs, _, err := p.Activity()
	if err != nil || len(recs) == 0 {
		return
	}
	s.mu.Lock()
	if newest := recs[len(recs)-1].ID; newest > ss.cursors[id] {
		ss.cursors[id] = newest
	}
	s.mu.Unlock()
}

// digest returns the update lines for a read (no warning).
func (s *Service) digest(c Caller, p *board.Project, t *board.Task) string {
	_, updates := s.updates(c, p, t, t.Claim)
	return updates
}

// updates reports what others changed on task t, or on tasks it waits on, since this session last touched
// t, and moves the session's cursor past it; reading and moving the cursor is one step per (session, task),
// so siblings sharing a session never both get the same update. The first touch reports nothing, unless the
// session holds the claim: then (after a server restart, say) what changed since the claim is reported.
// claim is the task's claim before the write being answered, so a write that ends the claim (the hand-off to
// review) still gets the warning. warning is set when a person changed the task – any change, notes
// included – since the claim holder's claim.
func (s *Service) updates(c Caller, p *board.Project, t *board.Task, claim *board.Claim) (warning, updates string) {
	ss := s.session(c)
	mu := s.taskLock(ss, t.ID)
	mu.Lock()
	defer mu.Unlock()
	s.mu.Lock()
	cursor, seen := ss.cursors[t.ID]
	s.mu.Unlock()
	mine := claim != nil && claim.By == c.ID

	watched := map[string]bool{t.ID: true}
	for _, b := range t.BlockedBy {
		watched[b] = true
	}
	// Records live in the activity file of each task's project.
	files := map[*board.Project]bool{p: true}
	for _, b := range t.BlockedBy {
		if bp, _, err := s.projectOfID(b); err == nil {
			files[bp] = true
		}
	}
	var fresh []board.Record
	newest := cursor
	for fp := range files {
		recs, _, err := fp.Activity()
		if err != nil {
			continue
		}
		for _, r := range recs {
			if r.ID > newest {
				newest = r.ID
			}
			if !watched[r.TaskID()] || r.Session == c.ID {
				continue
			}
			switch {
			case seen && r.ID <= cursor:
				continue
			case !seen && (!mine || r.At.Before(claim.At)):
				continue
			}
			fresh = append(fresh, r)
		}
	}
	s.mu.Lock()
	if newest > ss.cursors[t.ID] {
		ss.cursors[t.ID] = newest
	}
	s.mu.Unlock()
	if len(fresh) == 0 {
		return "", ""
	}
	slices.SortFunc(fresh, func(a, b board.Record) int { return strings.Compare(a.ID, b.ID) })

	var lines []string
	for _, r := range fresh {
		if l := describe(r, t); l != "" {
			lines = append(lines, l)
		}
		if mine && r.Actor == "person" && r.TaskID() == t.ID && !r.At.Before(claim.At) &&
			(r.Kind == board.KindNote || r.Kind == board.KindStatus || r.Kind == board.KindEdit) {
			warning = fmt.Sprintf("%s changed by a person since your claim – re-read it (task_show).", t.ID)
		}
	}
	if len(lines) == 0 {
		return warning, ""
	}
	if len(lines) > digestMax {
		more := len(lines) - digestMax
		lines = append(lines[:digestMax], fmt.Sprintf("+%d more (task_show %s)", more, t.ID))
	}
	return warning, "Updates: " + strings.Join(lines, " · ")
}

// who names the author of a record for the digest.
func who(r board.Record) string {
	if r.Actor == "agent" && r.Session != "" {
		return r.Session
	}
	if r.Actor != "" {
		return r.Actor
	}
	if r.Session != "" {
		return r.Session
	}
	return "someone"
}

// describe renders one record as a digest line about task t, or "" for records not worth a line.
func describe(r board.Record, t *board.Task) string {
	id := r.TaskID()
	about := id
	if id != t.ID {
		about = fmt.Sprintf("%s (%s waits on it)", id, t.ID)
	}
	switch r.Kind {
	case board.KindNote:
		return fmt.Sprintf("%s commented on %s: %q", who(r), about, r.Text)
	case board.KindStatus:
		if id == t.ID {
			return fmt.Sprintf("%s → %s (by %s)", id, r.To, who(r))
		}
		return fmt.Sprintf("%s → %s", about, r.To)
	case board.KindEdit:
		fields := slices.DeleteFunc(slices.Clone(r.Fields), func(f string) bool { return f == "claim" })
		if len(fields) == 0 {
			return ""
		}
		return fmt.Sprintf("%s changed %s: %s", who(r), about, strings.Join(fields, ", "))
	}
	return ""
}

// announcements returns person notes on the project's tasks labelled "announce" that this session has not
// seen yet (only those written after the session started).
func (s *Service) announcements(c Caller, p *board.Project) string {
	rows, err := s.ix.FindString("label:announce", index.FindOptions{Project: p.Slug(), IncludeArchived: true})
	if err != nil || len(rows) == 0 {
		return ""
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	recs, _, err := p.Activity()
	if err != nil {
		return ""
	}
	ss := s.session(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	for _, r := range recs {
		if r.Kind != board.KindNote || r.Actor != "person" || !ids[r.TaskID()] || r.ID <= ss.announceSeen || !r.At.After(ss.announceSince.Add(-1e9)) {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %q", r.TaskID(), r.Text))
		ss.announceSeen = r.ID
	}
	if len(lines) == 0 {
		return ""
	}
	return "Announcements: " + strings.Join(lines, " · ")
}
