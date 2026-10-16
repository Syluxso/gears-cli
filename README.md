# gears-cli

The Gears CLI: the local door to a Gears project. Binds a directory to a project, keeps shaped artifacts beside the code, runs the desk and session log, drives runbooks, and serves every verb as an MCP tool over stdio (`gears run`).

Binary name is `gears`. Module `github.com/Syluxso/gears-cli`.

Install (the binary name differs from the module name, so `go install` alone
would produce `gears-cli`):

    go build -o "$(go env GOPATH)/bin/gears" .

Then at a workspace root (the directory holding `projects/<repo>`): `gears init`
(or `gears init --name <agent>` without a terminal), `gears status`. The root is the only `.gears/`; init records the git repos
under `projects/` in `.gears/projects.yaml` (`gears projects scan` to refresh,
`gears projects list` to print). Commands run inside `projects/<repo>` resolve
to the root and tag session entries and new artifacts with that project;
`gears init` there is refused. A standalone clone with no workspace above it
still runs `gears init` on its own: `gears init` in a directory with no `.gears`
above it is how a workspace is created. Outside any workspace, other commands
say "not a Gears workspace". A nested `.gears` left from an older version is
ignored (the outermost wins) with one warning naming it, so it can be deleted. For an MCP harness, point it at
`gears run --dir <workspace root>` with `GEARS_AGENT` set to the agent's label.

`gears init` first asks for the agent's name (GRS-63): the string the agent is
known by in this workspace. Desk items are addressed to it and sent from it,
and session log entries and card activity carry it. It takes `--name <label>`,
else `-a`, else `GEARS_AGENT`; a name saved by an earlier init is kept without
asking (only `--name` replaces it); otherwise it prompts, suggesting the login
label. Without a terminal and without a name it stops before creating
anything, so scripts and agents run `gears init --name <label>`. Names are 1 to
64 of `a-z 0-9 - _ . +`. The name is saved for this workspace on this machine
in `.gears/.gearbox/agent` (never committed).

The agent label (GRS-21) comes from, strongest first: `gears -a <name>`, then
`GEARS_AGENT` (for an MCP server, its `env` in `.mcp.json`), then the name
`gears init` saved for the workspace, then the label saved by
`gears login --label <name>` for the API host, else `local`.
`gears status` and `gears whoami` print the label, where it came from and how
to change it; their JSON (and the MCP tools) carry
`agent: {label, source, sourceText, howToChange}` with source `flag`, `env`,
`workspace`, `login` or `default`.

Upgrading from gears v2 (GRS-32): uninstall the old `gears`, install this one,
then at the old workspace root:

    gears migrate --dry-run      # print the plan, change nothing
    gears migrate                # or: gears migrate <dir>

It renames `.gears` to `.gears_v2` (kept, for restoring), writes
`.gears_v2.zip` (an archive of it: store it or delete it), runs `gears init`
(and `gears projects scan` when `projects/` exists), and copies each v2
markdown file into `.gears/artifacts/` as a free artifact,
`free--migrated--<kind>-<name>.md` (so no v3 shape checks apply), body kept,
with `title`, `date` and `migrated_from` added to its front matter. The v2
kind starts the name:

| v2 file                                        | name starts |
|------------------------------------------------|-----------|
| `artifacts/adr--*.md`, `artifacts/adr_*.md`, `decisions/*.md` | `adr` |
| `story/story--*.md`, `story/*.md`              | `story`   |
| `*/idea--*.md`                                 | `idea`    |
| every `index.md` (name: its folder, root is `workspace`) | `index` |
| anything else (`sessions/*.md`, `memory/**`, `artifacts/notes.md`, ...) | its folder, e.g. `sessions-` (none for `artifacts/`) |

Not migrated, left in `.gears_v2`: `.gearbox/` (config, `gears.db`,
hydrate.md), v2's `README.md` and `gears-init.md`, `adr_template.md`, the
shipped `adr_example-*.md`, `sessions/hydrate-report-*.md`, and non-markdown
files. Name clashes get `-2`, `-3`. Re-running is safe: names already in
`.gears/artifacts/` are kept and counted. It refuses inside a v3 workspace,
where there is no v2 `.gears`, or in a v3 workspace without `.gears_v2`.
Then: `gears login`, `gears sync`.

Account and sync (slice 1):

    gears login --email you@example.com      # device token to ~/.gears/credentials.json,
                                             # then registers this workspace/project, reports pulls
    gears sync                               # push desk, session, artifact changes; pull and apply
    gears whoami                             # host, token state, workspace this syncs to
    gears logout                             # revoke the device, forget the token
    gears watch                              # follow the board live, one line per change

Cards sync immediately (GRS-51), unlike desk, sessions and artifacts:

- Every card write (CLI or MCP: new, update, move, claim, comment, tag,
  link, checklist, close, reopen, delete, states) pushes the board's
  pending rows at once (3s timeout) and takes the API's number and key, so
  `gears card new` prints the key the API gave. Offline, not logged in, or
  an API error: the write stands locally, one `gears: saved locally; not
  pushed (...)` line on stderr (a `sync: {pushed: false, note}` field in the
  MCP result), and the rows go up with the next write or `gears sync`.
- `gears run` holds the workspace's live board stream open
  (`board/events`, device token) and pulls the card tables a moment after
  each event; `gears watch` does the same in the foreground. Reconnects with
  backoff (3s to 5m); stops for good, logged once on stderr, when the API
  has no live board (404/405/501) or refuses the token (401/403).
- Card reads (`card list`, `card show`, the cards in `status`, and their MCP
  tools) pull the card tables first (1.5s timeout) unless the stream is
  open; on failure they answer from the cache with `"stale": true`.
- `gears run --sync-interval N` (GRS-25) also runs a full `gears sync`
  (desk, sessions, artifacts, cards) once at startup and then every N
  minutes; 0, the default, is off. `GEARS_SYNC_INTERVAL=N` sets it from the
  harness env (`.mcp.json` `env`); the flag wins. One line per sync on
  stderr (`gears sync: pushed N, pulled M`), never stdout. It shares the
  process's sync lock with the live board and card writes, so syncs never
  overlap (a card write waits within its 3s budget, else stays queued). Not
  logged in or offline: one note, then quiet skips until that changes.
  Stops with the server.
- Card-only pulls use `GET sync?tables=cards` and their own `cards_cursor`
  (the main cursor is the full sync's); an API that ignores `tables` gets a
  full pull instead. Needs one `gears sync` first. `GEARS_CARD_SYNC=off`
  turns all of this off.

`GEARS_API` or `--api` picks the host (default api.gearsos.com). `.gears/` is
never committed: project.yaml is the binding, the rest is cache; the record is
gearsos.com. A fresh clone binds by its git remote on first login.
`scripts/e2e-sync.sh` proves the round trip against a local gears-api.

Sibling repos:

- [gears-api](https://github.com/Syluxso/gears-api) — Go / Gin / byz-platform
- [gears-app](https://github.com/Syluxso/gears-app) — post-auth Angular app
- [gears-front](https://github.com/Syluxso/gears-front) — pre-auth Next.js site

Earlier generations of Gears live in `Syluxso/gears`; nothing from them is carried over.

Runbook runs (GRS-51, GRS-56, GRS-58) live in byz-orchestrator behind
gears-api; the local run engine is retired. The CLI and `gears run` call
gears-api only, never the orchestrator. `gears sync` pulls runs (table
`runs`) and adopted copies (table `runbooks`) into the local cache that
`gears status` and the offline fallbacks read.

**One runbook toolset (GRS-73).** The local MCP and the remote MCP expose
the same runbook tools with the same names, required args and args
(`internal/mcpserver/testdata/runbook-tools.json`, identical to gears-api's
`testdata/runbook-tools.json`; `TestRunbookToolParity` in each repo holds
the server to it):

| MCP tool | CLI | |
|----------|-----|-|
| `runbook_list` | `gears runbook list` | official runbooks (frozen or editable) and the adopted copies; `officialAvailable: false` and a note without the runbook service. Locally also `drafts`, `builtins`, `vocabulary` |
| `runbook_show {runbook}` | `gears runbook show <slug\|ref>` | one runbook in full: source, frozen/editable, steps with kind, params, tool, param spec and evidence kinds from the catalog (GRS-70). Locally also a draft or a cached `run-...` |
| `runbook_abilities` | `gears runbook abilities` | the step-kind catalog (byz-orchestrator's `GET /abilities`) |
| `runbook_adopt {ref, as?}` | `gears runbook adopt <ref> [--as]` | copy an official runbook into the workspace |
| `runbook_edit {runbook, ...}` | `gears runbook edit <slug> [--title] [--description] [--steps-file]` | change an editable copy (a frozen one is refused) |
| `runbook_unadopt {runbook}` | `gears runbook unadopt <slug>` | remove an adopted copy, frozen or editable (GRS-73) |
| `runbook_start {runbook, cardKey?, steps?, idempotencyKey?}` | `gears runbook start <name> [--card] [--step id[:kind]] [--key]` | start a run: an adopted slug, a registered runbook, or inline steps |
| `runbook_step_complete {run?, step?, evidence?, note?}` | `gears runbook complete [kind:ref ...] [--run] [--step] [--note]` | complete a step with evidence (`session:<id\|short>`, `artifact:<file\|id>`, `desk:<id>[:state]`, `card:<KEY>[:<State>]`, `commit:<sha>`); gears-api checks each exists first, so sync first |
| `runbook_step_skip {reason, run?, step?, note?}` | `gears runbook skip --reason` | skip a step with a reason |
| `runbook_stop {run, reason}` | `gears runbook stop <run> --reason` | stop (cancel) a running run; the current step and the run become canceled (GRS-69) |
| `runbook_status {run?, refresh?, status?, card?}` | `gears runbook status [run] [--refresh]` | one run from the record (refresh asks for the live state; `stale: true` with a note when it cannot), or the list |
| `runbook_runs {status?, runbook?, card?, by?, mine?, limit?, cursor?}` | `gears runbook runs [--mine] [--by] ...` | runs as summaries: current step, progress, agent, card, started/finished, `next` (GRS-71) |

`run` left out on the step tools is your one running run. Every tool takes
an optional `agent` (the label to act as). Kept local only, because they
author `runbook--<slug>.yaml` drafts in the retired local vocabulary and
do not clash with a shared name: `runbook_new`, `runbook_copy`,
`runbook_update`, `runbook_delete`, `runbook_step_add`,
`runbook_step_update`, `runbook_step_remove`, `runbook_step_move`
(`gears runbook new|copy|update|delete|step ...`). They refuse an adopted
slug. The local engine's tools are gone; `runbook_list` also carries the
official catalog.

**Without the orchestrator** (GRS-71) every read answers from the gears
record: `runbook_runs`, `runbook_status`, `runbook_show` of an adopted copy,
`runbook_list`'s adopted copies, `runbook_unadopt`. Only start, complete,
skip, refresh, the official catalog and the abilities need it. gears-api
offline: the local cache, `stale: true`. `gears status` lists my running
runs first (`myRuns`, each with its current step).
