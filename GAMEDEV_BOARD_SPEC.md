# quest-map – Spec & Plan

Project name: **`quest-map`**.

A local, single-binary, agent-first task board. Agents drive it over MCP; humans use a small web UI. It
knows five things project teams have that generic boards don't – milestones and releases, disciplines,
items with step pipelines, version-control links, and feedback intake (§4) – and a **suite** tailors them to a
kind of project (§4.1). The first suite is game development with Unreal Engine: builds, art/code/audio
disciplines, asset pipelines, binary-heavy version control (git and Lore; Perforce later) and playtest feedback.

> quest-map is inspired by Kanzo; credit it in the README.

---

## 1. Goals and non-goals

**Goals**
- One binary, no runtime dependencies, Windows first, Linux too. macOS is not a target.
- Agents can find, claim, update and finish work with a handful of MCP tools and terse replies.
- Plain-text data: every task is a Markdown file that humans, agents and VCS diffs can read.
- Five project concepts built in (§4), tailored per project by a suite (§4.1); game development (Unreal) is
  the first suite. Everything else stays out.
- Insight into the agent setup: measured agent cost linked to task outcomes, across agent vendors, compared
  with the person-done baseline on the same board (§3.6, §8.4).
- After v1: a knowledge base agents check before exploring code again (§4.5), and sandboxed UI plugins for
  views the core does not need – reports and a vision board first (§8.5).

**Non-goals (v1)**
- Multi-tenant hosted service, accounts, permissions.
- Several people editing one board at the same time. v1 is one person's board (§3.5 covers syncing it).
- Media review (image/video annotation) – link to Kitsu or similar instead.
- Gantt charts, timesheets, budgets and budget caps. (Usage records in §3.6 are for insight, not billing.)
- A full analytics product: the Insights view stays small; data is exportable for Grafana and similar.
- Engine plugins – agents in the editor reach the board through MCP.

---

## 2. Concepts

| Concept | Meaning |
|---|---|
| **Board** | A data folder on disk holding one or more projects. Default: `~/.quest-map/`. |
| **Project** | A folder in the board, e.g. `~/.quest-map/projects/rpg/`. Has a key (`RPG`), name, linked repos, config. |
| **Task** | One Markdown file `RPG-12.md` with YAML front matter. ID = project key + number. |
| **Status** | Fixed set: `backlog`, `todo`, `doing`, `review`, `done`. |
| **Doer** | `person` or `agent` – who is expected to do it. Agents never pick up `person` tasks. |
| **Claim** | Which agent session is working a `doing` task, and since when. |
| **Milestone** | Named target with optional date, e.g. `vertical-slice`, `alpha`. Tasks point to one. |
| **Release** | A recorded deliverable (id, target, VCS revision, date, notes). Tasks can say "found in" / "fixed in". The game-dev suite calls it a *build* and the target a *platform*. |
| **Discipline** | Which team or area does the work, from a list the suite sets (game dev: `code`, `art`, `anim`, `audio`, `qa`, …). |
| **Item task** | A task about one item (`asset` field) that follows a `pipeline` of steps. Game dev: an asset, e.g. `blockout → model → texture → import → in-engine`. |
| **Suite** | A named package of configuration that tailors the five concepts to a kind of project: wording, lists, pipelines, templates, views, intake defaults, adapters (§4.1). Chosen per project. |
| **Adapter** | Built-in code a suite switches on by name for what configuration cannot do, e.g. mapping Unreal asset paths to files, or reading Lore locks (§4.2). |
| **Activity** | Append-only per-project record of usage (tokens, time, model, client) and status changes (§3.6). |
| **Spent** | A task's usage totals, derived from activity. Measured where the client allows it, else self-reported. |
| **Repo link** | A project ↔ VCS remote mapping used to pick the project from the agent's working folder. |
| **Knowledge page** | A Markdown file in a project's `kb/` folder: an answer worth keeping, with the sources it was checked against (§4.5). |
| **UI plugin** | A JavaScript module that adds a view to the web UI, runs sandboxed and talks to the board only through the plugin API (§8.5). |

---

## 3. Data model

### 3.1 Layout

```
~/.quest-map/
  board.toml                 # board settings (port, timezone, task defaults, agent rules, default suite, claim_ttl, intake, prices)
  suites/                    # optional: suite files that extend or replace built-in suites (§4.1)
  projects/
    rpg/
      project.toml           # key, name, suite, repos, milestones, next id
      tasks/
        RPG-1.md
        RPG-2.md
      archive/
        RPG-0.md             # archived tasks, same format
      releases.toml          # recorded releases (game-dev suite: builds)
      activity.jsonl         # append-only usage + status-change records (§3.6)
      attachments/RPG-2/…    # images/logs pasted into tasks
      kb/                    # knowledge pages, one Markdown file each (§4.5)
      plugins/<name>/…       # data a UI plugin stores for this project (§8.5)
  .run/server.json           # running server: pid, port, token (§3.4)
  .run/sync.json             # this machine's auto-sync choice (§3.5)
  .run/usage-cursors.json    # per client session: last transcript entry counted (§3.6)
  .run/board.lock            # OS file lock held by the one running server
  .run/origin                # random id of this copy of the board, stamped on activity records (§3.5)
  .cache/index.sqlite        # derived; safe to delete
  .trash/<slug>-<time>/      # deleted projects, never synced (§8.3)
  .gitattributes, .gitignore # for boards synced with git (§3.5)
```

`.run/` is machine-local and private: when the server takes the lock it makes `.run/` usable by the current
user only (Windows: a protected access list with only that user; Linux: mode 0700), wherever the board lives.

Files are the source of truth. SQLite is a derived index for search and listing, rebuilt from files on start
and kept fresh by a file watcher.

The board lives in `~/.quest-map/`. Board commands can point elsewhere with `--board DIR` or
`$QUEST_MAP_BOARD`; that is meant for tests, demos and CI, not as a second place to keep a team's board. A
board must not be placed inside a Lore workspace (or, later, a Perforce
client): those files are read-only until checked out, which breaks atomic writes. `quest-map serve` refuses to
start if the board path is under a detected Lore workspace.

### 3.2 Task file

```markdown
---
id: RPG-12
title: Player falls through bridge collision
status: doing
doer: agent
assignees: [Mika]        # people it is assigned to (names); [] when nobody
claim: {by: "claude-code:7f3a", at: 2026-11-17T09:15:00Z}   # null when not claimed
priority: high            # low | normal | high | urgent
discipline: code
milestone: vertical-slice
labels: [physics, bug]
asset: null
pipeline: null            # or {name: prop, at: rig}
found_in: b2026-11-14.3   # release id (game dev: build)
fixed_in: null
blocked_by: [RPG-9]
moved_from: null          # old id if moved from another project
url: null                 # PR or Lore revision
created: 2026-11-17T09:12:00Z
---

Repro: load `L_Bridge`, sprint across the middle span.

<!-- quest-map:log -->
## Log
- 2026-11-17 10:40 agent (+42k tok, +25m): Collision mesh missing on SM_Bridge_Mid; regenerating.
```

Rules:
- A single `assignee: name` (written by an early build) is read as `assignees: [name]` and rewritten that
  way on the next write.
- Front matter keys are fixed. Unknown keys are preserved (round-trip through YAML nodes, keeping key order
  and comments) but not used.
- There is no `updated` key: it would change on every write and cause noisy diffs and merge conflicts. The
  index derives "last updated" from the newest log entry, else the file's modification time.
- The log starts at the `<!-- quest-map:log -->` marker. Everything above it is the body; a human-written
  `## Log` heading in the body has no effect. If the marker is missing, quest-map adds it (and the heading) at
  the end of the file on the next log write. Log lines are `- <time> <who>: <text>`; humans may edit or
  delete them, and quest-map only ever appends. Log times are `YYYY-MM-DD HH:MM` in the board's `timezone`
  (`board.toml`: an IANA name, `Local` or `UTC`; default `UTC`). Front-matter times stay RFC 3339 UTC.
- A new task gets `[defaults]` from `board.toml` (`status`, `doer`, `priority`; built in: `todo`, `person`,
  `normal`) for any of those fields its creator leaves empty.
- Usage is not stored in the task file (it would make every write a noisy diff). When a write carries
  usage, its log line gets a readable suffix `(+42k tok, +25m)`; the source of truth is `activity.jsonl`
  (§3.6), so editing or deleting log lines does not change totals.
- Writes are atomic: write a temp file in the same folder, then rename. On Windows, retry the rename with
  backoff (up to ~2 s) when it fails because another process (editor, antivirus, indexer) has the file open.
- All writes go through the one running server (§3.4), which serializes them per project.

### 3.3 Index (SQLite, derived)

Tables: `tasks` (front matter columns + body text + derived `updated` and spent totals), `tasks_fts` (FTS5
over title/body/log), `repos` (remote key → project), `releases`, `usage` and `transitions` (from
`activity.jsonl`). Only the server writes it. Rebuilt if missing or
its schema version changes.

File watcher rules:
- Debounce events per file (~200 ms) so editors that write in several steps are read once.
- Ignore echoes of the server's own writes: after each write, remember the file's size and content hash, and
  skip a watcher event whose file still matches.
- A hand-edited file that fails to parse keeps its last good index row, is flagged in the UI and in
  `task_show`, and is never overwritten by quest-map until it parses again.

### 3.4 One writer: the server

Every agent starts its own stdio MCP process, so an in-process mutex is not enough. Instead:

- `quest-map serve` takes an exclusive OS lock on `.run/board.lock` (fails if another server holds it), then
  writes `.run/server.json` with its pid, port and a random token (file readable by the current user only).
- `quest-map mcp` (stdio) is a thin proxy: it reads `.run/server.json`, starts `quest-map serve` in the background
  if no server is running, and forwards MCP calls to the server's `/mcp` endpoint with the token. It also
  passes its working folder and a session id so the server can pick the project and record claims.
- Other CLI commands (`reindex`, `export`, `feedback`) also go through the server; if none is running, they
  take the board lock themselves for the duration of the command.
- The server serializes writes with a per-project mutex. ID allocation (`next id` in `project.toml`) and
  task claiming (§6) happen under that mutex.

### 3.5 Syncing a board between machines

A board folder may be put in a git repository for backup or to share with a few people, taking turns. Two
copies can then create the same ID offline. On reindex, if two files claim the same ID, the one with the
later `created` keeps the ID; the other is renumbered to the next free ID, its `moved_from` is set to the old
ID, `blocked_by` references in other tasks are left alone, and a log line on both tasks records the change.
The UI shows a warning listing renumbered tasks. The same check runs when the watcher sees a new or changed
file, not only on reindex. The renumbered file is rewritten in place with its new id first and then renamed,
so a crash never leaves the task twice; a task file whose name is not its id (a merge copy such as
`RPG-1 (laptop).md`, or a rename that did not finish) is renamed to its id when that name is free.

Activity after a renumber: both copies' records sit under the old id, so every record carries the `origin` of
the board copy that wrote it (`.run/origin`, never synced). The renumber's `edit` record names the origin of
the renumbered copy (`take_origin`, found from the record that created that copy); records of the old id from
that origin written before the renumber belong to the new id, the rest stay. When the two copies cannot be
told apart (same machine, or records from before origins existed), everything stays with the kept task and
both log lines say so.

`activity.jsonl` is append-only, so quest-map keeps `activity.jsonl merge=union` in the board root's
`.gitattributes` (adding the line to an existing file); record `id`s (§3.6) make duplicate lines after a
merge harmless. It also keeps `.run/`, `.cache/` and `.trash/` in `.gitignore`.

Same task changed on both copies: git merges the file as text, so changes to different fields, the body and
new log lines usually merge cleanly. Changes to the same lines – the same field, or log lines added at the
same place – leave a normal git conflict, and quest-map does not resolve it. A file with conflict markers
fails to parse, so it is handled as a broken hand edit (§3.3): it keeps its last good index row, is flagged
in the UI and in `task_show`, and is never overwritten until the person resolves it. When resolving, keep
both sides' log lines; dropping one changes only the log, not spent totals, which come from `activity.jsonl`.
The resolved file arrives as a hand edit and is recorded as one (§3.6).

Sync from the UI: when the board folder is the top folder of a git work tree whose branch has an upstream (a
board inside a bigger repository is not synced: a commit would take what the person staged elsewhere and the
push would carry their other commits), the header shows
a Sync button (`⇅ Sync ↑2 ↓1`: commits to push, commits to pull as of the last fetch). A sync commits
everything under the board root (`quest-map sync: <host> <UTC time>`), fetches, merges the upstream
(`git merge --no-edit @{u}`, so the union driver for `activity.jsonl` applies), and pushes. Commit and merge
run with board writes held back (one write gate around every file write and activity append), so git never
commits or merges over a write; fetch and push run without it. Git runs with the user's own credential helper
(the system git config is kept for sync: Git for Windows sets the helper there) but never asks: no terminal
prompt, no credential manager window, no ssh password dialog – auto-sync runs on a timer in the background, so
missing credentials are an error that says to push once from a terminal. Results: `ok`, `conflict`, `error`, `offline` (`GET /api/sync`, `POST /api/sync` runs one;
the last sync's conflict or error is also in `GET /api/warnings` as `sync`). A conflict is left as git left it
(above); every later sync only looks until no conflicted file holds markers, then commits the merge – the
person need not `git add`. A conflicted binary file (a plugin image) has no markers, so it stays a conflict
until someone keeps one side and runs `git add`. Pulled changes are not hand edits: they arrive with the other copy's activity
records, which the hand-edit wait (§3.6) matches.
Auto-sync (Settings, per machine, `.run/sync.json`, `PUT /api/sync {auto, interval_minutes}`; default off,
10 minutes) syncs on start, every interval, and a minute after quest-map last wrote the board. A failed round
is tried again after the interval.

### 3.6 Activity: usage and status changes

Each project has `activity.jsonl`, one JSON object per line, append-only, written only by the server. It
is the source for spent totals (§6), agent updates (§6) and Insights (§8.4). Record kinds: `usage`,
`status`, `note` (a log line was added; holds task, actor, session, and the first 200 characters, ending in "… cannot continue" when the cut would drop that phrase, §8.4) and `edit`
(front-matter fields or body changed; holds task, actor, session and the changed field names, not values).
A move writes an `edit` record on both sides – `moved` with `to: <new id>` in the old project, `moved_from` with
`from: <old id>` in the new one – so a task's spent total follows it across projects. A renumber (§3.5)
writes `fields: ["id","moved_from"]` with `from` and `take_origin`:

```json
{"id":"01JD…","at":"2026-11-17T10:40:00Z","kind":"usage","task":"RPG-12","session":"claude-code:7f3a",
 "client":"claude-code/2.4.1","model":"claude-opus-5-5","source":"measured",
 "tokens":{"input":1200,"output":8100,"cacheRead":31000,"cacheCreation":1700},"time_s":1500,"cost_usd":0.41}
{"id":"01JD…","at":"2026-11-17T11:05:00Z","kind":"status","task":"RPG-12","from":"doing","to":"review",
 "actor":"agent","session":"claude-code:7f3a","client":"claude-code/2.4.1","model":"claude-opus-5-5"}
```

Fields:
- `id` – ULID, so records sort by time and merge duplicates are dropped.
- `client` – name/version from the MCP `initialize` `clientInfo`; recorded automatically.
- `model` – from measured data, else the `model` argument the agent passes (§6), else `null`.
- `tokens` – split by type, using the same names as Claude Code's OpenTelemetry metrics (`input`, `output`,
  `cacheRead`, `cacheCreation`); any subset may be present. Self-reports may give only a total as `{"total":…}`.
- `time_s` – active working time in seconds (not wall-clock since claim).
- `cost_usd` – stored only when the measured source supplies a cost; otherwise cost is computed when read,
  from `[prices.<model>]` in `board.toml` (per-million rates per token type), so price changes apply to history.
- `source` – `measured` (from the client's own logs), `self` (agent's estimate) or `human` (correction).
- `task` – may be `null`: usage in a session with no claimed task still counts as project overhead.
- `origin` – the board copy that wrote the record (§3.5); set automatically.
- `actor` – `agent` or `person`; `status`, `note` and `edit` records are written for every change, whatever its
  origin (MCP, UI, CLI, hand edits seen by the watcher – those with `actor: person`).
- Hand edits are recorded up to 2 s after the watcher sees them, so a git pull, checkout or branch switch can
  be told apart: the task file and the other copy's activity lines arrive together, in either order. If
  status, note or edit records for that task arrived with the change from another `origin`, or records
  vanished from the activity file (history rewound), nothing is recorded; records that arrived from this copy
  cancel the parts they explain; the rest is the person's edit. A rebuild after lost events records nothing
  for the burst. A file fixed after being broken is compared with its last good version.

**Measured capture (preferred).** Clients that keep local session logs are read directly instead of trusting
estimates (the same idea as ccusage):
- `quest-map usage-hook` is installed by `quest-map install claude-code` as a Claude Code `Stop` hook. It gets
  the session id and transcript path on stdin, reads the transcript entries added since the last cursor
  (`.run/usage-cursors.json`), sums per-message token usage and model, and measures active time as the sum of
  turn durations (prompt to last reply). It posts one `usage` record per model to the server.
- Subagent turns are in separate files next to the transcript (`<session>/subagents/agent-*.jsonl`); they
  are read too, with one cursor per file.
- Attribution by transcript link: every MCP tool reply ends with a line `quest-map session: <agent session
  id>`. That reply is stored in the client's transcript as a tool result, so the server finds the marker in
  the new transcript lines and links the hook's transcript to the MCP session that holds the claims (newest
  marker wins; after `/clear` the next reply links the new transcript again). Shortcuts when no marker is in
  the new lines: the client session id the MCP proxy forwarded (`CLAUDE_CODE_SESSION_ID`, when the client
  passes it), else the link stored for that client session. Measured records use the linked agent session's
  id, so that session's self-reports are ignored (above). A session never linked to quest-map records
  nothing, and its first ingest counts only from the turn it first talked to the board, not older history.
- Task: the one task the linked session held a claim on during the turn (still held, or ended by its own
  write since the last ingest, so the `review` hand-off turn counts); several (subagents sharing the session)
  or none: `task: null` (project overhead of that project, or of the working folder's project). Tasks claimed
  by other sessions are never used.
- The hook posts to the running server (`POST /api/usage-hook`, 10 s timeout), which reads the transcript and
  keeps the cursors (only after the usage is recorded); with no server running the hook does nothing and the
  turn is counted on a later call. `transcript_path` must be an absolute path to a regular `.jsonl` file; a
  replaced file is detected by a hash of its first bytes. Zero-token lines (`<synthetic>`) are skipped.
- Transcript format (checked against Claude Code's JSONL): `assistant` lines carry `message.id`, `message.model`
  and `message.usage` (input, output, cache read, cache creation); one message appears on several lines with the
  same usage, so each message id counts once. Active time runs from each prompt (a `user` line that is not a tool
  result) to the last `assistant` line after it.
- Readers for other clients' local logs (Codex, Gemini CLI, …) follow the same pattern and are added one at a
  time; each must be checked against the client's real log format before it ships.
- When a session has measured capture, self-reported tokens and time from that session are ignored (no double
  counting); the agent only needs to pass `model` if anything. "Has measured capture" is decided per
  project: the session has a `measured` record in the same `activity.jsonl` (on any task, or none). The task
  drawer, the index and filters all use this one rule.

**Corrections.** A person can add a `human` record (positive or negative) from the task drawer; records are
never edited or deleted.

---

## 4. Project concepts (the only five)

The core knows five concepts. Their names in the UI and in agent replies, their lists and their defaults
come from the project's suite (§4.1); the task-file keys never change.

1. **Milestones & releases** – milestones with dates; `release_record` stores id/target/revision/date; tasks
   carry `found_in` / `fixed_in`; filter "open bugs found in the latest release". Release ids are free strings,
   unique per project; a suite suggests a format (game dev: `b<YYYY-MM-DD>.<n>`).
2. **Disciplines** – every task has one, from the suite's list; board columns can be grouped by discipline;
   `task_claim` can be limited to a discipline so an "audio agent" only gets audio work.
3. **Item pipelines** – a task about one item (`asset`) follows one of the suite's named pipelines and tracks
   which step it is at; advancing a step logs it; the board shows a compact step bar.
4. **VCS links** – detect the project from the working folder (git built in; Lore through an adapter); store
   a task's `url` as a PR or revision; show read-only lock state for files named in the task through lock
   adapters (git LFS, Lore – §5). Perforce is deferred (Appendix A).
5. **Feedback intake** – a `feedback_report` MCP tool, a `quest-map feedback` CLI command, and an opt-in intake
   listener (§8.2) that turn a report (text, optional screenshot, release id) into a `backlog` task with the
   suite's intake defaults (game dev: a playtest note, `discipline: qa`, `found_in` set). The report's first
   line is the title and the rest the body. A release id the project has not recorded is kept as given, but the
   reply (and the listener's `warning`) and the task body say so, since it is often a typo.

### 4.1 Suites

A suite is configuration only. It sets:
- **Wording**: what the UI and agent replies call a release, its target, an item, a discipline and a feedback
  report (game dev: build, platform, asset, discipline, playtest note). Tool names stay generic; their
  descriptions and replies use the project's wording.
- **Lists**: disciplines, pipelines and their steps, release targets offered in the UI, release-id format.
- **Defaults**: task defaults and intake defaults (status, discipline, labels).
- **Templates and views** (UX pass): task templates (e.g. a bug report with repro steps) and saved views
  (filter, lanes, sort, card fields, board or table layout). A view is a `[[views]]` table: `name`, `filter`
  (the filter language), `lanes` (`""`, `discipline`, `milestone`), `sort` (`""` = id, `priority`, `updated`,
  `created`, `title`), `fields` (card fields from `priority claim steps doer assignees discipline milestone labels
  spent found` and the suite's custom field keys; empty = the default set) and `layout` (`board`, `table`). A suite file that sets `views` replaces the
  list. Bad values stop the suite file from loading, like any other suite error. In the web UI, people can also
  save their own views; those are kept per browser.
  A template is a `[[templates]]` table: `name`, `title` (a title start for the UI, e.g. `Bug: `), `body`,
  `status` (`backlog`, `todo`), `doer`, `priority`, `discipline`, `labels`, `pipeline` (one of the suite's).
  `task_new` takes `template`: the template fills the fields the call leaves empty and adds its labels; an
  unknown name is an error that lists the templates. The web UI's New task dialog fills the form from the
  template, and the person edits it before creating the task. A suite file that sets `templates` replaces the list.
- **Custom fields** (UX pass): extra front-matter keys the UI shows and the filter understands, e.g.
  `platform` or `ue_map` (unknown keys already round-trip, §3.2). A field is a `[[fields]]` table: `key`
  (lowercase letters and `_`, not a built-in key or filter key), `label`, `type` (`text`, or `choice` with
  `values`) and `help`. Values are one line of text, written as plain front-matter keys after the built-in ones;
  an empty value removes the key. `task_new`/`task_edit` and the REST API take `fields` (keys the suite does not
  declare are refused, except an empty value, which clears a key left from a field the suite has dropped; a
  key holding a list or map in the file is replaced when set; a choice value not in the list gets a note), `task_show` lists them, and the filter takes
  `<key>:<value>` (any case). In the UI they are in the drawer and New task dialog, can be card fields (and so
  table columns, edited in place), and saved views may name them.
- **Field rules** (`[show]`): when the UI shows a task field. Each key is a field that can be hidden (`asset`,
  `pipeline`, `found_in`, `fixed_in`, `milestone`, `url`, `blocked_by`, or a custom field key) and holds a list
  of terms, any of which shows it: one filter term each, with `label:`, `discipline:`, `doer:`, `status:`,
  `priority:` or a custom field key (`-` negates); other terms are a suite error. A field not listed always
  shows, an empty list shows it only when set, and a field with a value always shows, so hiding never hides
  data. The drawer and New task dialog check the rules as the form changes (a template's discipline and labels
  count) and put hidden fields under "More fields" (kept per browser). Display only: MCP and the REST API take
  every field. A suite file that sets `[show]` replaces the whole table.
- **Adapters** to switch on (§4.2) and, from the UX pass, **filter aliases** (e.g. `platform:` for a release's target).
- **Labels per language** (`[lang.<code>]`, §4.4) for its wording, lists, templates, views and fields.

Rules:
- Built-in suites are embedded in the binary. A file in the board's `suites/` folder with the same name
  extends a built-in suite (keys it sets win); a new name defines a new suite. Suite files are TOML.
- The web UI edits suite files (Milestones view → Edit suite): the board's `suites/<name>.toml` as TOML text,
  with the built-in suite shown for reference. A file is checked (merged and validated as on load) before it
  is written, so the editor never leaves a broken file; text is saved as typed, comments included. A new name
  makes a new suite; removing the file resets a built-in suite (`GET`/`PUT`/`DELETE /api/suites/{name}/file`).
  `GET` gives the file's `version`; a `PUT` with another one (the file changed since it was opened: another
  tab, "Save to suite", a git pull, a hand edit) is a 409 and writes nothing.
  "Save to suite" in the View dialog and "Save as template" in the New task dialog add a `[[views]]` or
  `[[templates]]` block to the end of that file (`POST /api/suites/{name}/views`, `…/templates`); a file that
  does not set the list yet first gets the list it has now, since setting a list replaces it – so views or
  templates a later quest-map adds to that built-in suite no longer reach this board. A name already in the
  list is refused; changing or removing one is done in the editor. A list written inline (`views = [...]`)
  must be rewritten as blocks before the UI can add to it.
- `project.toml` names its suite (`suite = "gamedev-unreal"`); a project without one uses `board.toml`'s
  `default_suite` (default `gamedev-unreal`). One board can hold projects of different kinds.
- Changing a project's suite changes wording, lists and adapters only; task files are not rewritten. Values a
  task already has stay valid even if the new suite's lists do not include them (shown as unknown).
- A second built-in suite is only added for a real project that needs it; the M5 tests use a small test suite
  file to prove the core has no game-dev assumptions left.
- A suite file that does not load never stops work: the suites as they last loaded stay in use (the built-in
  ones if none ever did), a project naming an unknown suite gets the default, and replies, the project's
  `suite_error` and `GET /api/warnings` (`suites`) say what is wrong and what is in use.

### 4.2 Adapters

Adapters are Go code shipped in the binary, selected by name from a suite; there is no code loading.
- **Asset resolver** – maps an `asset` value to files and back. `unreal`: `/Game/…` object paths and asset
  names to `Content/…` `.uasset`/`.umap` files (§5). `path` (default): the value is a repo-relative path.
  `task_show` lists the files it maps to in the caller's git working copy (`files`); lock state (M6) uses the
  same mapping.
- **Lock providers** – read-only lock state for a task's files: `git-lfs`, `lore` (§5).
- **VCS detectors** – `git` (always on), `lore`.
- New adapters are added to the core as needed; a suite only names them.

### 4.3 The game-dev suite (`gamedev-unreal`)

The first suite and the default. Everything stays editable through a suite file.
- Wording: release → **build**, target → **platform**, item → **asset**, feedback report → **playtest note**.
- Disciplines: `code` (C++), `blueprint`, `design`, `level`, `art`, `anim`, `vfx`, `audio`, `ui`, `qa`.
- Pipelines:
  - `prop`: blockout → model → texture → import → material → in-engine
  - `character`: concept → model → texture → rig → anim → import → in-engine
  - `level`: blockout → layout → art-pass → lighting → polish
  - `vfx`: concept → niagara → in-engine
- Adapters: asset resolver `unreal`; lock providers `git-lfs`, `lore`; VCS detector `lore`.
- `asset` accepts an asset name (`SM_Bridge_Mid`) or object path (`/Game/Props/SM_Bridge_Mid`); the
  `asset:` filter matches either. Examples use Unreal naming prefixes (`SM_`, `SK_`, `T_`, `M_`, `BP_`).
- Platforms offered in the UI use Unreal's names (`Win64`, `Linux`, `Android`, `IOS`), plus whatever console
  names the team adds. Build ids: `b<YYYY-MM-DD>.<n>`.
- Field rules: found/fixed in build show for `bug`, `crash` and `playtest` labels and `qa`; asset and pipeline
  for `art`, `anim`, `vfx`, `level` and the `asset` label.
- Intake: playtest note → `backlog`, `discipline: qa`, label `playtest`, `found_in` = the reported build.
- Languages: English, and Japanese labels in `[lang.ja]` (§4.4).

### 4.4 Localization

Localization is customization with the same tools as suites: built-in text in the binary, extended by files on
the board, chosen per person.

What is translated:
- **UI text** – every word of the web UI (buttons, headings, menus, pickers, empty states, errors, Settings)
  comes from a message catalog, never from a string in `app.js` or `index.html`.
- **Suite labels** – wording (release, target, item, discipline, feedback report), discipline names, pipeline
  steps, status names, and template, view and field labels.

What is not translated:
- **Data** – task titles, bodies, notes and log lines stay as people wrote them.
- **Keys** – front-matter keys and values, filter keys and values, ids, tool names. A value is the same key in
  every language and only its label changes, so one board synced between a Japanese and an English machine
  stays one board with identical files.
- **Text quest-map writes into files** (renumber and auto-archive log lines, signatures) stays English: files
  are shared and diffed.
- **Agent-facing text** (server instructions, tool descriptions, replies) and **CLI output** stay English in
  v1; agents parse replies, and models read English best. Revisit if a team asks.

Catalogs:
- Built-in catalogs are embedded: `web/i18n/en.json` (the source, every key), `ja.json` and `zh-TW.json`
  (Traditional Chinese, user decision 2026-11-20). Keys are flat
  (`drawer.save`); values take `{name}` placeholders; plurals use `<key>.one` / `<key>.other`, picked with
  `Intl.PluralRules`. A missing key falls back to English, then to the key itself.
- A board can add `i18n/<lang>.json` to override keys or add a language. It loads like a suite file: a file
  that does not load never stops work, and `GET /api/warnings` (`i18n`) says what is wrong.
- A suite file gives labels per language in `[lang.<code>]` tables: `title`, and `wording`, `pipelines`,
  `disciplines`, `steps`, `statuses`, `templates`, `views`, `fields` and `fields_help`, each mapping a key or name
  to its label. A label without a translation shows the suite's own text. Extending a built-in suite can add or
  override one language's labels; they merge per key. `gamedev-unreal` ships `[lang.ja]` and `[lang.zh-TW]`.
- Free-text fields that hold a key (discipline) show its label and turn what was typed back into the key on
  save, so a Japanese and an English browser write identical task files.
- `GET /api/i18n` lists the languages (each named in its own language) and the board default; `GET
  /api/i18n/<code>` returns a language's catalog with the board file merged in. `en-XA` is generated in the
  browser from English.
- Tests: a UI string in `index.html` or `app.js` that bypasses the catalog, a key missing from `en.json`, and a
  built-in language that lacks a key or changes a placeholder all fail `go test ./web/`.

Choosing the language:
- Per browser, in Settings: `Auto` (the first of `navigator.languages` that has a catalog, else `[ui]
  language` in `board.toml`, else English) or one listed language. Kept per browser like the other UI settings.
- Dates, times and numbers in the UI use `Intl` with the chosen language; times written into files do not
  change (§3.2).
- Filter terms are typed as keys (`discipline:art`); pills and pickers show the labels. Free-word search
  already works in any language (§7).
- Layout: a font stack that covers CJK, no fixed-width text controls, and a pseudo-language (`en-XA`: longer
  text with accents) to catch hard-coded strings and overflow. Right-to-left languages are not in v1.

### 4.5 Knowledge base (after v1, M9)

Agents keep re-learning the same things: where a system lives, why a rule exists, how a build step works. The
knowledge base keeps those answers so the next agent looks them up instead of exploring again – a cache for
questions, which knows when its answer may be out of date. It is board infrastructure like activity, not a
sixth project concept: suites do not change it.

Page file (`projects/<slug>/kb/<page>.md`, the name is a slug of the title):

```markdown
---
title: How bridge collision is generated
tags: [physics, level]
question: Why does the player fall through SM_Bridge_Mid?   # optional: the question this answers
sources:                    # what the answer was checked against
  - path: Source/RPG/Collision/BridgeGen.cpp
  - path: Content/Props/SM_Bridge_Mid.uasset
  - task: RPG-12
checked: {rev: 3f9c2e1, at: 2026-11-20T08:00:00Z, by: "claude-code:7f3a"}
retired: false
created: 2026-11-20T08:00:00Z
---

Collision for bridge meshes comes from …
```

Rules:
- Same handling as task files: atomic writes through the server, unknown keys kept, a page that fails to parse
  is flagged and never overwritten (§3.2, §3.3), `kb_fts` (FTS5) in the index, the watcher picks up hand edits
  and git pulls. Pages are data: they are not translated (§4.4). A page body is at most 64 KB.
- **Freshness** is what makes it a cache. A page is `fresh`, `stale` or `unknown`:
  - With path sources and a `checked.rev`, a lookup from an agent's working copy compares the sources at
    `checked.rev` with the working copy (git: `git diff --quiet <rev> -- <paths>`; Lore through its adapter).
    Any change → `stale`, naming the changed sources. A path source with no file in the working copy (one that
    never existed, often a typo) is also `stale`, named with "(not found)", and `kb_write` says so. The result
    is cached in the index per (page, revision).
  - A task source whose task changed status after `checked.at` makes the page `stale`.
  - No path sources, no revision, or no working copy (the web UI) → `unknown`, shown with the page's age.
  - Stale pages are still returned – the old answer plus the list of changed sources is a good start – but
    ranked below fresh ones and marked in the reply.
- Writing a page sets `checked` from the caller's working copy (HEAD, or the Lore revision) and session.
  Only a working copy whose remote is linked to the page's project counts, for writes and lookups alike: another
  repository says nothing about the page, so it leaves no revision and the page is `unknown` there.
  Re-checking a stale page without changing it is `kb_write` with `recheck: true`.
- `retired: true` hides a page from lookups but keeps the file; the UI can show retired pages.
- A new page whose slug is taken is refused with the existing page named, so agents update instead of
  duplicating. Two copies writing the same page offline is an ordinary git merge (§3.5).
- Activity (§3.6) gets a `kb` record on every write (`page`, `actor`, `session`, changed field names), and the
  server instructions tell agents to `kb_find` before exploring and `kb_write` after an answer that took real
  exploring. Lookups are counted in the index only (not synced); hit rate and "stale found" become an Insights
  row (§8.4) once there is data.

Tools and UI:
- MCP: `kb_find` (free words and `tag:`, `stale:`, `source:` terms; current project, or `scope: board`),
  `kb_show` (page, freshness and changed sources), `kb_write` (create or update; `recheck`, `retired`).
  `task_show` lists pages that name the task as a source.
- REST mirrors the tools; the UI gets a Knowledge view (list, search, read, edit in Markdown, freshness badge)
  and a "Knowledge" section in the task drawer.

How it is built (M9):
- Search: `kb_fts` uses the trigram tokenizer (any language, any substring). Free words are OR-ed, common
  English endings are also tried without the ending, and pages rank by bm25 (title > question > tags > body),
  so a question asked in other words still finds the page. Words under three characters are not searched.
- Freshness: `git diff --name-only <checked.rev> -- <paths>` in the caller's git working copy, one diff per
  checked revision for a search, cached per (page version, HEAD, repository). `checked.rev` must be a commit id
  (7–64 hex digits), else the page is `unknown`: page files are shared, and git must never see an option there.
  A search filters and orders by freshness before its limit; uncommitted edits to a source count after the next commit. Lore workspaces get `unknown` until
  the Lore adapter can give a revision. The web UI checks against the project's working folder (Settings, or
  where an agent last worked).
- A person's edit in the UI does not move `checked`: only an agent looking at a working copy can say the answer
  still holds. Sources given as absolute paths inside the repository are stored relative to its root.
- REST: `GET /api/kb?project=&q=`, `POST /api/kb/<project>` (create from the title), `GET`/`PUT
  /api/kb/<project>/<slug>` (with `version`, 409 on a stale edit), `GET /api/tasks/<id>/kb`. The UI key is `k`.

---

## 5. VCS detection

Run detectors on the agent's working folder; each returns zero or more **remote keys**:

| VCS | How | Key format |
|---|---|---|
| git | `git -C <dir> remote -v` (origin first) | `git:<host>/<path>` e.g. `git:github.com/acme/rpg` |
| Lore | walk up to `.lore/config.toml`, read `remote_url` (+ `.lore/id`) | `lore:<host>:<port>/<repo id>` |

**Normalization**
- Keys always include the host, so the same path on two hosts never collides.
- Lowercase scheme and host; strip `.git`; strip trailing slashes; drop user names and default ports – a
  URL drops only its own scheme's default (ssh 22, https 443, http 80, git 9418); in key form, where the scheme
  is unknown, 22 and 443 are dropped; Lore 41337. Lowercase the path for git; keep path case for Lore. Key-form
  input (`git:GitHub.com/Acme/RPG.git`) is normalized the same way, ssh aliases included.
- `git` runs with `GIT_*` variables removed, no system config and no fsmonitor (commands that ask a remote –
  board sync, `git lfs locks` – keep the system config, where Git for Windows sets the credential helper); ssh hosts that start with `-`
  or contain spaces are refused. Detected keys are cached per folder for 5 minutes (a failed or empty result
  for 30 seconds), so a remote added later is seen.
- git: these all become `git:github.com/acme/rpg`:
  `git@github.com:acme/rpg.git`, `https://github.com/acme/rpg`, `ssh://git@github.com:22/acme/rpg.git`,
  `https://user@github.com/acme/rpg/`.
- git ssh host aliases: resolve the alias with `ssh -G <alias>` (read `hostname`); if that fails, keep the
  alias as the host. `link_repo` accepts either form.

**General**
- Every detector call has a timeout (3 s). Detection failures are silent (no key), never errors.
- Lore is pre-1.0. Verified against Lore 0.14 and a Lore server: the workspace root has `.lore/config.toml`
  whose top-level `remote_url` names only the server (`lore://<host>:<port>`), and `.lore/id` holds the
  repository id (16 bytes; `lore status` shows it as 32 hex digits). The key is `lore:<host>[:<port>]/<id>`;
  a `remote_url` that has a path keeps the path instead. A repository made with `--offline` has an empty
  `remote_url` and gives no key. `repo_link` accepts a `lore://` URL as well as a `lore:` key.

**Lock display**
- Which files: the task's `asset`, plus file paths written in backticks in the body. Unreal object paths are
  mapped to files: find the `.uproject` above the working folder; `/Game/…` → `<project>/Content/…`,
  `/<Plugin>/…` → `Plugins/<Plugin>/Content/…`; try `.uasset`, then `.umap`.
- Lore: `lore --non-interactive lock status -- <files>` in the workspace, one call per task. Its lines read
  `<path> by <owner> on <RFC 1123 time>` (paths may hold spaces; the owner is `<unknown>` for a lock taken
  without a signed-in identity, shown as "owner not reported"); unlocked files print nothing.
- git LFS: `git lfs locks --json` (optionally `--path`), only when `git lfs` is installed and the repo's
  `.gitattributes` marks files `lockable`. It asks the LFS server, so it runs with the 3 s timeout and without
  prompts (`GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never`, ssh in batch mode); if auth or the network fails,
  the UI shows "lock state unknown", never an error. git-lfs 3.5 prints `[]` and exits 0 when it cannot get a
  login, so an empty list with a message on stderr also counts as unknown.
- Lock state is fetched only when a task is opened in the UI or `task_show` asks for it, batched per task
  (one call per repo), and cached for 60 s (failures too). It shows owner and since-when; quest-map never takes
  or breaks locks.
- `task_show` reads files and locks in the caller's working copy. The web UI has no working folder, so it uses
  the folder a person set for the project in Settings (this machine only), else the folder an agent last used
  the project from; until there is one it says so. Both are kept in `.run/workdirs.json` (machine-local, never
  synced). Lock answers are shared between callers asking at once, and a caller that goes away (a browser
  reload) does not cut a lock command short. Git LFS locks apply when any of the task's files is `lockable`
  (top `.gitattributes`, else `git check-attr` for nested ones).

---

## 6. MCP interface

Transports: **stdio** (`quest-map mcp`, a proxy to the server – §3.4) and **streamable HTTP** at `/mcp` on the
local server (requires the token – §8.1).
Without a `project` argument, tools use the project linked to the caller's working folder (stdio: the
proxy's cwd; HTTP: the `roots` the client reports, else require `project`).

Replies are short plain text, not JSON dumps, e.g.
`RPG-12 doing (agent, code, vertical-slice): Player falls through bridge collision`.
Each reply also carries the same data as MCP `structuredContent` for clients that parse results.

| Tool | Purpose |
|---|---|
| `project_list` | Projects with open counts; marks the one for the current folder. |
| `project_create` | key, name, optional repo remote. |
| `repo_link` / `repo_unlink` | Map a git or Lore remote to a project (`remote` defaults to the caller's working folder's remote). |
| `task_find` | Filter string (§7); default: open tasks of the current project. |
| `task_show` | Full task: front matter, body, log, spent totals, parse warnings, lock state. |
| `task_claim` | Claim the best unblocked `todo` task with `doer: agent` (optional discipline/milestone filter), see below. |
| `task_new` | title, body, discipline, milestone, priority, doer, labels, asset/pipeline. |
| `task_edit` | Any front-matter field; status changes are logged automatically. |
| `task_note` | Append to the task log. |
| `task_release` | Clear the claim and set the task back to `todo`, with a note. |
| `task_archive` | Move a `done` task to `archive/`. |
| `task_move` | Move a task to another project (new id; `moved_from` set). Open, not-done tasks in any project whose `blocked_by` names the old id are rewritten to the new id, with a log line. |
| `pipeline_advance` | Move an item task (game dev: asset) to its next pipeline step. |
| `release_record` | id, target, revision, notes (game dev: a build and its platform). |
| `feedback_report` | Feedback → backlog task with the suite's intake defaults (game dev: playtest note → QA task). |
| `insights` | Read-only: the §8.4 metrics for the project (or one task), optionally grouped by model/client/discipline. |
| `kb_find` / `kb_show` / `kb_write` | Knowledge base (§4.5, M9): look up answers with their freshness; write or re-check a page. |

Permanent delete is only available in the web UI, not to agents.

`blocked_by` written by `task_new` / `task_edit` is checked: every id must name an existing task (any project,
archived counts), a task cannot wait on itself, and a list that closes a cycle of waits is refused (the error
shows the cycle). Hand-edited files are not checked; an unknown blocker there does not block (§7).

**Reporting usage (§3.6)**
- Every call records the client automatically (`clientInfo`), and every status change writes a `status`
  record with session, client and model.
- `task_note`, `task_edit` and `task_release` take optional `model` (string), `spent_tokens` (integer total)
  and `spent_time` (duration like `25m`, `1h30m`). Tokens and time are increments since the caller's last
  report on that task and become a `usage` record with `source: self`. Tool descriptions mark them as
  recommended. `model` is remembered for the session, so passing it once is enough.
- With measured capture active for the session (§3.6), self-reported tokens/time are ignored and the reply
  says so once.
- The reply shows the task's totals, e.g. `RPG-12 review … · spent 184k tok, 2h15m, ~$3.10`.
- Nudge, not enforcement: when the claim holder writes with no usage and no measured capture, the write still
  succeeds and the reply adds one short line:
  `Tip: pass model / spent_tokens / spent_time. 25m since your last report.` The elapsed time is only a hint
  (it includes idle time); the server never records it on its own.
- `task_claim` replies with the task's current totals, so an agent resuming work sees what was already spent.

**Updates to agents**

Agents can't receive pushes (no listener; MCP resource-update notifications are mostly not shown to the
model), so updates are pulled: they ride on replies the agent already gets. Changes from any origin count,
because they all land in `activity.jsonl`.
- **Digest, routed by task.** A reply to a call about task X ends with changes to X made by *someone else*
  since this session last touched X, e.g.
  `Updates: person commented on RPG-12: "use the LOD1 mesh" · RPG-9 (RPG-12 waits on it) → done`.
  "About X" covers X itself and tasks in X's `blocked_by`. The server keeps a cursor per (session, task).
  At most 5 lines, then `+N more (task_show RPG-12)`.
- **Changed since your claim.** If a person changes a claimed task in any way – a note, a field, the body –
  the claim holder's next write to it still succeeds, and the reply starts with
  `RPG-12 changed by a person since your claim – re-read it (task_show).` This also shows on the write that
  hands the task off (`review` clears the claim). After a server restart, a claim holder with no cursor gets
  the changes since its claim.
- **No task in the call.** Replies to `task_find` and `task_claim` list new person notes marked as
  announcements (label `announce` on the note's task) since the session last saw them.
- Routing by task, not by session, is what makes this work in multi-agent harnesses where subagents share one
  MCP session: each subagent usually works one task, so it gets that task's updates. Known v1 limits: no
  per-subagent identity (an optional `agent` argument), no per-subagent usage attribution, and updates seen by
  a subagent reach the lead only through the subagent's own summary. Outbound webhooks (chat/CI) are deferred.

**Claiming (`task_claim`)**
- Runs under the project mutex as one check-and-set: pick the task, set `status: doing` and
  `claim: {by: <session id>, at: <now>}`, write, then reply. Two agents calling at once get different tasks.
- Order: priority (urgent → low), then milestone date (earliest first, no date last), then `created`
  (oldest first).
- A claim is **stale** when the task is `doing` and has had no write for `claim_ttl` (board.toml, default
  4 h). `task_claim` never takes a stale task; the filter `stale:yes` lists them so a person or agent can
  `task_release` them.
- The claim check re-reads `blocked_by` under the mutex, so a task whose blocker was just reopened is not
  taken. A real write error is returned, not reported as "no task".
- `task_edit` to `review`, `done`, `todo` or `backlog` clears the claim.

**Agent rules** (checked under the project mutex, against the state the write acts on):
- No changes to `done` tasks; only notes on `doer: person` tasks; `doing` only through `task_claim`.
- A task claimed by another session: notes only – no `task_edit`, `task_move` or `task_release` – unless that
  claim is stale. `task_release` needs a claim.
- Agents do not set `done` unless `board.toml` says `[agents] may_set_done = true` (default false); they move
  finished work to `review` and a person decides. People (UI, CLI, hand edits) are not limited by this.
- When `project` is omitted and the working folder's remotes are linked to different projects, the call fails
  and asks for `project`.
- `model` is at most 80 characters from a fixed set.

**Replies** end with a line `quest-map session: <agent session id>`, used to link measured usage (§3.6).
Each reply is sent as text content and as structured content; the structured content repeats the whole reply
text (marker included) in a `message` field, because some clients (Claude Code) show only the structured
content when there is any. `task_show` and `task_find`, whose text only repeats their fields, put just the
other lines (notes, "Updates:", "+N more", announcements) and the marker in `message`. Errors are text only. A not-found error for a moved task names its new id.

**Server instructions** (sent on initialize), in our own words:
> Pick up work with `task_claim`; it marks the task as yours. Record progress with `task_note` as you go.
> When the work is ready for a person to check – or you cannot continue – move it to `review` and leave a
> note saying what is done and how to pick it up again. Put the PR or Lore revision in `url`.
> Do not change tasks whose doer is `person` (notes are fine), and do not change `done` tasks.
> Tell the board which model you are with `model` on your first write. Unless a reply says your usage is
> measured, also pass `spent_tokens` and `spent_time` for the work since your last report – a rough estimate
> is fine. Replies may end with "Updates:" – read them. Agents do not set `done` unless the board allows it.
> The last line of each reply ("quest-map session: …") lets the board measure your usage; ignore it.

---

## 7. Filter language

Space-separated terms, all must match:
`status:doing`, `-status:done`, `doer:agent`, `assignee:mika` (any of the assignees), `discipline:art`, `milestone:alpha`, `label:physics`,
`priority:high`, `found:latest`, `found:latest/ps5`, `blocked:yes`, `stale:yes`, `spent:no`,
`model:claude-opus*`, `asset:SM_Bridge*`,
free words (full-text search). Values with spaces use quotes: `label:"art review"`.
Any term can be negated with a leading `-`. Repeating a key narrows further (`status:todo status:doing`
matches nothing); there is no OR. Keys and fixed values (`status`, `doer`, `priority`, `yes`/`no`) are
case-insensitive. An unknown `key:` is an error naming the known keys (typos like `stauts:`), except where
it cannot be a key: URLs (`https://…`), times (`10:30`) and non-ASCII text before a colon are free words.
Free words match as substrings in any language (Japanese has no word spaces); terms with no letter or digit
(`-`, `""`) are ignored.

Definitions:
- `found:latest` – `found_in` is the project's release with the newest `date`, across all targets.
  `found:latest/<target>` – the newest release for that target (game dev: platform).
- `blocked:yes` – at least one task in `blocked_by` exists and is not `done` (archived counts as done).
- `stale:yes` – see §6.
- `spent:yes` – the task has any usage record (also a self-report that measured data replaced); `spent:no`
  finds tasks nobody reported on.
- `model:<glob>` – the task has usage or a status change from a matching model, following moves and
  renumbers like spent totals.

The same parser serves the UI search box, `task_find` and `task_claim`.

---

## 8. HTTP API and web UI

### 8.1 Local server

- Binds to `127.0.0.1` only. Default port **47321** (`board.toml`); if it is taken, the server picks a free
  port and writes the actual one to `.run/server.json`, which `quest-map mcp` and `quest-map install` read.
- Every request must have a `Host` of `127.0.0.1:<port>` or `localhost:<port>` (blocks DNS rebinding).
- Every non-GET request must have no `Origin` or an `Origin` equal to the server's own (blocks CSRF).
- `/api/*` and `/mcp` require either `Authorization: Bearer <token>` (agents, CLI) or the `quest_map_session`
  cookie (`HttpOnly; SameSite=Strict`), which the server sets when it serves the UI page.
- JSON REST under `/api/…` mirrors the MCP tools; the UI uses only this API. Renaming and deleting a project
  (`PUT /api/projects/{slug}/name`, `DELETE /api/projects/{slug}`) are REST only: agents never delete projects.
- Live updates via Server-Sent Events (`/api/events`) driven by the file watcher.

### 8.2 Feedback intake listener (opt-in)

People reporting feedback (game dev: playtesters) are on other machines, so the localhost server can't reach them. With
`[intake] listen = "0.0.0.0:47322"` and `token = "…"` in `board.toml`, the server opens a second listener
that serves only `POST /feedback` (text, optional screenshot up to 10 MB, release id, `Bearer` intake token),
rate-limited per client IP. Off by default. Release tooling (game dev: build machines) can call `quest-map feedback` instead.
With `[intake] project` set, the listener files only into that project and refuses others. Reports are
recorded as actor kind `intake`, named `<feedback word>:<reporter>` (game dev: `playtest:mika`), so a remote
reporter never passes for a person or agent of the board; the reporter name is cut to one line of 40 characters.

### 8.3 UI

- One embedded HTML/CSS/JS page, no build step. Views: board (columns by status, optional swimlanes by
  discipline or milestone), task drawer, milestone view, releases list (wording from the suite). Keyboard first. Light/dark.
- Search box: a task id (`RPG-12`, any case, or `#12` in the current project) is a jump, not a filter:
  Enter opens it. When the box is not being edited, filter terms show as pills; a pill's × drops that term.
- The card or row under the mouse is the selected one (arrow keys also move the selection); a click opens
  it in the drawer.
- Keys on the selected card (board or table) without opening it: `1`–`5` status, `p` priority, `d` doer,
  `l` toggle a label (or add a new one), `a` toggle an assignee (or add a new name). Each opens a small picker next to the card: typing
  filters it, and Enter or the shown digit picks. Assignee names come from the tasks on the board.
- "My tasks" toggle (`Shift+M`): shows only tasks whose assignees include your name. The name is set in
  Settings (per browser) and defaults to the server's user name; it is added to the filter as `assignee:`.
- Board or table layout (`t`). The table has one row per task with ID, title, status and changed time,
  plus the view's card fields as columns. Status, priority, doer, discipline, milestone and labels are
  edited in place. Ticked rows (`x`) get a bulk bar that sets status, priority, doer, discipline or
  milestone, or adds a label, on all of them. Swimlanes become group rows. Table, quick-picker and bulk writes
  send the row's task version (409 if the file changed meanwhile, then the row reloads); label and assignee
  changes are sent as `add_labels`/`remove_labels`/`add_assignees`/`remove_assignees`, which never drop
  entries someone else added.
- Auto-archive: `archive_done_after_days` in `project.toml` (0 or missing = never). The server archives done
  tasks that have not changed for that many days when it starts and every hour, signed "quest-map
  (auto-archive)". "Changed" is the later of the task's last log line and its last activity record (a hand
  edit leaves only the record), both of which travel with the board. The archive is stamped with that time
  plus the days, not the current time, so two machines syncing one board by git write the same archive file. Set per project in Settings (`PUT /api/projects/{slug}/archive`
  `{days}`), which also archives what is already due. Archived tasks still open by id.
- Task panel: the drawer is a column on the right of the board (like the project sidebar on the left), so it
  never covers cards. It collapses to a rail (`]`, its » button, or a click on the rail expands it again),
  opening a card expands it, and its width is dragged from its left edge (kept per browser). Below 1100 px it
  floats over the board, so the board keeps its width.
- Terse card fronts: id, title (at most three lines), priority, claim (the client's name; the full id on
  hover), assignees, discipline and pipeline steps; everything else is
  in the drawer, and a view's card fields can add it back. Priority shows as the card's left edge.
- Header: views, search box and New task on top; the board's own controls (saved view, layout, My tasks,
  lanes, sort) in a second bar shown only on the board. Below 900 px the sidebar shows as its icon rail.
- Per-machine styles: `custom.css` in the user's config folder (`%AppData%\quest-map\` on Windows,
  `~/.config/quest-map/` on Linux) is loaded after the UI's own stylesheet; Settings shows the path. It is never
  part of a board, so it does not travel with one synced by git.
- A collapsible sidebar lists the projects with their open-task counts and opens Settings (theme, collapsed
  columns). Every status column and swimlane can be collapsed; the choice is kept per browser.
- Project menu: a ⋯ button on each project in the sidebar (shown on hover or focus; right-click works too, also
  on the icon rail) offers Rename…, Settings… and Delete…. Rename changes the name only; the key, folder and
  task ids stay. Delete asks the person to type the project key, is refused while an agent holds a live
  (not stale) claim in the project, and moves the project folder to `.trash/<slug>-<UTC time>/` in the board
  root instead of removing it: restoring is moving the folder back to `projects/`. A New project button under
  the list (and on the empty board) opens a dialog: name, key (suggested from the name's initials), folder
  (the key in lowercase unless edited), suite (the board's default first) and an optional repository to link –
  `POST /api/projects`, the same rules as `project_create`. `.trash/` is never synced,
  so on a board synced with git the other copies lose the project at their next sync. Emptying the trash is
  left to the person.
- Blocked tasks: a card or table row whose `blocked_by` names a task that is still open (the `blocked:yes` rule,
  §7) shows a `⛔ <first blocker> +N` chip whatever the view's card fields, and the card is dimmed. The drawer
  lists each blocker with its status (done and archived ones struck through) and the open tasks waiting on this
  one; `GET /api/tasks` rows carry `Blocked` (open blockers), `GET /api/tasks/{id}` `blockers` and `blocks`.
  Moving a blocked task to doing is allowed; the toast says it is still blocked.
- Columns: a status with no task on the board is a narrow strip ("Nothing here") that still takes drops. Done
  shows its 10 most recently changed cards, with "Show all N" (kept per browser) and a pointer to auto-archive.
- Shows parse warnings, stale claims and renumbered tasks (§3.5).
- Spent: a small `184k tok · 2h15m` badge on cards that have usage (`~` prefix when any of it is
  self-reported); the drawer shows totals by model and token type, cost, and the task's status history, and
  lets a person add a correction (§3.6); the milestone view sums spent per milestone.
- Attachments: paste an image into a task → saved under `attachments/` (max 10 MB each), linked in the body.

### 8.4 Insights

Goal: tell the user how well their agent setup works – not how much it was used. Outcome metrics come first;
volume is context. Everything is computed from `activity.jsonl` (§3.6) and task fields, over a chosen period.

| Metric | Definition |
|---|---|
| First-pass acceptance | Share of agent tasks whose first `review` was followed by `done`, not by a move back. |
| Rework | Moves back from `review` (to `doing`/`todo`/`backlog`) per task, and tokens/time spent after the first `review`. |
| Reopened after done | Share of `done` tasks that later left `done`. With releases: a reopened task whose new `found_in` is at or after its `fixed_in` counts as a regression. |
| Cost per accepted task | All cost on tasks that reached `done` ÷ their count. Cost on released/abandoned tasks and on `task: null` usage is shown next to it as waste and overhead. |
| Abandonment | Claims ending in `task_release` or going stale, and agent moves to `review` with a "cannot continue" note, per task claimed. |
| Human attention | Time tasks wait in `review`; person notes per agent task; review queue depth over time (more agent output with flat `done` = review backlog). |
| Cycle time | First claim → first `review`; first `review` → `done`. |
| Baseline | The metrics above for `doer: person` tasks in the same discipline, side by side with agent tasks. |
| Coverage | Share of agent work with usage records, and measured vs self-reported share – how far to trust the cost numbers. |

- Group by model, client, discipline, label or milestone. A task's model is the one with most tokens on it;
  tasks that used several models are marked. Comparisons are shown within a discipline/label, never as one
  overall model ranking, because models get different kinds of work.
- Every figure shows its sample size; groups with fewer than 5 tasks are greyed out.
- Surfaces: an Insights view in the UI, `quest-map stats` (text, or `--csv`), the `insights` MCP tool, and
  `GET /api/activity` (raw records) for Grafana or a spreadsheet. Sending to OpenTelemetry is a later option.
  `GET /api/insights?project=&since=&by=&task=` returns the report as JSON.

How the definitions are applied (M7):
- **Period**: a task counts when it has activity at or after `since`, or is in `review` or `doing` (open work
  waiting since before the period); its figures use its whole history.
  Records follow tasks through moves and renumbers (§3.6), and self-reports replaced by measured data never count.
- **Claim**: a move to `doing`, or an agent taking over a claim without a status change. **Abandoned**: a move
  from `doing` back to `todo`/`backlog` (release), a claim taken over by another agent session, a current claim
  older than `claim_ttl` with no activity, or a move to `review` written together with a note containing
  "cannot continue".
- **Cost**: a record's own `cost_usd`, else its tokens at `[prices."<model>"]` in `board.toml`
  (`input`, `output`, `cacheRead`, `cacheCreation`, USD per million). Unknown models and total-only self-reports
  count as $0 and are listed as not priced. **Waste**: cost on tasks that are not done and had an abandoned claim.
  **Rework spent**: usage recorded after the first move to `review`.
- **Medians** take the lower middle value. The **review queue** counts agent tasks per day in the board time zone
  (at most 92 days).
- **Client** groups by name without version; a task's client and model are those with most tokens on it, else
  those of its first claim.

### 8.5 UI plugins (after v1, M10–M11)

A UI plugin adds a view to the web UI. It runs sandboxed, reads the board through a small API and keeps its
own data on the board. The server never loads plugin code: no Go plugins, no WebAssembly, the binary stays
single and cgo-free. Plugins add no MCP tools; agent-facing features go into the core (§4.5).

Plugin folder:
- `plugin.toml`: `name` (lowercase letters, digits, `-`), `version`, `title`, `entry` (an ES module, default
  `main.js`), `views` (each `id`, `title`, `icon`, `scope` = `project` or `board`), `permissions` (below).
- Optional `i18n/<lang>.json` for its own text, loaded like §4.4 catalogs, and any static files it ships
  (scripts, styles, images). No network: plugins load nothing from outside the folder.
- **Built-in plugins** are embedded in the binary and use the same API – `reports` and `vision` first. Others
  come from `plugins/` in the user's config folder (next to `custom.css`, §8.3). Both are off until enabled in
  Settings, per machine (kept in the browser, like the language). A folder's name must be the plugin's name; a
  folder plugin cannot replace a built-in one. A plugin that fails to load never breaks the UI; `GET /api/warnings` (`plugins`)
  says why.

Sandbox:
- Each view is an `<iframe sandbox="allow-scripts allow-downloads">` (opaque origin, no cookies, no storage of
  the page; downloads for CSV) served from `/plugins/<name>/…` with a CSP that allows only the plugin's own
  files, `connect-src 'none'`, and its own `sandbox` directive, so a plugin file opened on its own is sandboxed
  too. Plugin files are public (no token) and sent with `Access-Control-Allow-Origin: *`, which the opaque
  origin needs to load ES modules. The plugin cannot read the session cookie or call `/api` itself;
  everything goes through the host over `postMessage`. `/plugins/<name>/_qm/` is the frame page and the API
  script every plugin shares.
- The host passes the theme as CSS variables, the chosen language, and the view's size.

Plugin API (`activate(api)` in the entry module; every call is a promise):
- Read: `api.tasks.find(filter, {archived})` (§7), `api.tasks.get(id)`, `api.activity({since, until, kinds})`
  (§3.6 records, following moves and renumbers: `GET /api/activity?follow=yes&until=&kinds=`), `api.releases()`, `api.milestones()`, `api.suite()` (wording, lists, labels
  in the chosen language), `api.project()`.
- Live: `api.on("change", fn)` – task, activity and storage changes relayed from `/api/events`; a storage
  change names its `key`, so a view can reload just that key (`all` means reload everything).
- Own data: `api.storage.list(prefix)`, `get(key)`, `put(key, json)` (1 MB), `delete(key)`, and
  `putBlob(key, file)` / `blobURL(key)` for PNG, JPEG, GIF or WebP images (10 MB each, like attachments; no
  SVG). Blobs share the key space (`/api/plugins/<name>/blobs/<key>`); the host fetches one and posts it to
  the frame as a `Blob`, which makes its own `blob:` URL. Stored under
  `projects/<slug>/plugins/<name>/` (board-scoped views: `plugins/<name>/` at the board root), one file per
  key, written atomically by the server, so data syncs with the board and different keys never conflict in
  git. A key is a relative path of `[a-z0-9._-]` segments.
- Host actions: `api.ui.openTask(id)`, `api.ui.newTask(fields)` (opens the New task dialog filled in – the
  person creates it), `api.ui.toast(text)`, `api.ui.openLink(url)` (http and https only, in a new tab: the
  frame has no `allow-popups`).
- Also: `api.view` (`id`, `title`, `scope`), `api.lang`, `api.timezone` (the board's `timezone` as an IANA
  name; undefined when it is `Local`, so the browser's applies), `api.t(key, vars)` (the plugin's catalog, with
  `view.<id>` and `title` used by the host for the tab and Settings), `api.download(name, text, type)`.
- Permissions in `plugin.toml`: `read` (always), `storage`, `ui`. Settings lists them when the plugin is
  enabled. Plugins never write tasks or activity directly in this version.
- Known limit: no CSP directive stops a frame from navigating itself, so a plugin could carry what it read to
  another site in the URL of one page load. The host removes a plugin frame on its second `load` event (it
  left its page), keeps that plugin stopped until the page reloads and says so; one request may still have
  gone out. Settings says a plugin can read the board and send what it reads elsewhere: folder plugins are
  trusted code, like anything else a person installs. Built-in plugins are tested to use only the API.

`reports` (built-in, M10) – charts for people, next to Insights (which judges the agent setup, §8.4):
- Cumulative flow (tasks per status over time), burn-up per milestone, throughput per week by discipline or
  doer, cycle-time scatter, bugs found and fixed per release (game dev: per build), item pipeline step counts.
- Period and filter on every chart; numbers come from `activity.jsonl` and task fields, as in §8.4. Days and weeks are those of
  the board's `timezone` (`api.timezone`), like Insights, whatever the viewer's own zone is. Charts are
  plain SVG drawn by the plugin (no chart library), with a table and CSV download for each.
- Saved report settings go to the plugin's storage.

`vision` (built-in, M11) – a canvas per project for direction: pillars, reference images, notes, links and
task cards.
- Items: `note`, `image` (pasted or dropped, stored as a blob), `link`, `task` (live id, title and status),
  `frame` (a named group, e.g. a pillar). Free placement, resize, zoom and pan; several canvases per project.
- Each item is one storage key (`<canvas>/items/<id>.json`: type, position, size, order, content; the id is
  lowercase and sorts by time), and the canvas key (`<canvas>/canvas.json`) holds only its name and view, so
  two people editing different items merge cleanly. Images are blobs at `<canvas>/blobs/<id>.<ext>`.
- Moving a frame moves the items whose centre is inside it. Pasted text becomes a task card (a task id), a
  link (a URL) or a note.
- "Make a task" on a note opens the New task dialog with its text (`api.ui.newTask`).
- Not media review: no annotation of images or video (§1 non-goals).

---

## 9. CLI

```
quest-map serve              # start web UI + HTTP MCP (holds the board lock)
quest-map stop               # stop the running server: it finishes its writes and releases the lock
quest-map restart            # stop it and start it again in the background (after installing a new build)
quest-map mcp                # stdio MCP proxy (what agents launch); starts the server if needed
quest-map install <client>   # register the MCP server and usage hook with an agent client (--board DIR)
quest-map feedback <text>    # file a feedback report (--release, alias --build; --screenshot)
quest-map reindex            # rebuild the SQLite cache
quest-map find <filter>      # list tasks matching a filter (§7), through the running server (else with the lock)
quest-map export             # the whole board as one Markdown document; --format json, --project, --out FILE,
                             # --archived; reads task files directly (no server or lock needed)
quest-map stats              # Insights metrics (§8.4); --project, --since 30d|2026-11-01, --by model|client|
                             # discipline|label|milestone, --task ID, --csv; through the running server (else with the lock)
quest-map usage-hook         # client hook: read new transcript usage, post it (§3.6); installed by `install`
quest-map autostart on|off|status  # start the server at login for the current user
quest-map update             # install the latest release over this binary, then restart a running server;
                             # --check only looks (release installs; `go install` builds use go install)
quest-map version
```

Board commands take `--board DIR`; default `$QUEST_MAP_BOARD`, else `~/.quest-map`.

`quest-map stop` asks the server to shut down (`POST /api/shutdown`, board token only – the UI's cookie is
not enough) and waits for the board lock to be free; a server that does not answer (an older build, or one
that hangs) is ended by its pid, only while it still holds the lock. `quest-map restart` then starts the
server in the background from the binary that ran the command, the way `quest-map mcp` does (log in
`.run/serve.log`). Agents' `mcp` proxies reconnect on their own (§3.4).

`quest-map autostart` uses a per-user mechanism (Windows: `HKCU\…\Run` entry; Linux: systemd
user unit), not a system service – a Windows service runs as LocalSystem and would not find `~/.quest-map`.

---

## 10. Tech stack

| Concern | Choice |
|---|---|
| Language | Go (current stable) |
| HTTP | `net/http` with the standard method/path router |
| MCP | official Go SDK `github.com/modelcontextprotocol/go-sdk` |
| SQLite | `modernc.org/sqlite` (pure Go, no cgo → easy cross-compile) with FTS5 |
| YAML front matter | `go.yaml.in/yaml/v3` (verify it's the maintained fork at start) |
| TOML | `github.com/pelletier/go-toml/v2` |
| File watching | `github.com/fsnotify/fsnotify` |
| File locking | `github.com/gofrs/flock` (or `x/sys` directly) |
| UI | `embed` + plain HTML/CSS/JS |
| Distribution | Default: `go install github.com/RoyalNoob/quest-map/cmd/quest-map@latest` – built locally, so Windows SmartScreen / Smart App Control never check it and no signing is needed |
| Release | GoReleaser prebuilt archives (secondary, for people without Go); Windows/Linux, amd64 + arm64, with SHA-256 checksums; archive names carry no version so `releases/latest/download/…` works; `quest-map-installer.sh` / `.ps1` (check the checksum, install to `~/.local/bin`) and `quest-map update` use them (needs a public repository) |
| Windows signing | None (user decision, 2026-11-20). Like Kanzo, every release file gets a GitHub build attestation instead (`actions/attest-build-provenance`; check with `gh attestation verify <file> --repo RoyalNoob/quest-map`) |
| Lint/test | `go vet`, `staticcheck`, `go test -race` |

Package layout:
```
cmd/quest-map/            main, CLI
internal/board/       files ↔ tasks, atomic writes, IDs, claims
internal/server/      board lock, server.json, write serialization, REST, SSE, auth checks, embedded UI, intake listener
internal/index/       SQLite cache, FTS, rebuild, watcher
internal/filter/      filter language
internal/vcs/         git and lore detectors + key normalization
internal/mcpserver/   MCP tools, stdio proxy
web/                  index.html, app.js, app.css
internal/kb/          knowledge pages, freshness (M9)
internal/plugins/     plugin loading, storage, sandbox serving (M10)
web/plugins/          built-in UI plugins: reports, vision (M10–M11)
```

---

## 11. Plan

Each milestone ends with tests passing and something usable.

**M0 – Skeleton**
- `go mod init`, package layout, CI (vet, staticcheck, test on Windows + Linux), GoReleaser config.
- `go install …/cmd/quest-map@latest` is the default way to distribute (§10); Windows code-signing is optional.
- Done when: `quest-map version` builds on Windows and Linux in CI.

**M1 – Tasks on disk**
- Task file read/write, front matter round-trip preserving unknown keys, log marker, atomic writes with
  Windows rename retry.
- project.toml, ID allocation, status set, archive and move.
- Activity (§3.6): `activity.jsonl` append/read with ULID ids, `usage`, `status`, `note` and `edit` records
  (written on every change), `(+… tok, +…)` log suffix, duration parse/format, board `.gitattributes`.
- Done when: tests create/update/note/archive/move tasks and the files diff cleanly, usage records sum
  correctly (tokens-only, time-only, split token types, corrections), and a union-merged `activity.jsonl`
  with duplicate lines gives the same totals.

**M2 – Server, index and filters**
- Board lock, `server.json`, write serialization, SQLite cache, FTS5, rebuild on start, watcher with
  debounce and echo suppression, parse-error handling, duplicate-ID renumbering, filter parser.
- `usage` / `transitions` index tables from `activity.jsonl`, spent totals, `spent:` and `model:` filters.
- Done when: editing a task file by hand shows up in `task_find`-equivalent queries within a second, and two
  processes trying to serve the same board fail cleanly.

**M3 – MCP server**
- HTTP transport with token, stdio proxy that starts the server, all §6 tools except builds/feedback/
  pipelines, claiming with stale detection, server instructions, cwd-based project pick with git detection,
  usage reporting (§6): `clientInfo` capture, `model`/`spent_tokens`/`spent_time` arguments, reply tip.
- `quest-map install` for at least Claude Code, including the `usage-hook` Stop hook; measured capture from
  Claude Code transcripts, attributed by the client's session id (§3.6) – verify the hook payload and transcript format
  against the installed Claude Code first.
- Agent updates (§6): task-routed digest with per-(session, task) cursors, "changed since your claim" warning,
  announcements on `task_find`/`task_claim`.
- Multi-agent test cases: design and automate them in this milestone, covering at least
  - two subagents in one session (shared session id), each on its own task: each gets only its task's updates;
  - a person comments on a task while its subagent is mid-work: that subagent's next write shows the
    digest and the "changed since your claim" warning; the sibling subagent sees neither;
  - a decision task that another task waits on moves to `done`: the agent on the waiting task is told;
  - the same changes made from the UI, the CLI and a hand edit produce the same updates;
  - a digest is not repeated after it was delivered, and is not lost when another subagent calls first;
  - measured usage of a session with two claims lands as `task: null`, not on the wrong task;
  - separate agent processes in one repo (each its own session) get correct routing as well.
- Done when: two agents in the same git repo call `task_claim` at once and get different tasks, and each can
  claim → note → `review` without passing `project`; a Claude Code session's measured usage lands on the task
  it claimed, and its self-reported numbers are ignored; the multi-agent test cases pass.

**M4 – Web UI**
- REST API with Host/Origin/token checks, SSE, board view, task drawer, create/edit, filter box, keyboard
  shortcuts, attachments, warnings for parse errors, stale claims and renumbered tasks, spent badge, drawer
  usage/status history and corrections (§8.3).
- Done when: a human and an agent edit the same board and both see changes live, and a cross-origin POST
  from another page is rejected.

**M5 – Project concepts and the game-dev suite**
- Suites (§4.1): built-in `gamedev-unreal`, `suites/` files that extend or add suites, `suite` in
  `project.toml`, `default_suite` in `board.toml`; wording used by the UI and agent replies.
- Adapters (§4.2): asset resolver interface (`unreal`, `path`) and lock-provider interface (the providers
  themselves come in M6).
- Milestones, releases, `found_in`/`fixed_in`, disciplines + swimlanes, item pipelines + `pipeline_advance`,
  `release_record`, feedback intake (`feedback_report`, CLI, opt-in listener); milestone view with spent per
  milestone; releases list.
- Done when: a playtest note sent from another machine becomes a QA task tied to a build, and
  `task_claim discipline:art` works – both with the game-dev suite; and a small test suite file (other
  wording, disciplines and pipelines, `path` resolver) changes all of that without code changes.

**UX pass (after M5)**
- Using Codecks and Trello as references for patterns, not looks: saved views and task templates from suites,
  custom fields, a table view for bulk triage, keys that act on the selected card (`p`, `d`, `l`, `a`), jump to
  a task id from the search box, filter terms shown as pills, a "my tasks" toggle, configurable auto-archive of
  done tasks, terse card fronts, per-machine `custom.css`.
- Code plugins are deferred until the views settle; they come after v1 as sandboxed UI plugins (§8.5, M10).

**Localization (after the UX pass)**
- §4.4: message catalog loader, every UI string moved to `en.json`, `ja.json`, Settings language picker,
  `Intl` dates and numbers, `[lang.<code>]` in suite files with `[lang.ja]` in `gamedev-unreal`, board
  `i18n/<lang>.json` files with warnings, `en-XA` pseudo-language.
- From here on, new UI text (Insights included) goes through the catalog.
- Done when: with Japanese chosen, the board, table, drawer, dialogs and Settings show no English UI text and
  the game-dev wording, disciplines and steps in Japanese; a test fails on a UI string not in the catalog or a
  key missing from `en.json`; a board `i18n/` file adds a language without code changes; and the same edits
  made from a Japanese and an English browser write identical task files.

**M6 – Lore and file locks**
- Lore detector (after verifying its config format against a real install), refuse a board inside a Lore
  workspace.
- Read-only lock display (§5): Lore locks via `lore lock query`, git LFS locks via `git lfs locks --json`,
  Unreal object path → file mapping, timeouts without prompts, 60 s cache.
- Done when: an agent inside a Lore workspace gets its project picked without passing `project`; a task naming
  a locked asset (Lore or git LFS) shows who holds the lock; an LFS server that needs a login it doesn't have
  gives "lock state unknown" within the timeout instead of a prompt or hang.

**M7 – Insights**
- §8.4 metrics, `[prices]` in `board.toml`, Insights view, `quest-map stats` (+ `--csv`), `insights` MCP tool,
  `GET /api/activity`.
- Done when: on a test board with agent and person tasks, Insights shows first-pass acceptance, cost per
  accepted task and the agent-vs-person baseline per discipline, and every metric matches a hand calculation
  from the fixture's `activity.jsonl`.

**M8 – Polish and release**
- `quest-map export`, `quest-map autostart`, README (install via `go install` first), release with prebuilt archives
  (no code signing: GitHub build attestations, installer scripts and `quest-map update`, as Kanzo does), Credits
  section.

After v1:

**M9 – Knowledge base**
- §4.5: page files, parse and watcher handling, `kb_fts`, freshness (git, Lore through its adapter, task
  sources), `kb` activity records, `kb_find` / `kb_show` / `kb_write`, server instructions, REST, Knowledge view
  and drawer section, `task_show` links.
- Done when: an agent writes a page with path sources; after a commit that changes one of them, `kb_find` from
  that working copy shows the page as stale and names the file, and `kb_write recheck` makes it fresh; a second
  agent session finds the page by a question in other words before reading code; a hand-edited page is
  searchable within a second; two board copies that wrote different pages merge with no conflict.

**M10 – UI plugins and reports**
- §8.5: `plugin.toml`, built-in and folder plugins, Settings enable per machine with permissions, sandboxed
  iframe with CSP, `postMessage` API (read, live, storage, host actions), plugin storage on the board,
  plugin catalogs, `plugins` warnings. Then the `reports` plugin.
- Done when: `reports` is built only on the public plugin API; a plugin dropped into the config folder adds a
  view without code changes; a test plugin cannot read the session cookie, call `/api` or load a remote URL;
  every report matches a hand calculation from a fixture board.

**M11 – Vision board**
- §8.5 `vision`: canvases, note/image/link/task/frame items, placement, zoom, pan, blobs, "make a task".
- Done when: a person builds a canvas with pasted images, notes and task cards that show live status; two
  board copies that moved different items merge with no conflict and show both changes; "make a task" opens
  the filled New task dialog.

---

## 12. Decisions

- **Board location:** `~/.quest-map/`, never inside a game repo or workspace (§3.1). `--board` /
  `$QUEST_MAP_BOARD` stay as an override for tests, demos and CI; `.run/` is made owner-only wherever the
  board lives.
- **Agents and `done`:** agents move finished work to `review`; `[agents] may_set_done` in `board.toml`
  (default false) can allow them to set `done`.
- **Measured-usage attribution:** by a session marker in tool replies that the hook finds in the transcript,
  not by environment variables the client may not pass to MCP servers (§3.6).
- **Duplicate-ID renumbering:** activity is split by the machine that wrote it (record `origin`), so each
  copy keeps its own spent history (§3.5).
- **Sync settings:** auto-sync is chosen per machine (`.run/sync.json`), not in `board.toml`, which travels
  with the board: one copy syncing on its own must not turn it on for every copy (§3.5).
- **Team sharing:** v1 is one person's board; syncing the folder through git is supported with duplicate-ID
  renumbering (§3.5). Simultaneous multi-user editing is out of scope.
- **Agent updates:** pulled, not pushed – a digest routed by task in tool replies (§6). Per-subagent identity,
  per-subagent usage attribution and outbound webhooks are deferred until the M3 multi-agent tests show a need.
- **License:** MIT.
- **Engine focus:** Unreal Engine first – the `gamedev-unreal` suite is the first suite and the default (§4.3).
- **Project kinds:** the core has five generic concepts; suites (configuration) and adapters (built-in code
  selected by name) tailor them per project (§4.1, §4.2). Task-file keys stay the same for every suite.
  Arbitrary code plugins are not part of v1.
- **Extensions after v1:** agent-facing features go into the core (knowledge base, §4.5). UI plugins are
  JavaScript in a sandboxed iframe that use only the plugin API, store their own data on the board and never
  write tasks directly (§8.5); `reports` and `vision` are the first, built in. The server loads no plugin code
  (no Go plugins, no WebAssembly) and plugins add no MCP tools.
- **VCS:** git and Lore in v1. Perforce is deferred; its design is kept in Appendix A.
- **Front-matter formatting:** a task file quest-map does not change keeps its bytes. When a value changes, the
  front matter is re-encoded from its YAML nodes: key order, unknown keys and comments are kept, hand
  formatting (alignment, spacing) is not – a one-time diff per hand-formatted file. In-place value edits were
  considered and not chosen (more code, multi-line values).
- **Localization:** UI text and suite labels are translated; data, keys, text written into files, agent
  replies and CLI output are not (§4.4). Language is chosen per browser. English and Japanese ship built in.
- **Board settings (`board.toml`):** log-line `timezone` (default UTC) and `[defaults]` for new tasks' status,
  doer and priority (default todo, person, normal) are configurable (§3.2).

## 13. Open questions

None right now.
- **Web UI access:** the UI page hands out the `quest_map_session` cookie to whoever loads it from
  `127.0.0.1`, so any local process or other OS user on the machine can use the UI and its API (the token in
  `.run/` stays owner-only for agents and the CLI). Accepted for v1, which targets one person's workstation.
  Browsers do not scope cookies by port, so the cookie also reaches other servers on localhost. A one-time key
  URL (`quest-map open`) is the fix if shared machines matter later. Drawer edits carry the task's version
  and are refused (409) if the file changed since it was loaded.

---

## Appendix A. Deferred: Perforce

Kept for when Perforce support is picked up again. Open question then: which server versions and auth
setups must work (tickets, SSO).

- Key format: `p4:<server>/<stream or depot root>`; keep path case.
- Server normalization: strip `ssl:`/`tcp:` prefixes, lowercase the host, keep the port (`p4:perforce:1666/…`).
- Lock display: `p4 opened -a` / `fstat`, same caching as Lore.
- Board location: also refuse a board under a detected P4 client root.
- Done when: an agent inside a Perforce client gets its project picked, and a logged-out client gives no key
  instead of hanging.

**Perforce detection**
1. Offline first: `p4 -d <dir> -ztag set` (reads P4CONFIG/P4PORT/P4CLIENT without contacting the server).
2. Then `p4 -d <dir> -ztag client -o` with a 3 s timeout to read the client's `Stream`, else the depot root
   of its first view mapping. That stream or depot root is the key's path.
3. Never run commands that can prompt (set `P4PASSWD` unset, use `-z tag` and a non-interactive stdin). If
   not logged in or the server is unreachable, detection returns no key.
- Results are cached per working folder for the server's lifetime.
