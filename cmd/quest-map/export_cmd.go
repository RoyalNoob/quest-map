package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
)

// exportTask is one task in an export.
type exportTask struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Status     board.Status      `json:"status"`
	Doer       board.Doer        `json:"doer"`
	Priority   board.Priority    `json:"priority"`
	Assignees  []string          `json:"assignees,omitempty"`
	Discipline string            `json:"discipline,omitempty"`
	Milestone  string            `json:"milestone,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
	Asset      string            `json:"asset,omitempty"`
	Pipeline   *board.Pipeline   `json:"pipeline,omitempty"`
	FoundIn    string            `json:"found_in,omitempty"`
	FixedIn    string            `json:"fixed_in,omitempty"`
	BlockedBy  []string          `json:"blocked_by,omitempty"`
	URL        string            `json:"url,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	Created    time.Time         `json:"created"`
	Archived   bool              `json:"archived,omitempty"`
	Body       string            `json:"body,omitempty"`
	Log        []exportLog       `json:"log,omitempty"`
}

type exportLog struct {
	At    time.Time `json:"at"`
	Who   string    `json:"who"`
	Text  string    `json:"text"`
	Usage string    `json:"usage,omitempty"`
}

type exportProject struct {
	Slug  string       `json:"slug"`
	Key   string       `json:"key"`
	Name  string       `json:"name"`
	Tasks []exportTask `json:"tasks"`
}

// cmdExport writes the board (or one project) as one Markdown or JSON document. It only reads task files,
// which are replaced atomically, so it needs neither the server nor the board lock.
func cmdExport(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "quest-map export:", err)
		return 1
	}
	root, rest, err := boardRoot(args)
	if err != nil {
		return fail(err)
	}
	opts, flags, err := parseFlags(rest, []string{"project", "format", "out"}, []string{"archived"})
	if err != nil {
		return fail(err)
	}
	format := strings.ToLower(opts["format"])
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		return fail(fmt.Errorf("--format %q: use md or json", format))
	}
	if _, err := os.Stat(root); err != nil {
		return fail(fmt.Errorf("no board at %s", root))
	}
	b, err := board.Open(root)
	if err != nil {
		return fail(err)
	}
	ps, err := b.Projects()
	if err != nil {
		return fail(err)
	}
	if opts["project"] != "" {
		p, err := pickProject(b, opts["project"])
		if err != nil {
			return fail(err)
		}
		ps = []*board.Project{p}
	}
	var out []exportProject
	for _, p := range ps {
		tasks, errs := p.List()
		for _, e := range errs {
			fmt.Fprintln(stderr, "quest-map export: skipped:", e)
		}
		if flags["archived"] {
			arch, errs := p.ListArchived()
			for _, e := range errs {
				fmt.Fprintln(stderr, "quest-map export: skipped:", e)
			}
			tasks = append(tasks, arch...)
		}
		ep := exportProject{Slug: p.Slug(), Key: p.Key(), Name: p.Config().Name, Tasks: []exportTask{}}
		for _, t := range tasks {
			ep.Tasks = append(ep.Tasks, toExport(t))
		}
		sort.SliceStable(ep.Tasks, func(i, j int) bool {
			a, b := ep.Tasks[i], ep.Tasks[j]
			if sa, sb := statusRank(a.Status), statusRank(b.Status); sa != sb {
				return sa < sb
			}
			return idNum(a.ID) < idNum(b.ID)
		})
		out = append(out, ep)
	}

	w := stdout
	var f *os.File
	if path := opts["out"]; path != "" {
		if f, err = os.Create(path); err != nil {
			return fail(err)
		}
		w = f
	}
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		err = enc.Encode(out)
	} else {
		err = writeMarkdown(w, out, b.Location())
	}
	if f != nil {
		err = errors.Join(err, f.Close()) // a failed close can mean a truncated file
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func toExport(t *board.Task) exportTask {
	e := exportTask{ID: t.ID, Title: t.Title, Status: t.Status, Doer: t.Doer, Priority: t.Priority, Assignees: t.Assignees,
		Discipline: t.Discipline, Milestone: t.Milestone, Labels: t.Labels, Asset: t.Asset, Pipeline: t.Pipeline,
		FoundIn: t.FoundIn, FixedIn: t.FixedIn, BlockedBy: t.BlockedBy, URL: t.URL, Fields: t.Custom,
		Created: t.Created, Archived: t.Archived, Body: t.Body}
	for _, l := range t.Log {
		e.Log = append(e.Log, exportLog{At: l.Time, Who: l.Who, Text: l.Text, Usage: l.Usage})
	}
	return e
}

func statusRank(s board.Status) int {
	for i, x := range board.Statuses {
		if x == s {
			return i
		}
	}
	return len(board.Statuses)
}

func idNum(id string) int {
	n := 0
	if i := strings.LastIndex(id, "-"); i >= 0 {
		fmt.Sscan(id[i+1:], &n)
	}
	return n
}

// writeMarkdown renders the export as one Markdown document: projects, then tasks grouped by status.
func writeMarkdown(w io.Writer, ps []exportProject, loc *time.Location) error {
	var sb strings.Builder
	sb.WriteString("# quest-map board\n")
	for _, p := range ps {
		fmt.Fprintf(&sb, "\n## %s (%s)\n", p.Name, p.Key)
		if len(p.Tasks) == 0 {
			sb.WriteString("\nNo tasks.\n")
		}
		status := board.Status("-")
		for _, t := range p.Tasks {
			if t.Status != status {
				status = t.Status
				fmt.Fprintf(&sb, "\n### %s\n", status)
			}
			fmt.Fprintf(&sb, "\n#### %s – %s\n\n", t.ID, t.Title)
			facts := []string{string(t.Doer), string(t.Priority)}
			add := func(name, v string) {
				if v != "" {
					facts = append(facts, name+" "+v)
				}
			}
			add("discipline", t.Discipline)
			add("milestone", t.Milestone)
			add("labels", strings.Join(t.Labels, ", "))
			add("assignees", strings.Join(t.Assignees, ", "))
			add("asset", t.Asset)
			if t.Pipeline != nil {
				add("pipeline", t.Pipeline.Name+" at "+t.Pipeline.At)
			}
			add("found in", t.FoundIn)
			add("fixed in", t.FixedIn)
			add("waits on", strings.Join(t.BlockedBy, ", "))
			keys := make([]string, 0, len(t.Fields))
			for k := range t.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				add(k, t.Fields[k])
			}
			add("url", t.URL)
			if t.Archived {
				facts = append(facts, "archived")
			}
			sb.WriteString(strings.Join(facts, " · ") + "\n")
			if t.Body != "" {
				sb.WriteString("\n" + t.Body + "\n")
			}
			if len(t.Log) > 0 {
				sb.WriteString("\nLog:\n")
				for _, l := range t.Log {
					text := strings.ReplaceAll(l.Text, "\n", "\n  ")
					if l.Usage != "" {
						text += " (" + l.Usage + ")"
					}
					fmt.Fprintf(&sb, "- %s %s: %s\n", l.At.In(loc).Format("2006-01-02 15:04"), l.Who, text)
				}
			}
		}
	}
	_, err := io.WriteString(w, sb.String())
	return err
}

// parseFlags reads --name value / --name=value options and bare --switch flags.
func parseFlags(args, valued, switches []string) (map[string]string, map[string]bool, error) {
	opts, flags := map[string]string{}, map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch {
		case !strings.HasPrefix(a, "--"):
			return nil, nil, fmt.Errorf("unexpected argument %q", a)
		case slices.Contains(switches, name) && !hasVal:
			flags[name] = true
		case !slices.Contains(valued, name):
			return nil, nil, fmt.Errorf("unknown option %q", a)
		case !hasVal && i+1 < len(args):
			i++
			opts[name] = args[i]
		case !hasVal:
			return nil, nil, fmt.Errorf("--%s needs a value", name)
		default:
			opts[name] = val
		}
	}
	return opts, flags, nil
}
