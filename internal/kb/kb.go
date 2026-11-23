// Package kb holds the knowledge base logic the MCP tools and the REST API share (spec §4.5): reading a
// search, and deciding whether a page is still fresh for a working copy.
package kb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/vcs"
)

// Freshness states.
const (
	Fresh   = "fresh"
	Stale   = "stale"
	Unknown = "unknown"
)

// Freshness says whether a page's answer still holds for a working copy.
type Freshness struct {
	Status  string   `json:"status"`            // fresh, stale or unknown
	Changed []string `json:"changed,omitempty"` // sources that changed since the check ("task:RPG-12" for tasks)
	Reason  string   `json:"reason,omitempty"`  // why it is unknown
}

// Query is a parsed kb search: free words plus tag:, source:, stale:yes|no and retired:yes terms.
type Query struct {
	index.PageQuery
	Stale string // "", "yes" or "no"
}

// ParseQuery reads a search. Terms are separated by spaces; "a phrase" stays one word.
func ParseQuery(s string) (Query, error) {
	var q Query
	for _, w := range splitWords(s) {
		key, val, ok := strings.Cut(w, ":")
		if !ok || val == "" || strings.ContainsAny(key, " /\\") {
			q.Words = append(q.Words, w)
			continue
		}
		switch strings.ToLower(key) {
		case "tag":
			q.Tags = append(q.Tags, val)
		case "source":
			q.Source = val
		case "stale":
			if val != "yes" && val != "no" {
				return q, fmt.Errorf("stale:%s: use stale:yes or stale:no", val)
			}
			q.Stale = val
		case "retired":
			q.Retired = val == "yes"
		default:
			q.Words = append(q.Words, w) // e.g. "error:" in a question is a word
		}
	}
	return q, nil
}

func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case (r == ' ' || r == '\t' || r == '\n') && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// gitTimeout bounds every git call a lookup makes.
const gitTimeout = 10 * time.Second

func git(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=", "-C", root}, args...)...)
	cmd.Env = vcs.GitEnv()
	vcs.HideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Revision returns the working copy's current revision: git HEAD. "" when dir is not in a git working copy.
// ponytail: Lore workspaces get no revision yet (pages written there are "unknown"); add it through the Lore
// adapter when a Lore team uses the knowledge base.
func Revision(dir string) string {
	root := vcs.GitRoot(dir)
	if root == "" {
		return ""
	}
	rev, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return rev
}

// validRev is a git commit id. A page's checked.rev comes from a file anyone with the board can edit, so it is
// checked before it reaches git: "--output=<file>" would make git diff write that file.
var validRev = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// Check decides a page's freshness for the working copy at workdir ("" when there is none, as for the web UI
// without a working folder). Path sources are compared with git between the checked revision and the working
// copy; task sources are stale once their task changed status after the check.
func Check(ix *index.Index, row index.PageRow, workdir string) Freshness {
	return (&checker{ix: ix, workdir: workdir}).check(row)
}

// checker checks pages against one working copy. Find gives it the sources of all pages it checks, so pages
// checked at the same revision share one git diff instead of one each.
type checker struct {
	ix         *index.Index
	workdir    string
	resolved   bool
	root, head string
	reason     string              // why there is no working copy to compare with
	union      map[string][]string // revision → path sources of every page checked at it
	diffs      map[string]diffOut  // revision → git diff of its union
}

type diffOut struct {
	files []string
	err   error
}

// repo finds the working copy's root and HEAD once.
func (c *checker) repo() bool {
	if !c.resolved {
		c.resolved = true
		switch c.root = vcs.GitRoot(c.workdir); {
		case c.workdir == "":
			c.reason = "no working copy to compare with"
		case c.root == "":
			c.reason = "the working copy is not a git repository"
		default:
			var err error
			if c.head, err = git(c.root, "rev-parse", "HEAD"); err != nil {
				c.reason = err.Error()
			}
		}
	}
	return c.reason == ""
}

// diff lists the files under paths changed since rev, running git once per revision when Find gave the union.
func (c *checker) diff(rev string, paths []string) ([]string, error) {
	if d, ok := c.diffs[rev]; ok {
		return d.files, d.err
	}
	spec := paths
	if u := c.union[rev]; len(u) > 0 {
		spec = u
	}
	out, err := git(c.root, append([]string{"diff", "--name-only", rev, "--"}, spec...)...)
	if err != nil && len(c.union[rev]) > 0 {
		if _, missing := git(c.root, "cat-file", "-e", rev+"^{commit}"); missing == nil {
			// The revision is here, so one page's bad source (a path outside the repository) failed the
			// shared diff: check those pages one by one.
			delete(c.union, rev)
			return c.diff(rev, paths)
		}
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, filepath.ToSlash(l))
		}
	}
	if c.union[rev] != nil {
		if c.diffs == nil {
			c.diffs = map[string]diffOut{}
		}
		c.diffs[rev] = diffOut{files, err}
	}
	return files, err
}

// under lists the files that one of paths names: the file itself, a file in the folder, or a glob match.
// Case is ignored, so a source spelt differently on Windows still counts (a false stale beats a false fresh).
func under(files, paths []string) []string {
	var out []string
	for _, f := range files {
		lf := strings.ToLower(f)
		for _, p := range paths {
			p = strings.ToLower(strings.TrimSuffix(filepath.ToSlash(p), "/"))
			ok, _ := path.Match(p, lf)
			if ok || p == "" || p == "." || lf == p || strings.HasPrefix(lf, p+"/") {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func (c *checker) check(row index.PageRow) Freshness {
	var changed []string
	var paths []string
	for _, s := range row.Sources {
		switch {
		case s.Task != "":
			if row.Checked == nil || row.Checked.At.IsZero() {
				continue
			}
			if moved, err := c.ix.TaskChangedSince(s.Task, row.Checked.At); err == nil && moved {
				changed = append(changed, "task:"+s.Task)
			}
		case s.Path != "":
			paths = append(paths, s.Path)
		}
	}
	result := func(pathChanges []string, reason string) Freshness {
		all := append(changed, pathChanges...)
		sort.Strings(all)
		switch {
		case len(all) > 0:
			return Freshness{Status: Stale, Changed: all}
		case reason != "":
			return Freshness{Status: Unknown, Reason: reason}
		default:
			return Freshness{Status: Fresh}
		}
	}
	switch {
	case len(paths) == 0:
		if len(changed) == 0 && !hasTask(row.Sources) {
			return result(nil, "no file sources")
		}
		return result(nil, "")
	case row.Checked == nil || row.Checked.Rev == "":
		return result(nil, "checked without a revision")
	case !validRev.MatchString(row.Checked.Rev):
		return result(nil, "the checked revision is not a commit id")
	case !c.repo():
		return result(nil, c.reason)
	}
	// The cache key holds the repository too: a shallow clone without the checked revision and a full clone at
	// the same HEAD answer differently.
	// ponytail: keyed on HEAD, so uncommitted edits to a source are seen only after a commit (or a page write);
	// key on the working tree state too if that matters.
	key := c.head + "@" + c.root
	if status, ch, ok := c.ix.CachedFreshness(row.Path, row.Hash, key); ok {
		if status == Unknown {
			return result(nil, "the checked revision is not in this working copy")
		}
		return result(ch, "")
	}
	files, err := c.diff(row.Checked.Rev, paths)
	if err != nil {
		// Most often the checked revision is not in this clone (shallow, or not fetched yet).
		c.ix.CacheFreshness(row.Path, row.Hash, key, Unknown, nil)
		return result(nil, "the checked revision is not in this working copy")
	}
	ch := under(files, paths)
	for _, p := range missing(c.root, paths, ch) {
		ch = append(ch, p+NotFound)
	}
	status := Fresh
	if len(ch) > 0 {
		status = Stale
	}
	c.ix.CacheFreshness(row.Path, row.Hash, key, status, ch)
	return result(ch, "")
}

// NotFound marks a changed source that has no file in the working copy.
const NotFound = " (not found)"

// missing lists the path sources with no file in the working copy that the diff did not already name (a
// deleted source is a change already). A source that never existed – most often a typo – leaves no diff, and
// must not leave the page fresh.
func missing(root string, paths, changed []string) []string {
	var out []string
	for _, p := range paths {
		if len(under(changed, []string{p})) > 0 {
			continue
		}
		// Glob also answers for a plain path: it returns the path when it exists.
		if m, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(p))); len(m) == 0 {
			out = append(out, p)
		}
	}
	return out
}

func hasTask(src []board.Source) bool {
	for _, s := range src {
		if s.Task != "" {
			return true
		}
	}
	return false
}

// Result is a page found by a search, with its freshness.
type Result struct {
	index.PageRow
	Freshness Freshness `json:"freshness"`
}

// Find runs a search and checks each page's freshness for the working copy workdir gives for its project ("" for
// none). Stale pages are still returned (the old answer and what changed is a good start) but after fresh and
// unknown ones. The stale: filter and that order apply to every match before the limit, so a stale page never
// pushes a fresh one out.
func Find(ix *index.Index, q Query, workdir func(project string) string) ([]Result, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	pq := q.PageQuery
	pq.Limit = 10000 // ponytail: every match is checked; one git diff per checked revision keeps that cheap
	rows, err := ix.FindPages(pq)
	if err != nil {
		return nil, err
	}
	checkers := map[string]*checker{} // one per project: projects can live in different working copies
	checkerOf := func(project string) *checker {
		if checkers[project] == nil {
			checkers[project] = &checker{ix: ix, workdir: workdir(project), union: map[string][]string{}}
		}
		return checkers[project]
	}
	for _, r := range rows {
		c := checkerOf(r.Project)
		if r.Checked != nil && validRev.MatchString(r.Checked.Rev) {
			for _, s := range r.Sources {
				if s.Path != "" {
					c.union[r.Checked.Rev] = append(c.union[r.Checked.Rev], s.Path)
				}
			}
		}
	}
	var out []Result
	for _, r := range rows {
		f := checkerOf(r.Project).check(r)
		if (q.Stale == "yes" && f.Status != Stale) || (q.Stale == "no" && f.Status == Stale) {
			continue
		}
		out = append(out, Result{r, f})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Freshness.Status != Stale && out[j].Freshness.Status == Stale
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ErrNoTitle is returned for a new page without a title.
var ErrNoTitle = errors.New("a new page needs a title")
