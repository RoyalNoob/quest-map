package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/insights"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// cmdStats prints the Insights metrics (§8.4): through the running server, else with the board lock taken here.
func cmdStats(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "quest-map stats:", err)
		return 1
	}
	root, rest, err := boardRoot(args)
	if err != nil {
		return fail(err)
	}
	opts, flags, err := parseFlags(rest, []string{"project", "since", "by", "task"}, []string{"csv"})
	if err != nil {
		return fail(fmt.Errorf("%w; use --project, --since, --by, --task, --csv", err))
	}
	asCSV := flags["csv"]
	q := insights.Query{Since: opts["since"], By: opts["by"], Task: opts["task"]}
	project := opts["project"]
	if project == "" && q.Task != "" {
		project, _, _ = strings.Cut(q.Task, "-")
	}

	var rep insights.Report
	if info := server.Running(root); info != nil {
		v := url.Values{"project": {project}, "since": {q.Since}, "by": {q.By}, "task": {q.Task}}
		if err := server.Call(info, http.MethodGet, "/api/insights?"+v.Encode(), &rep); err != nil {
			return fail(err)
		}
	} else {
		lock, err := server.AcquireLock(root)
		if err != nil {
			return fail(err)
		}
		defer lock.Release()
		b, err := board.Open(root)
		if err != nil {
			return fail(err)
		}
		_, ttl, err := server.LoadSettings(root)
		if err != nil {
			return fail(err)
		}
		p, err := pickProject(b, project)
		if err != nil {
			return fail(err)
		}
		r, err := insights.Build(b, p, q, ttl)
		if err != nil {
			return fail(err)
		}
		rep = *r
	}
	if asCSV {
		err = rep.WriteCSV(stdout)
	} else {
		err = rep.WriteText(stdout)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// pickProject finds a project by folder name or key; with no name, the board's only project.
func pickProject(b *board.Board, name string) (*board.Project, error) {
	ps, err := b.Projects()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range ps {
		if name != "" && (strings.EqualFold(p.Slug(), name) || strings.EqualFold(p.Key(), name)) {
			return p, nil
		}
		names = append(names, p.Slug())
	}
	if name == "" && len(ps) == 1 {
		return ps[0], nil
	}
	if name == "" {
		return nil, fmt.Errorf("pass --project (one of %s)", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no project %q (have %s)", name, strings.Join(names, ", "))
}
