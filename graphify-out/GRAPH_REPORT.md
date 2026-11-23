# Graph Report - quest-map  (2026-11-23)

## Corpus Check
- 156 files · ~180,555 words
- Verdict: corpus is large enough that graph structure adds value.
- Unclassified: 15 file(s) not represented in the graph (top: .toml 7, .css 4, (none) 3)

## Summary
- 2051 nodes · 8328 edges · 122 communities (82 shown, 40 thin omitted)
- Extraction: 86% EXTRACTED · 14% INFERRED · 0% AMBIGUOUS · INFERRED: 1207 edges (avg confidence: 0.85)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `7ecefa72`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- quest-map – Spec & Plan
- builds
- run
- main.mjs
- steps
- Service
- with
- vision/main.mjs
- server.go
- .Claim
- index.Index
- Server
- board.Project
- Open
- errorf
- testing.T
- bridge
- .handler
- github.com/RoyalNoob/quest-map/internal/board.Project
- index.Watcher
- ParseError
- Board
- claude.go
- Syncer
- newFixture
- app.js
- mcpAgent
- t
- board.Project
- Task
- .api
- startWithProject
- .KBWrite
- fixture
- wire
- .Rebuild
- Detector
- .Ledger
- ruleError
- Record
- Task
- kanzo_export.py
- Suite
- Lock
- renderBoard
- Live
- suite_test.go
- autostart_other.go
- .Files
- quest-map
- setView
- adapter.go
- boardsync/sync_test.go
- Kanzo
- time.Duration
- autostart_windows.go
- .refreshLocked
- .suiteFilePath
- .lockState
- .saveConfig
- suiteNow
- net/http.Request
- time.Time
- hub
- el
- .IngestTranscript
- .MCPServer
- git
- quest-map user guide
- Compute
- .check
- install.sh script
- .kbRoutes
- load_tz
- newer
- web/i18n_test.go
- Running
- plugins/plugins.go
- .WriteText
- headerTransport
- api.js
- .termSQL
- LockCache
- cmdUpdate
- FormatTokens
- lore
- .MovedTo
- lockable
- DiffHandEdit
- loadWarnings
- slowProvider
- Release
- quest-map
- .attachmentDir
- .SetSettings
- brief
- apiError
- github.com/RoyalNoob/quest-map

## God Nodes (most connected - your core abstractions)
1. `Server` - 123 edges
2. `Service` - 83 edges
3. `t()` - 81 edges
4. `Index` - 70 edges
5. `Task` - 70 edges
6. `el()` - 52 edges
7. `wire()` - 49 edges
8. `api()` - 45 edges
9. `newFixture()` - 44 edges
10. `Caller` - 43 edges

## Surprising Connections (you probably didn't know these)
- `4.2 Adapters` --references--> `lore`  [INFERRED]
  GAMEDEV_BOARD_SPEC.md → internal/adapter/locks.go
- `4.3 The game-dev suite (`gamedev-unreal`)` --references--> `lore`  [INFERRED]
  GAMEDEV_BOARD_SPEC.md → internal/adapter/locks.go
- `How work flows` --references--> `agent`  [INFERRED]
  docs/guide.md → internal/mcpproxy/e2e_test.go
- `2. Concepts` --references--> `agent`  [INFERRED]
  GAMEDEV_BOARD_SPEC.md → internal/mcpproxy/e2e_test.go
- `3.6 Activity: usage and status changes` --references--> `agent`  [INFERRED]
  GAMEDEV_BOARD_SPEC.md → internal/mcpproxy/e2e_test.go

## Import Cycles
- None detected.

## Communities (122 total, 40 thin omitted)

### Community 0 - "quest-map – Spec & Plan"
Cohesion: 0.09
Nodes (22): How work flows, 10. Tech stack, 11. Plan, 12. Decisions, 13. Open questions, 1. Goals and non-goals, 2. Concepts, 3.1 Layout (+14 more)

### Community 1 - "builds"
Cohesion: 0.08
Nodes (25): archives, format_overrides, formats, name_template, builds, binary, env, flags (+17 more)

### Community 2 - "run"
Cohesion: 0.18
Nodes (21): cmdInstall(), cmdMCP(), cmdUsageHook(), sameDir(), cmdAutostart(), boardRoot(), cmdServe(), cmdExport() (+13 more)

### Community 3 - "main.mjs"
Cohesion: 0.10
Nodes (47): addDays(), bugsPerRelease(), burnUp(), csv(), cumulativeFlow(), cycleTimes(), dayEnd(), dayKey() (+39 more)

### Community 4 - "steps"
Cohesion: 0.08
Nodes (26): jobs, release-check, test, os, name, on, pull_request, push (+18 more)

### Community 5 - "Service"
Cohesion: 0.24
Nodes (5): safeShort(), ArchiveIn, Caller, Service, session

### Community 6 - "with"
Cohesion: 0.09
Nodes (22): GITHUB_TOKEN, permissions, runs-on, steps, jobs, goreleaser, name, on (+14 more)

### Community 7 - "vision/main.mjs"
Cohesion: 0.11
Nodes (45): activate(), add(), addImage(), ask(), cur(), deleteCanvas(), edit(), fail() (+37 more)

### Community 8 - "server.go"
Cohesion: 0.15
Nodes (4): Price, NoArgs, langJSON, milestoneSummary

### Community 9 - ".Claim"
Cohesion: 0.11
Nodes (19): FindOptions, Row, FormatDuration(), ParseDuration(), TestFormatAndParseDuration(), splitList(), dur(), line() (+11 more)

### Community 10 - "index.Index"
Cohesion: 0.14
Nodes (5): Index, ParseProblem, containsStr(), isTaskFileName(), truncRunes()

### Community 11 - "Server"
Cohesion: 0.13
Nodes (4): builtinCatalog(), i18nProblem, pluginList, Server

### Community 12 - "board.Project"
Cohesion: 0.12
Nodes (8): ProjectConfig, cleanSources(), ParsePage(), Slugify(), TestPagesFromTwoCopiesMerge(), TestSlugAvoidsWindowsDeviceNames(), TestSlugify(), ValidSlug()

### Community 13 - "Open"
Cohesion: 0.09
Nodes (30): TestExport(), TestShutdownNeedsTheToken(), TestAgentsMaySetDoneSetting(), TestClaimUnblockedIfRechecksBlockers(), TestUpdateIfChecksUnderTheLock(), Open(), TestOpenKeepsUsersGitFiles(), TestBoardConfigDefaultsAndTimezone() (+22 more)

### Community 14 - "errorf"
Cohesion: 0.20
Nodes (13): Claim, agentCheck(), checkAgentMayChange(), errorf(), notAllowed(), sameClaim(), AdvanceIn, EditIn (+5 more)

### Community 15 - "testing.T"
Cohesion: 0.09
Nodes (45): TestStopWithNoServer(), TestUsageSuffix(), TestCheckBlockers(), TestMovedTaskNotFoundNamesNewID(), TestMoveRetargetsBlockers(), clockFrom(), copyBoard(), TestMeasuredRuleIsPerActivityFile() (+37 more)

### Community 16 - "bridge"
Cohesion: 0.23
Nodes (4): Run(), NewSessionID(), bridge, Options

### Community 17 - ".handler"
Cohesion: 0.15
Nodes (4): errString(), writeJSON(), isUIAsset(), FeedbackIn

### Community 18 - "github.com/RoyalNoob/quest-map/internal/board.Project"
Cohesion: 0.23
Nodes (5): Doer, capitalize(), join(), NewIn, RecordReleaseIn

### Community 21 - "Board"
Cohesion: 0.11
Nodes (9): AgentRules, BoardConfig, TaskDefaults, dirExists(), Board, loadBoardConfig(), TestTrashProject(), boardZone() (+1 more)

### Community 22 - "claude.go"
Cohesion: 0.26
Nodes (9): ClaudeCode, decodeJSON(), editJSON(), HookCommand(), isOurHook(), readFile(), TestInstallClaudeCodeWithoutCLI(), TestInstallUsesClaudeCLI() (+1 more)

### Community 23 - "Syncer"
Cohesion: 0.27
Nodes (6): Status, Syncer, LastWrite(), binary(), netState(), sameDir()

### Community 24 - "newFixture"
Cohesion: 0.06
Nodes (61): gitRepo(), TestCheckedRevIsNotAGitOption(), TestKnowledgeBaseDoneWhen(), TestMissingSourceIsNotFresh(), TestPageKeepsUnknownKeysAndRefusesBrokenFiles(), TestPageNotCheckedAgainstUnlinkedRepo(), TestStaleFilterBeforeLimit(), index_FindAll() (+53 more)

### Community 25 - "app.js"
Cohesion: 0.05
Nodes (49): ALL_TASKS, applyI18n(), applyWords(), boardKey(), cap(), chosenLang(), closeQuickPick(), DEFAULT_FIELDS (+41 more)

### Community 27 - "t"
Cohesion: 0.14
Nodes (46): api(), createPage(), createProject(), deleteProject(), deleteSuiteFile(), editPage(), fileFeedback(), fmtDateTime() (+38 more)

### Community 28 - "board.Project"
Cohesion: 0.15
Nodes (12): Board, Usage, noteRecordText(), readRecords(), signed(), TestNoteRecordTextKeepsCannotContinue(), TestRemoveWithRetry(), removeWithRetry() (+4 more)

### Community 29 - "Task"
Cohesion: 0.22
Nodes (12): field, customNode(), flowMap(), isEmptyNode(), listField(), listNode(), nullNode(), sameValue() (+4 more)

### Community 30 - ".api"
Cohesion: 0.17
Nodes (5): ValidKey(), isUserError(), pluginCSP(), portOf(), apiFunc

### Community 31 - "startWithProject"
Cohesion: 0.10
Nodes (36): newBrowser(), ptr(), sse(), startWithProject(), TestAttachments(), TestBlockedAndBlocks(), TestCloseEndsEventStreams(), TestCrossOriginPostIsRejected() (+28 more)

### Community 32 - ".KBWrite"
Cohesion: 0.11
Nodes (21): PageQuery, PageRow, searchTerms(), Check(), Find(), hasTask(), ParseQuery(), splitWords() (+13 more)

### Community 33 - "fixture"
Cohesion: 0.14
Nodes (35): appendForeign(), editFile(), ownRecords(), recordIDs(), TestEditsWhileWaitingAreRecordedOnce(), TestFailedIndexWriteRecordsNothing(), TestFixingABrokenFileRecordsTheEdit(), TestHandEditIsRecordedAfterTheWait() (+27 more)

### Community 34 - "wire"
Cohesion: 0.14
Nodes (34): applyTheme(), applyView(), archiveNote(), boardFilter(), boardQuery(), closeDrawer(), column(), drawerAction() (+26 more)

### Community 35 - ".Rebuild"
Cohesion: 0.30
Nodes (9): assetName(), deleteTask(), insertActivity(), insertReleases(), insertRepos(), nullStr(), ts(), loadPagesTx() (+1 more)

### Community 36 - "Detector"
Cohesion: 0.07
Nodes (34): Match(), NewLockCache(), lfsRepo(), TestGitLFSLocks(), TestGitLFSNoLoginNoHang(), TestLockCache(), TestLockCacheDetachedAndShared(), TestLoreOffline() (+26 more)

### Community 37 - ".Ledger"
Cohesion: 0.29
Nodes (7): link, 8.1 Local server, 8.2 Feedback intake listener (opt-in), 8.3 UI, 8.4 Insights, 8.5 UI plugins (after v1, M10–M11), 8. HTTP API and web UI

### Community 39 - "Record"
Cohesion: 0.11
Nodes (17): Record, Spent, appendRecords(), appendRecordsNow(), countsUsage(), measuredSessions(), newID(), SumUsage() (+9 more)

### Community 40 - "Task"
Cohesion: 0.07
Nodes (27): Edit, LogEntry, statusRank(), toExport(), laterRun(), birthOrigin(), editList(), IsCustomKey() (+19 more)

### Community 41 - "kanzo_export.py"
Cohesion: 0.16
Nodes (12): default_key(), log_line(), main(), ref(), rewrite(), sub(), _numeric(), parse_time() (+4 more)

### Community 42 - "Suite"
Cohesion: 0.14
Nodes (10): milestoneJSON, projectJSON, Adapters, Field, Intake, Labels, Pipeline, Suite (+2 more)

### Community 44 - "renderBoard"
Cohesion: 0.12
Nodes (37): allFields(), blockedChip(), bulkBar(), bulkPatch(), cardFor(), cardKey(), cellKeys(), customCell() (+29 more)

### Community 45 - "Live"
Cohesion: 0.33
Nodes (4): NameFor(), NewLive(), Live, Registry

### Community 46 - "suite_test.go"
Cohesion: 0.19
Nodes (16): Add(), BuiltinText(), Check(), IsBuiltin(), Load(), TestAdd(), TestBadSuiteFiles(), TestBoardFileExtendsAndDefines() (+8 more)

### Community 47 - "autostart_other.go"
Cohesion: 0.50
Nodes (7): autostartOff(), autostartOn(), autostartStatus(), systemctl(), systemdQuote(), unitFile(), unitPath()

### Community 48 - ".Files"
Cohesion: 0.22
Nodes (8): pathResolver, unrealResolver, cleanSegments(), entryName(), fileExists(), findPlugin(), hasDrive(), uprojectDir()

### Community 49 - "quest-map"
Cohesion: 0.20
Nodes (8): Commands, Credits, Develop, Install, License, quest-map, Release, Start

### Community 50 - "setView"
Cohesion: 0.27
Nodes (11): knownAssignees(), loadPlugins(), mountPlugin(), pluginCatalog(), pluginOn(), pluginText(), pluginViews(), readList() (+3 more)

### Community 52 - "adapter.go"
Cohesion: 0.24
Nodes (7): CheckVCS(), AssetResolver, keys(), LockProviderNamed(), Resolver(), TestPathResolverAndRegistry(), TestUnrealResolver()

### Community 53 - "boardsync/sync_test.go"
Cohesion: 0.62
Nodes (6): New(), clone(), gitT(), repo(), TestBinaryConflictStaysUnresolved(), TestBoardInSubfolderIsOff()

### Community 55 - "time.Duration"
Cohesion: 0.14
Nodes (13): Metrics, row, taskStats, groupNames(), median(), ratio(), cell(), newRateLimiter() (+5 more)

### Community 56 - "autostart_windows.go"
Cohesion: 0.29
Nodes (3): autostartCommand(), autostartOn(), TestAutostartCommandQuoting()

### Community 57 - ".refreshLocked"
Cohesion: 0.43
Nodes (6): fileInfo, isCanonical(), pickKeeper(), readFileRetry(), readTaskFile(), taskFiles()

### Community 58 - ".suiteFilePath"
Cohesion: 0.16
Nodes (6): RemoveFile(), ContentHash(), badRequest(), decode(), suiteFileVersion(), writeSuiteFile()

### Community 59 - ".lockState"
Cohesion: 0.18
Nodes (8): lockLines(), orUnknown(), TestLockLines(), workRoot(), LockOut, LockState, workdir, workdirs

### Community 60 - ".saveConfig"
Cohesion: 0.11
Nodes (10): Milestone, TestWriteFileAtomic(), removeNow(), renameWithRetry(), withRetry(), WriteFileAtomic(), writeFileAtomic(), writeFileAtomicNow() (+2 more)

### Community 61 - "suiteNow"
Cohesion: 0.21
Nodes (20): applyNewTemplate(), applyShow(), createTask(), currentView(), customValues(), deleteView(), discKey(), fieldHelp() (+12 more)

### Community 62 - "net/http.Request"
Cohesion: 0.21
Nodes (3): cleanList(), nonNil(), nonNilMap()

### Community 63 - "time.Time"
Cohesion: 0.10
Nodes (17): Link, Options, pendingEdit, Renumbered, seenMark, parseTS(), scanTranscript(), entry (+9 more)

### Community 64 - "hub"
Cohesion: 0.23
Nodes (5): eventFromChange(), newHub(), removeInfo(), Event, hub

### Community 65 - "el"
Cohesion: 0.17
Nodes (25): closeProjectMenu(), customInputs(), discShow(), el(), fillDrawer(), fillParts(), fillProjectLists(), fillTemplates() (+17 more)

### Community 66 - ".IngestTranscript"
Cohesion: 0.17
Nodes (9): checkTranscriptPath(), clientName(), firstNonEmpty(), pathKey(), subagentFiles(), clientCursor, cursorStore, HookRequest (+1 more)

### Community 67 - ".MCPServer"
Cohesion: 0.20
Nodes (7): add(), withMessage(), ProjectCreateIn, ProjectOut, ProjectsOut, RepoLinkIn, RepoOut

### Community 68 - "git"
Cohesion: 0.15
Nodes (10): gitLFS, git(), Revision(), startDetached(), startDetached(), hideWindow(), hideWindow(), HideWindow() (+2 more)

### Community 69 - "quest-map user guide"
Cohesion: 0.20
Nodes (10): Board, Builds, Insights, Knowledge, Milestones, Plugins and Reports, Projects and Settings, quest-map user guide (+2 more)

### Community 70 - "Compute"
Cohesion: 0.11
Nodes (25): Group, Options, Prices, Query, QueueDay, Report, analyze(), Build() (+17 more)

### Community 71 - ".check"
Cohesion: 0.20
Nodes (7): missing(), TestParseQuery(), TestUnder(), TestValidRev(), under(), checker, diffOut

### Community 84 - "newer"
Cohesion: 0.67
Nodes (3): TestNewer(), newer(), semver()

### Community 87 - "web/i18n_test.go"
Cohesion: 0.44
Nodes (8): catalog(), file(), has(), placeholders(), TestKeysUsedAreInEnglish(), TestNoHardCodedTextInPage(), TestNoHardCodedTextInScript(), TestTranslationsAreComplete()

### Community 88 - "Running"
Cohesion: 0.14
Nodes (23): cmdFind(), cmdReindex(), localIndex(), printRenumbered(), lockFree(), stopServer(), TestStopAsksTheServerToShutDown(), waitLockFree() (+15 more)

### Community 89 - "plugins/plugins.go"
Cohesion: 0.50
Nodes (3): Load(), read(), View

### Community 90 - ".WriteText"
Cohesion: 0.50
Nodes (3): count(), usd(), usdp()

### Community 91 - "headerTransport"
Cohesion: 0.29
Nodes (3): Headers(), headerTransport, hdrTransport

### Community 92 - "api.js"
Cohesion: 0.20
Nodes (4): blobURLs, catalog, listeners, pending

### Community 93 - ".termSQL"
Cohesion: 0.09
Nodes (21): Query, Term, token, isKeyword(), Keys(), Parse(), ParseFields(), searchable() (+13 more)

### Community 94 - "LockCache"
Cohesion: 0.18
Nodes (10): Lock, LockCache, lockCall, lockEntry, LockProvider, cleanLockPath(), parseLFSLocks(), parseLoreLocks() (+2 more)

### Community 95 - "cmdUpdate"
Cohesion: 0.23
Nodes (11): releaseServer(), TestDownloadAndReplace(), TestDownloadRefusesABadChecksum(), TestUpdateCheck(), assetName(), download(), extract(), get() (+3 more)

### Community 96 - "FormatTokens"
Cohesion: 0.67
Nodes (3): FormatTokens(), TestFormatTokens(), trimZero()

### Community 97 - "lore"
Cohesion: 0.20
Nodes (9): lore, 4.1 Suites, 4.2 Adapters, 4.3 The game-dev suite (`gamedev-unreal`), 4.4 Localization, 4.5 Knowledge base (after v1, M9), 4. Project concepts (the only five), loreRemote() (+1 more)

### Community 101 - "lockable"
Cohesion: 0.16
Nodes (14): cmdError(), hasGitLFS(), hasLockable(), lfsEnv(), lockable(), TestHasLockable(), TestLFSEnvKeepsCredentialHelper(), TestLockableNested() (+6 more)

### Community 102 - "DiffHandEdit"
Cohesion: 0.25
Nodes (6): HandEdit, addedLogLines(), DiffHandEdit(), sameClaim(), samePipeline(), TestDiffHandEditFindsInsertedNotesAndMovedFrom()

### Community 103 - "loadWarnings"
Cohesion: 0.38
Nodes (7): connectEvents(), loadWarnings(), pluginEvent(), refreshCounts(), scheduleReload(), warnLink(), withLink()

### Community 105 - "Release"
Cohesion: 0.36
Nodes (3): releasesFile, Release, ReleaseOut

### Community 106 - "quest-map"
Cohesion: 0.25
Nodes (6): Git workflow (standing instruction from the user), graphify, Graphify (keep the code graph current), Project management: quest-map, quest-map, Toolchain

## Knowledge Gaps
- **1 isolated node(s):** `github.com/RoyalNoob/quest-map`
  These have ≤1 connection - possible missing edges or undocumented components. (Counts symbols only; 278 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **40 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `agent` connect `quest-map – Spec & Plan` to `server.go`, `mcpAgent`, `Open`?**
  _High betweenness centrality (0.080) - this node is a cross-community bridge._
- **What connects `github.com/RoyalNoob/quest-map` to the rest of the system?**
  _1 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `quest-map – Spec & Plan` be split into smaller, more focused modules?**
  _Cohesion score 0.09420289855072464 - nodes in this community are weakly interconnected._
- **Why does `Server` connect `Server` to `Service`, `server.go`, `index.Index`, `Open`, `.handler`, `index.Watcher`, `Board`, `Syncer`, `.api`, `startWithProject`, `.KBWrite`, `time.Duration`, `.suiteFilePath`, `net/http.Request`, `hub`, `.MCPServer`, `.kbRoutes`, `Running`, `lockable`?**
  _High betweenness centrality (0.051) - this node is a cross-community bridge._
- **Should `builds` be split into smaller, more focused modules?**
  _Cohesion score 0.07692307692307693 - nodes in this community are weakly interconnected._
- **Should `main.mjs` be split into smaller, more focused modules?**
  _Cohesion score 0.0988235294117647 - nodes in this community are weakly interconnected._
- **Should `steps` be split into smaller, more focused modules?**
  _Cohesion score 0.07977207977207977 - nodes in this community are weakly interconnected._