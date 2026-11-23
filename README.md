# quest-map

A local, single-binary, agent-first task board. Agents drive it over MCP; people use a small web UI. It knows
milestones and releases, disciplines, item pipelines, version-control links and feedback intake, tailored per
project by a suite. The first suite is game development with Unreal Engine: builds, asset pipelines, git and
Lore (Perforce later), and playtest feedback.

How to use it: the [user guide](docs/guide.md). The spec and plan are in
[GAMEDEV_BOARD_SPEC.md](GAMEDEV_BOARD_SPEC.md). Targets: Windows and Linux, amd64 +
arm64.

## Install

**With Go (recommended).** Go 1.27.4 or newer builds it on your machine, so Windows SmartScreen never
checks it:

```sh
go install github.com/RoyalNoob/quest-map/cmd/quest-map@latest
```

The binary lands in `$(go env GOPATH)/bin`.

**Without Go.** The installer downloads the latest release, checks it against `checksums.txt` and puts
`quest-map` in `~/.local/bin` (Windows: `%USERPROFILE%\.local\bin`, added to your PATH).

Linux:

```sh
curl -LsSf https://github.com/RoyalNoob/quest-map/releases/latest/download/quest-map-installer.sh | sh
```

Windows, in PowerShell:

```powershell
powershell -ExecutionPolicy Bypass -c "irm https://github.com/RoyalNoob/quest-map/releases/latest/download/quest-map-installer.ps1 | iex"
```

Or download a zip / tar.gz from the [latest release](https://github.com/RoyalNoob/quest-map/releases/latest)
and put `quest-map` on your PATH.

Release files are not code-signed. Instead, GitHub attests each one: it proves the file was built by this
repository's release workflow from the tagged commit. Check a download with:

```sh
gh attestation verify quest-map_windows_amd64.zip --repo RoyalNoob/quest-map
```

Windows may warn about a downloaded `quest-map.exe` the first time it runs; the `go install` build avoids that.

**Update** a release install with `quest-map update` (`--check` only looks). It replaces the binary and
restarts a running server. A `go install` build updates with `go install …@latest`, then `quest-map restart`.

## Start

```sh
quest-map install claude-code   # register the MCP server and the usage hook with Claude Code
quest-map serve                 # board at http://127.0.0.1:47321 (agents start it on their own too)
quest-map autostart on          # start the server at login (Windows Run entry, Linux systemd user unit)
```

The board lives in `~/.quest-map` (`--board DIR` or `$QUEST_MAP_BOARD` for another). Then ask your agent to
"claim a task from quest-map". To share a board, keep that folder in a git repository and use the Sync button
([guide](docs/guide.md#sharing-the-board-with-git)).

## Commands

| Command | Does |
|---|---|
| `quest-map serve` | Web UI + MCP over HTTP; holds the board lock |
| `quest-map stop`, `restart` | Stop the server; start it again in the background (after an update) |
| `quest-map mcp` | stdio MCP proxy that agents launch |
| `quest-map install claude-code` | Register the MCP server and usage hook with Claude Code |
| `quest-map find <filter>` | List tasks matching a filter |
| `quest-map feedback <text>` | File a feedback report (game dev: a playtest note) |
| `quest-map stats` | Insights: how well the agent setup works (`--since`, `--by`, `--csv`) |
| `quest-map export` | The whole board as Markdown (`--format json`, `--project`, `--out FILE`, `--archived`) |
| `quest-map autostart on\|off\|status` | Start the server at login for the current user |
| `quest-map update` | Install the latest release over this binary (`--check`) |
| `quest-map reindex` | Rebuild the SQLite cache |
| `quest-map version` | Print the version |

## Develop

Go is pinned in `mise.toml`:

```sh
mise install
go vet ./...
go test -race ./...
go run ./cmd/quest-map version
```

## Release

Push a tag `vX.Y.Z`. The release workflow runs GoReleaser (`.goreleaser.yaml`): Windows zips and Linux tar.gz
for amd64 and arm64, `checksums.txt`, and the two installers, all with GitHub build attestations. Archive names
have no version (`quest-map_windows_amd64.zip`), so `releases/latest/download/…` links always point at the
newest release. The installers and `quest-map update` need the repository to be public.

## Credits

- [Kanzo](https://github.com/emanuele-em/kanzo) by emanuele-em: the agent-first board that inspired quest-map,
  and the model for its install, attestation and update flow.
- Built on the [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk),
  [modernc.org/sqlite](https://gitlab.com/cznic/sqlite), [go-toml](https://github.com/pelletier/go-toml),
  [go.yaml.in/yaml](https://github.com/yaml/go-yaml), [fsnotify](https://github.com/fsnotify/fsnotify),
  [flock](https://github.com/gofrs/flock) and [GoReleaser](https://goreleaser.com).

## License

[MIT](LICENSE)
