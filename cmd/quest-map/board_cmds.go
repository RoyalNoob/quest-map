package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/RoyalNoob/quest-map/internal/board"
	"github.com/RoyalNoob/quest-map/internal/index"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// boardRoot picks the board folder: --board DIR, else $QUEST_MAP_BOARD, else ~/.quest-map. It returns the
// remaining arguments.
func boardRoot(args []string) (string, []string, error) {
	var rest []string
	root := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--board" && i+1 < len(args):
			root = args[i+1]
			i++
		case strings.HasPrefix(a, "--board="):
			root = strings.TrimPrefix(a, "--board=")
		default:
			rest = append(rest, a)
		}
	}
	if root == "" {
		root = os.Getenv("QUEST_MAP_BOARD")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		root = filepath.Join(home, ".quest-map")
	}
	abs, err := filepath.Abs(root)
	return abs, rest, err
}

func cmdServe(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("unexpected arguments %q (serve takes only --board DIR)", rest)
	}
	if err != nil {
		fmt.Fprintln(stderr, "quest-map serve:", err)
		return 1
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(stderr, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	}
	s, err := server.Start(root, server.Options{Logf: logf, Version: version})
	var running *server.ErrRunning
	if errors.As(err, &running) {
		fmt.Fprintln(stderr, "quest-map serve:", err)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "quest-map serve:", err)
		return 1
	}
	fmt.Fprintf(stdout, "quest-map serving %s on http://127.0.0.1:%d (Ctrl+C to stop)\n", root, s.Info().Port)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Fprintln(stderr, "stopping (press Ctrl+C again to quit at once)")
	case <-s.StopRequested():
		fmt.Fprintln(stderr, "stopping (quest-map stop)")
	}
	signal.Stop(sig) // a second Ctrl+C stops the process at once if shutting down hangs
	if err := s.Close(); err != nil {
		fmt.Fprintln(stderr, "quest-map serve: stop:", err)
		return 1
	}
	return 0
}

func cmdReindex(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("unexpected arguments %q (reindex takes only --board DIR)", rest)
	}
	if err != nil {
		fmt.Fprintln(stderr, "quest-map reindex:", err)
		return 1
	}
	if info := server.Running(root); info != nil {
		var out struct {
			Renumbered []index.Renumbered `json:"renumbered"`
		}
		if err := server.Call(info, http.MethodPost, "/api/reindex", &out); err != nil {
			fmt.Fprintln(stderr, "quest-map reindex:", err)
			return 1
		}
		printRenumbered(stdout, out.Renumbered)
		fmt.Fprintln(stdout, "reindexed by the running server")
		return 0
	}
	ix, done, err := localIndex(root)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map reindex:", err)
		return 1
	}
	defer done()
	printRenumbered(stdout, ix.Renumbered())
	problems, _ := ix.ParseProblems()
	for _, p := range problems {
		fmt.Fprintf(stdout, "cannot read %s: %s\n", p.Path, p.Err)
	}
	fmt.Fprintln(stdout, "reindexed")
	return 0
}

// localIndex is for CLI commands when no server is running (§3.4): it takes the board lock itself, opens the
// board and builds the index. Call done to release them.
func localIndex(root string) (*index.Index, func(), error) {
	lock, err := server.AcquireLock(root)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*index.Index, func(), error) {
		lock.Release()
		return nil, nil, err
	}
	b, err := board.Open(root)
	if err != nil {
		return fail(err)
	}
	_, ttl, err := server.LoadSettings(root)
	if err != nil {
		return fail(err)
	}
	ix, err := index.Open(filepath.Join(root, ".cache", "index.sqlite"), b, index.Options{ClaimTTL: ttl})
	if err != nil {
		return fail(err)
	}
	if err := ix.Rebuild(); err != nil {
		ix.Close()
		return fail(err)
	}
	return ix, func() { ix.Close(); lock.Release() }, nil
}

func printRenumbered(w io.Writer, rs []index.Renumbered) {
	for _, r := range rs {
		fmt.Fprintf(w, "renumbered duplicate %s in %s to %s\n", r.OldID, r.Project, r.NewID)
	}
}

// cmdFind lists tasks matching a filter: through the running server, else with the board lock taken here.
func cmdFind(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map find:", err)
		return 1
	}
	v := url.Values{}
	var terms []string
	for _, a := range rest {
		switch {
		case strings.HasPrefix(a, "--project="):
			v.Set("project", strings.TrimPrefix(a, "--project="))
		case a == "--archived":
			v.Set("archived", "yes")
		default:
			terms = append(terms, a)
		}
	}
	v.Set("q", strings.Join(terms, " "))
	var rows []index.Row
	if info := server.Running(root); info != nil {
		if err := server.Call(info, http.MethodGet, "/api/tasks?"+v.Encode(), &rows); err != nil {
			fmt.Fprintln(stderr, "quest-map find:", err)
			return 1
		}
	} else {
		ix, done, err := localIndex(root)
		if err == nil {
			rows, err = ix.FindString(v.Get("q"), index.FindOptions{Project: v.Get("project"), IncludeArchived: v.Get("archived") == "yes"})
			done()
		}
		if err != nil {
			fmt.Fprintln(stderr, "quest-map find:", err)
			return 1
		}
	}
	for _, r := range rows {
		line := fmt.Sprintf("%s %s (%s", r.ID, r.Status, r.Doer)
		for _, s := range []string{r.Discipline, r.Milestone} {
			if s != "" {
				line += ", " + s
			}
		}
		line += "): " + r.Title
		if r.SpentRecords > 0 {
			u := board.Usage{Tokens: board.Tokens{Total: r.SpentTokens}, Time: r.SpentTime}
			line += " ·" + u.Suffix()
		}
		if r.ParseError != "" {
			line += "  [cannot read file: " + r.ParseError + "]"
		}
		fmt.Fprintln(stdout, line)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no tasks match")
	}
	return 0
}
