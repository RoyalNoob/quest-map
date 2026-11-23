# quest-map

Local, single-binary, agent-first task board for game teams (Go). The spec and milestone plan are in
[GAMEDEV_BOARD_SPEC.md](GAMEDEV_BOARD_SPEC.md) – read the relevant section before changing behaviour.

## Project management: quest-map

We track our own work on quest-map (the `quest-map` MCP server), project key `QM`. Tasks were migrated from
Kanzo with the same numbers (`quest-map#12` → `QM-12`); Kanzo is no longer used.
- This repo's remote is linked to `QM`, so tools called from here use it without `project`.
- Milestones: M0 `QM-1`, M1–M6 `QM-6`–`QM-11`, M7 Insights `QM-13`, M8 release `QM-12`, UX pass `QM-19`,
  Localization `QM-23`, M9 knowledge base `QM-25`, M10 UI plugins `QM-26`, M11 vision board `QM-27`.
- Tasks with `doer: person` are the user's (decisions, signing) – notes are fine, don't change them.
- Start a task with `task_claim` (or `task_edit` to `doing`), record progress with `task_note`, and move it to
  `review` with a note on what is done and how to resume; put the commit or PR in `url`. Never set `done`.
  Pass `model` on your first write, and `spent_tokens`/`spent_time` unless a reply says usage is measured.
- New work found along the way becomes a new task, not scope creep in the current one.
- The `RPG` and `PROTO` projects are test data for trying quest-map out; don't track quest-map work there.

## Toolchain

- Go is pinned in `mise.toml`. Run Go through mise: `mise exec -- go …` (Go is not on the global PATH).
- Module: `github.com/RoyalNoob/quest-map`. Package layout follows spec §10.
- Before committing code: `go vet ./...` and `go test ./...` must pass. CI also runs staticcheck and
  `go test -race` on Windows and Linux.
- Targets are Windows and Linux only (amd64 + arm64). No macOS code paths.
- Keep the binary cgo-free (`CGO_ENABLED=0`); use pure-Go deps (e.g. `modernc.org/sqlite`).

## Git workflow (standing instruction from the user)

- **Commit and push automatically** after each finished change – no need to ask. Commit to `main` and
  `git push origin main`.
- Small, focused commits with a clear message; reference the quest-map task, e.g. `M1: atomic task writes (QM-6)`.
- Never force-push, rewrite pushed history, or skip hooks.
- If vet or tests fail, fix them first; don't commit red code.

## Graphify (keep the code graph current)

Before every commit that changes code, refresh the knowledge graph and commit it together with the change:

```sh
graphify update .
git add graphify-out
```

`graphify update` re-extracts code only (no LLM). Use `graphify-out/GRAPH_REPORT.md` and `graphify query` to
orient yourself in the codebase before reading many files.

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- When graphify-out/graph.json exists, run `graphify query "<question>"` before your first grep, search or source-file read for a codebase question, and put this rule in every subagent prompt that explores code. Read raw files only for lines the graph or an earlier read already located. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
- A committed graphify-out/graph.json is rewritten whole on every build, so a large diff to it in a pull request is expected. Do not flag it as churn or suggest skipping the graph rebuild or its hooks.
