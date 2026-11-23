# quest-map user guide

quest-map is a task board that you and your agents share. Agents work it through MCP; you use the web UI at
http://127.0.0.1:47321 (start it with `quest-map serve`, see the [README](../README.md)). This guide is for
people. Agents need no guide: the server sends them their instructions when they connect.

Words in this guide follow the game-dev suite (build, platform, asset, playtest note). A project with another
suite shows its own words in the same places.

## How work flows

1. You (or an agent, or a playtest note) create a task. Each task has a **doer**: `agent` or `person`.
2. An agent claims the best open `agent` task in `todo`: it moves to **doing** with the agent's session on it.
   Agents never pick up `person` tasks.
3. The agent writes progress notes to the task's log and, when done or stuck, moves it to **review** with a
   note on what is done and how to pick it up again, plus a PR or revision in `url`.
4. You review it and move it to **done**, or back to `todo` with a note. Agents don't set `done`.

Every task is a Markdown file in `~/.quest-map/projects/<project>/tasks/`, so you can also read, diff and edit
tasks by hand; the board picks up changes.

## Projects and Settings

- The left sidebar lists projects with their open-task counts. **New project** asks for a name, a key (the
  task id prefix, e.g. `RPG`), a suite and, optionally, a git repository to link. Agents working in a linked
  repository use that project without being told.
- The **⋯** menu on a project (or right-click) renames it, opens its settings, or deletes it. Delete moves the
  folder to `.trash/` in the board; move it back to `projects/` to restore it.
- **Settings** (bottom left): theme, language, your name for "My tasks", auto-archive of old done tasks,
  board sync on this machine (see [Sharing the board](#sharing-the-board-with-git)), plugins, and the
  project's **working folder** on this machine. Set the working folder to your checkout: the task panel reads
  file locks there, and the Knowledge view checks page freshness against it.
- Your own styles go in `custom.css` in your config folder (Settings shows the path).

## Board

Columns by status: backlog, todo, doing, review, done. An empty column shrinks to a narrow strip that still
takes drops. Done shows its 10 most recently changed cards; **Show all** shows the rest, and auto-archive
(Settings) keeps it short.

- **Find**: type filter terms in the search box, e.g. `status:review discipline:art`, `doer:agent`,
  `label:physics`, `found:latest`, `blocked:yes`, or plain words. A leading `-` negates a term. Type a task id
  (`RPG-12`, or `#12` in this project) and press Enter to open it.
- **Open a task**: click a card. The task panel on the right shows the body, fields, log, time and tokens spent,
  status history, and Knowledge pages that cite the task. `Ctrl+S` saves. Fields the task doesn't need
  (e.g. found in build on a feature task) sit under **More fields**; a field with a value always shows.
- **Blocked tasks**: a task whose `blocked_by` names a task that is still open shows a ⛔ chip and its card is
  dimmed. The task panel lists its blockers and the tasks waiting on it. You can still move it to doing.
- **Change without opening**: point at a card and press `1`–`5` (status), `p` (priority), `d` (doer),
  `l` (label) or `a` (assignee).
- **Layout**: lanes by discipline or milestone (`g`), sort, board or table (`t`). In the table, edit fields in
  place, or tick rows with `x` to change many at once. Save a filter + layout as a view and pick it with `v`.
- **My tasks** (`Shift+M`) shows tasks assigned to your name.
- **New task** (`n`) can start from a template such as a bug report. **Save as template** there, or **Save
  to suite** in the View dialog, keeps it in the project's suite for everyone on the board.
- Cards show a `184k tok · 2h15m` badge when agents reported usage (`~` means partly self-reported).
- Paste an image into a task to attach it.

Press `?` for every keyboard shortcut.

## Milestones

Named targets with optional dates (e.g. `vertical-slice`, `alpha`). **Edit milestones** adds, renames and
dates them. Each milestone shows its tasks and the time and tokens spent on them. The **Suite** picker
here chooses what the project calls things, its disciplines and its asset pipelines. **Edit suite** opens the
board's copy of the suite as TOML, with the built-in suite beside it for reference; a file with a mistake is
refused, not saved, and removing it resets the built-in suite.

## Builds

Recorded deliverables: build id (e.g. `b2026-11-21.1`), platform, VCS revision and notes. Tasks say which
build a problem was **found in** and which build has the **fix**, so `found:latest` lists the bugs in the
newest build.

**Playtest notes**: **File feedback** on this view files a note as a backlog task (discipline `qa`, label `playtest`,
found in the build you give). Playtesters on other machines can send notes through the optional intake
listener, and build machines through `quest-map feedback`.

## Insights

How well your agent setup works – outcomes first, volume second. Pick a period and group by discipline,
model, client, label or milestone.

- **First-pass acceptance**: agent tasks accepted at their first review.
- **Rework** and **reopened after done**: how often work came back.
- **Cost per accepted task**, plus waste (cost on abandoned work).
- **Abandonment**, **human attention** (time waiting in review), **cycle time**.
- **Baseline**: the same figures for `person` tasks, side by side.

Each figure shows its sample size; groups under 5 tasks are greyed out. **Raw data** gives the activity
records as JSON for Grafana or a spreadsheet; `quest-map stats` prints the same report in a terminal.

## Knowledge

Answers agents found while exploring the code, kept so the next agent looks them up instead of exploring
again. Agents search it before they start and add a page after an answer that took real work. You can read,
write and fix pages too.

- **Read**: pick a page on the left. It shows the question it answers, its sources (repo files and tasks it
  was checked against), when it was last checked and by whom, and the answer.
- **Freshness badge**:
  - `fresh` – none of the source files changed since the page was checked.
  - `stale` – a source file changed, or a source task changed status; the page lists what changed. The old
    answer is still shown: it is usually a good start.
  - `unknown` – there is nothing to check against: no file sources, never checked, or no working folder set
    for the project on this machine (Settings).
- **Search**: plain words (they match in any language), or `tag:build`, `source:Source/Physics`,
  `stale:yes`.
- **New page**: title, the question it answers, tags (comma separated), sources (one per line: a repo path,
  or `task:RPG-12`) and the answer in Markdown.
- **Edit** changes a page. Your edit doesn't mark it as checked: only an agent that has looked at the code
  can say the answer still holds.
- **Retire** hides an outdated page from searches; tick **Show retired** to see it and **Restore** it.

Pages are Markdown files in `projects/<project>/kb/`.

## Plugins and Reports

Plugins add views to the top bar. They are off until you turn them on in **Settings → Plugins on this
machine**, which also says what each one may do. The choice is kept in your browser.

- **Reports** is built in: cumulative flow, burn-up per milestone, throughput per week (by discipline or
  doer), cycle time (doing → done; click a dot to open the task), bugs per build (by found-in / fixed-in) and
  open items per pipeline step. Each chart has a period and a filter (the board's filter words) and a table
  with **Download CSV**. Days and weeks count in the board's time zone (`timezone` in `board.toml`, default
  UTC). Your last choice is saved on the board.
- **Vision** is built in too: canvases per project for the game's direction. Add notes, frames (a named
  group such as a pillar – moving it moves what is inside), links, task cards (they show the task's live
  status) and images. Paste or drop images, links, text or a task id straight onto the canvas. Drag to move,
  drag the corner to resize, drag the background to pan, wheel to zoom; double-click to edit a note or open a
  link or task. Rename a canvas next to its name; **Delete canvas** is in its **⋯** menu. **Make a task** on a note opens the New task dialog with its text. Each item is its own file
  on the board, so two people working on different items merge cleanly.
- **Only turn on plugins you trust.** A plugin can read the board, and although the sandbox blocks the usual
  ways out, it could still send what it read to another site by leaving its page. quest-map stops a plugin
  that does that and warns you, but by then one request may have gone out. The built-in plugins don't.
- **Your own plugins**: put a folder with a `plugin.toml` in `plugins/` next to `custom.css` and reload the
  page. A plugin runs sandboxed: it reads the board and keeps its own data only through the plugin API
  (spec §8.5), can't reach the network or your session, and never edits tasks itself. A plugin that doesn't
  load shows under the ⚠ button.

## Sharing the board with git

To share a board, make the board folder (`~/.quest-map`) the top folder of a git repository with a remote,
and clone it on each machine. quest-map keeps `.gitignore` and `.gitattributes` right for you.

- The header then shows **⇅ Sync** with commits to push (↑) and to pull (↓). Sync commits the board, pulls
  and pushes. **Settings → Board sync on this machine** turns on auto-sync (every few minutes and a minute
  after changes).
- Sync never asks for a password. Push once from a terminal so git has your credentials.
- A board inside a bigger repository is not synced: that would commit and push your other work.
- Changes to different fields or tasks merge on their own. The same field changed on both sides leaves a git
  conflict; the task is flagged and left alone until you fix the file (keep both sides' log lines). The next
  sync commits the fix. A conflicted image needs `git add` after you pick a side.

## What agents do on their own

- Pick the project from the repository they work in, claim the best `todo` task for agents, and keep it in
  `doing` while they work. A claim with no activity for a while shows as stale.
- Add log notes as they go, report their model, tokens and time, and move finished or stuck work to `review`.
- Leave `person` tasks alone, except for notes, and never set `done`.
- Look up the knowledge base before exploring the code, and add pages after.
- File new work they find as new tasks.

You can always edit, move or reassign anything they touched.
