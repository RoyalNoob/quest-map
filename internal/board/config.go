package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "time/tzdata" // Windows has no system zone database; embed one so any IANA zone name works

	"github.com/pelletier/go-toml/v2"
)

// BoardConfig holds the board.toml settings this package uses. Other keys in the file (port, prices, …)
// belong to other packages and are ignored here.
type BoardConfig struct {
	// Timezone is the IANA zone name ("Asia/Tokyo"), "Local" or "UTC" used for log-line times. Front-matter
	// times are always RFC 3339 UTC. Default: UTC.
	Timezone string `toml:"timezone"`
	// Defaults are the values a new task gets for fields its creator leaves empty.
	Defaults TaskDefaults `toml:"defaults"`
	// Agents limits what agent callers (MCP) may do. People (UI, CLI, hand edits) are not limited by it.
	Agents AgentRules `toml:"agents"`
	// DefaultSuite is the suite of projects whose project.toml names none (spec §4.1). Default gamedev-unreal.
	DefaultSuite string `toml:"default_suite"`
}

// AgentRules are the board.toml [agents] settings.
type AgentRules struct {
	// MaySetDone lets agents move tasks to done. Default false: agents move finished work to review and a
	// person decides it is done.
	MaySetDone bool `toml:"may_set_done"`
}

// TaskDefaults are the status, doer and priority of a new task when its creator gives none.
type TaskDefaults struct {
	Status   Status   `toml:"status"`
	Doer     Doer     `toml:"doer"`
	Priority Priority `toml:"priority"`
}

// builtinDefaults apply when board.toml leaves a default out.
var builtinDefaults = TaskDefaults{Status: Todo, Doer: Person, Priority: Normal}

// boardTomlTemplate is written when a board has no board.toml yet, so the settings are easy to find.
const boardTomlTemplate = `# quest-map board settings.

# Time zone for log-line times: an IANA name such as "Asia/Tokyo", or "Local", or "UTC".
# Front-matter times (created, claim) are always UTC.
timezone = "UTC"

# Suite for projects whose project.toml names none: what the board calls things, disciplines, pipelines…
# Built in: gamedev-unreal. Files in suites/ extend it or add suites.
default_suite = "gamedev-unreal"

# Values a new task gets when its creator leaves them empty.
[defaults]
status = "todo"       # backlog | todo | doing | review | done
doer = "person"       # person | agent
priority = "normal"   # low | normal | high | urgent

# What agents (MCP callers) may do. People (UI, CLI, hand edits) are not limited by this.
[agents]
may_set_done = false  # true lets agents move tasks to done; false: they move finished work to review

# Web UI language when a browser has not chosen one and its own language has no catalog: en, ja, zh-TW, or a
# language added in i18n/<code>.json.
# [ui]
# language = "ja"

# Model prices for Insights, in USD per million tokens. Usage that carries its own cost uses that instead.
# Prices apply to all history when read, so a change here re-prices old usage.
# [prices."claude-opus-5-5"]
# input = 0.0
# output = 0.0
# cacheRead = 0.0
# cacheCreation = 0.0
`

// loadBoardConfig reads root/board.toml, writing the template first if the file is missing.
func loadBoardConfig(root string) (BoardConfig, *time.Location, error) {
	path := filepath.Join(root, "board.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte(boardTomlTemplate)
		if err := writeFileAtomic(path, data, 0o644); err != nil {
			return BoardConfig{}, nil, err
		}
	} else if err != nil {
		return BoardConfig{}, nil, err
	}
	var cfg BoardConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return BoardConfig{}, nil, fmt.Errorf("board.toml: %w", err)
	}
	return cfg.resolve()
}

// resolve fills in built-in defaults and checks the values.
func (c BoardConfig) resolve() (BoardConfig, *time.Location, error) {
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return c, nil, fmt.Errorf("board.toml: timezone %q: use an IANA name like \"Asia/Tokyo\", or \"Local\" or \"UTC\"", c.Timezone)
	}
	d := &c.Defaults
	if d.Status == "" {
		d.Status = builtinDefaults.Status
	}
	if d.Doer == "" {
		d.Doer = builtinDefaults.Doer
	}
	if d.Priority == "" {
		d.Priority = builtinDefaults.Priority
	}
	switch {
	case !d.Status.Valid():
		return c, nil, fmt.Errorf("board.toml: defaults.status %q is not one of backlog, todo, doing, review, done", d.Status)
	case !d.Doer.Valid():
		return c, nil, fmt.Errorf("board.toml: defaults.doer %q is not person or agent", d.Doer)
	case !d.Priority.Valid():
		return c, nil, fmt.Errorf("board.toml: defaults.priority %q is not low, normal, high or urgent", d.Priority)
	}
	return c, loc, nil
}
