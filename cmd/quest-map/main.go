// Command quest-map is a local, agent-first task board for game teams.
package main

import (
	"fmt"
	"io"
	"os"
)

// Set at release time with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `quest-map – agent-first task board for game teams

Usage:
  quest-map serve              start web UI + HTTP MCP (holds the board lock)
  quest-map stop               stop the running server (it finishes its writes first)
  quest-map restart            stop the server and start it again in the background (after an update)
  quest-map mcp                stdio MCP proxy (what agents launch)
  quest-map install <client>   register the MCP server and usage hook with an agent client (claude-code; --board DIR)
  quest-map usage-hook         client hook: report the turn's measured usage (installed by install)
  quest-map feedback <text>    file a feedback report, e.g. a playtest note (--project, --release/--build,
                               --screenshot FILE, --reporter NAME)
  quest-map reindex            rebuild the SQLite cache
  quest-map find <filter>      list tasks matching a filter (--project=SLUG, --archived)
  quest-map stats              Insights: how well the agent setup works (--project, --since 30d|2026-11-01,
                               --by model|client|discipline|label|milestone, --task ID, --csv)
  quest-map export             the whole board as one Markdown document (--format json, --project, --out FILE,
                               --archived)
  quest-map autostart on|off|status
                               start the server at login for the current user
  quest-map update             install the latest release over this binary and restart the server (--check)
  quest-map version            print the version

Board commands take --board DIR (default: $QUEST_MAP_BOARD, else ~/.quest-map).
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "quest-map %s (%s, %s)\n", currentVersion(), commit, date)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "stop":
		return cmdStop(args[1:], stdout, stderr)
	case "restart":
		return cmdRestart(args[1:], stdout, stderr)
	case "reindex":
		return cmdReindex(args[1:], stdout, stderr)
	case "find":
		return cmdFind(args[1:], stdout, stderr)
	case "mcp":
		return cmdMCP(args[1:], stderr)
	case "install":
		return cmdInstall(args[1:], stdout, stderr)
	case "usage-hook":
		return cmdUsageHook(args[1:], os.Stdin, stderr)
	case "feedback":
		return cmdFeedback(args[1:], stdout, stderr)
	case "stats":
		return cmdStats(args[1:], stdout, stderr)
	case "export":
		return cmdExport(args[1:], stdout, stderr)
	case "autostart":
		return cmdAutostart(args[1:], stdout, stderr)
	case "update":
		return cmdUpdate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "quest-map: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
