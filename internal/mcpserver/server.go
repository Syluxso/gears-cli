// Package mcpserver exposes the verbs as MCP tools. It is the primary door
// for agents: typed inputs the harness validates, structured results, and
// rules carried in tool descriptions so an agent learns them before the
// first call rather than from the first error.
package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

// New builds a server bound to one project and acting agent.
func New(c verbs.Ctx, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "gears", Version: version}, nil)

	add(s, c, "status",
		"Cold-start packet for the whole workspace. Run first in every session: every project (manifest, projects/ on disk, gears-api when logged in) with tech, git remote, branch, last commit, dirty count, ahead/behind and README line; open desk items for this agent, running runs (gears record, as synced), artifact counts and shape problems, today's session entries, and a suggested next action. agent {label, source (flag|env|workspace|login|default), howToChange} says who you are acting as and how to change it. arch {file, body} is the workspace's architecture doc (arch--primary.md) in full: read it before you design anything; missing with a hint when the workspace has none.",
		verbs.Status)
	add(s, c, "arch", "The workspace's architecture artifact, arch--primary.md, in full: the master design doc (context, boundaries, current shape, open questions). Free-form, one per workspace; edit the file, then artifact_reconcile. status carries the same body. missing with a hint when the workspace has none (artifact_new prefix arch writes the stub).", verbs.Arch)

	add(s, c, "workspace_list", "Your workspaces on gears-api (id, slug, name, prefix; here is this binding's). deleted=true lists your deleted workspaces instead (the ones you own) with deletedAt and deletedBy. Needs gears login.", verbs.WorkspaceList)
	add(s, c, "workspace_delete", "Soft delete a workspace on gears-api (default this one): owners only, confirm must be its slug. Everything in it becomes unreachable everywhere, running runs stop, pending invites are revoked; nothing is erased. This cache keeps its data and refuses reads until a restore. Ask your person first. The same tool as the remote MCP.", verbs.WorkspaceDelete)
	add(s, c, "workspace_restore", "Restore a deleted workspace you own (id or slug from workspace_list deleted=true; default this one). Everything in it is reachable again and this cache reads again; stopped runs and revoked invites stay so. Refused when a live workspace already uses its slug. Then sync.", verbs.WorkspaceRestore)
	add(s, c, "people_list", "The people of this workspace (its members) from gears-api: id, name, email, role and the agent labels their devices use. Cached locally; offline it answers the cached list with stale: true. Call it before desk_send with assignedTo, to turn a name like Bob into his id.", verbs.PeopleList)
	add(s, c, "members_list", "This workspace's members from gears-api (userId, name, email, role owner|member, status) and its pending invites (id, inviteeEmail, role). Live from the API, no local copy: needs gears login and a reachable API; offline it is an error that says so. all includes removed members.", verbs.MembersList)
	add(s, c, "member_invite", "Invite an existing Gears account into this workspace by email (or userId), role member (default) or owner (owners only). The API refuses an email with no account (they must sign up first), someone already a member, or already invited; the error is the API's own words. The invitee accepts from the app's Home or invite_accept.", verbs.MemberInvite)
	add(s, c, "invite_list", "Invites sent to the logged-in account (the person behind this agent): pending ones, any workspace, newest first, with id, workspaceName and role. Accept with invite_accept, decline with invite_decline.", verbs.InviteList)
	add(s, c, "invite_accept", "Accept one of my pending invites (id from invite_list): the account becomes a member of that workspace with the invite's role. Ask the person first; accepting joins their account, not just this agent.", verbs.InviteAccept)
	add(s, c, "invite_decline", "Decline one of my pending invites (id from invite_list).", verbs.InviteDecline)
	add(s, c, "desk_list", "Open desk items for this agent: addressed to it or to another label of the same person, assigned to or made by that person, sent from one of their labels, or addressed to nobody. Items for other people are private to them and never reach this desk. Set all to include closed items.", verbs.DeskList)
	add(s, c, "desk_send", "Place an item on a desk. Type is lifecycle: info closes when read; ask closes when answered; event closes when dismissed; work closes when done; handoff never closes. to is an agent label. For a person (\"make a desk item for Bob\"): call people_list first, then pass that person's id as assignedTo; with to empty the item is for that person and any of their agents. Never guess an id. The author is set by gears-api when the item syncs.", verbs.DeskSend)
	add(s, c, "desk_read", "Read one item. Reading an info item consumes it.", verbs.DeskRead)
	add(s, c, "desk_answer", "Close an ask with a reply. The reply is required.", verbs.DeskAnswer)
	add(s, c, "desk_done", "Close a work item.", verbs.DeskDone)
	add(s, c, "desk_dismiss", "Close an event without making it a card.", verbs.DeskDismiss)
	add(s, c, "desk_clear", "Close open desk items of every type, handoffs included: items addressed to you, or with all every item you can see (the desk holds only items you made, items for you or your labels, and items for anyone).", verbs.DeskClear)

	add(s, c, "card_shapes", "Read before the first card_new: types, the required top-line section per type, seeded and offered sections, every section's fields, sizes, priorities, link kinds.", verbs.CardShapes)
	add(s, c, "card_new", "Create a card on this project's board. Refused unless the type's top-line section and at least one acceptance line are present: story{asA,iWant,soThat}; defect{symptom,expected,actual}; task{what,soThat}; spike{question}; chore{what,why}; epic{outcome}. An empty brief is seeded for the agent. Search with card_list q first; it may exist. Lands in the default state with key <PREFIX>-<n>. Cards sync at once: every card write pushes to gears-api before it returns (the key is the API's) and its result carries sync {pushed, note}; offline it is saved locally and queued, never refused.", verbs.CardNew)
	add(s, c, "card_list", "The board: states in order with counts, tags in use, and open cards (topLine, size, priority, tags, assignee). Closed cards are hidden; each state carries its closed count. Filter by state, type, tag, assignee (me), parent, or q substring; all includes completed and closed cards; closed lists only closed cards. Reads the board as gears-api has it (pulled first, or kept live by gears run); stale: true means the API did not answer and this is the local copy.", verbs.CardList)
	add(s, c, "card_show", "The whole card: sections, blockedBy, comments, links, checklists, children, activity. Read it before picking a card up.", verbs.CardShow)
	add(s, c, "card_update", "Change only the fields sent: title, body, type, size, priority, assignee, parent, dueAt, estimate, artifact, externalKey, story, acceptance, or sections (merged by key). The card must still pass its type's rule. Logged to the card's activity.", verbs.CardUpdate)
	add(s, c, "card_move", "Move a card to a state and position (top, bottom, after:<key>). The terminal state stamps completedAt; a state change stamps stateChangedAt. A closed card moved into a non-terminal state is reopened. A WIP overrun returns a warning.", verbs.CardMove)
	add(s, c, "card_close", "Take a card off the board without deleting it: it keeps its state and trail, card_list and status hide it (card_list closed or all shows it), and the API auto-closes cards 7 days into the terminal state. Optional note lands in the activity. Refused if already closed or a run drives the card.", verbs.CardClose)
	add(s, c, "card_reopen", "Put a closed card back on the board in the state it was in. Moving a closed card into a non-terminal state also reopens it.", verbs.CardReopen)
	add(s, c, "card_claim", "I'm on it: assign the card to this agent and move it from the default state into the next one.", verbs.CardClaim)
	add(s, c, "card_comment", "Add evidence to a card: findings, log excerpts, decisions, what was tried. Comments are the trail; the body is the original text.", verbs.CardCommentAdd)
	add(s, c, "card_tag", "Add or remove tags. A tag names a slice of the board, not a status.", verbs.CardTag)
	add(s, c, "card_link", "Add or remove a link: blocks, blocked-by, related (another card) or ref (a URL). Returns the card's links.", verbs.CardLinkOp)
	add(s, c, "card_checklist", "Checklists a human ticks: add (default title UAT, with items), item, tick, untick, remove.", verbs.CardChecklistOp)
	add(s, c, "card_states", "The swimlanes: list, add, rename, set (wipLimit, default, terminal), reorder, remove (empty states only).", verbs.CardStates)
	add(s, c, "card_activity", "Recent events across the board, newest first: who did what to which card.", verbs.CardActivity)
	add(s, c, "card_delete", "Soft-delete a card. Its number is never reused; children are detached.", verbs.CardDelete)

	add(s, c, "session_new", "Append one entry to today's session log. Kinds: done, decision, problem, next. Write as the work happens. Returns the entry id (a UUID, the same on every machine) and short (its last 8 hex digits); runbook_step_complete accepts either as session evidence (once synced).", verbs.SessionNew)
	add(s, c, "session_list", "Session entries from the last N days (default 3), oldest first, every agent folded by time.", verbs.SessionList)

	add(s, c, "artifact_new", "Create an artifact: the row first, then a file seeded from its shape. Prefixes: adr (numbered, never rewritten), story, idea, index, free, arch (one per workspace, always arch--primary.md, free-form; a second is refused). The new file fails its shape until its sections are filled; fill it, then artifact_validate.", verbs.ArtifactNew)
	add(s, c, "artifact_list", "All artifacts with tags and shape status. Reconciles rows and files first. Filter by tag.", verbs.ArtifactList)
	add(s, c, "artifact_tag", "Add or remove tags on an artifact. Tags live on the row, never in the file.", verbs.ArtifactTag)
	add(s, c, "artifact_delete", "Delete an artifact: the row and the file together, so reconcile neither regenerates nor re-adopts it. Works offline; the next sync soft-deletes it on the server and other machines drop row and file. Give the file name, id, or short id.", verbs.ArtifactDelete)
	add(s, c, "artifact_reconcile", "Make rows and files agree: regenerate missing files from rows, adopt files with no row, update rows whose file changed (file wins for body, title, status).", verbs.ArtifactReconcile)
	add(s, c, "artifact_validate", "Check artifacts against the shapes in force. Returns problems per file.", verbs.ArtifactValidate)
	add(s, c, "artifact_shapes", "The shapes in force: required front matter, required sections, minimum body, and whether built-in or a project override.", verbs.Shapes)

	add(s, c, "projects_list", "The workspace's projects from .gears/projects.yaml: id, slug, path (projects/<repo>), git remote, and missing when the repo is gone. current is the project this server runs in. Pass a slug or id as project on session_new or artifact_new to tag work against another one.", verbs.ProjectsList)
	add(s, c, "projects_scan", "Scan <workspace root>/projects/*/ for git repos and record them in .gears/projects.yaml. New repos take the id gears-api already holds for their remote (when logged in), else a fresh one that sync reconciles; repos that are gone are marked missing, never dropped. Only the workspace root has .gears.", verbs.ProjectsScan)
	add(s, c, "sync", "Push local desk, session, artifact and card changes to gears-api, then pull and apply changes from the workspace. Needs gears login first. Cards also sync on their own, at every card write and read.", verbs.Sync)
	add(s, c, "whoami", "Which API host, whether the saved device token works, the workspace this project syncs to, and the agent label in use: agent {label, source (flag|env|workspace|login|default), howToChange}.", verbs.Whoami)
	add(s, c, "export", "Dump the local record (every table, every row) as JSON, or to a file with out. The record is not committed; this is its backup until sync exists.", verbs.Export)

	add(s, c, "runbook_list", "The official runbooks (byz-orchestrator's catalog through gears-api: each frozen or editable, with its steps and the slugs this workspace adopted it as) and the workspace's adopted copies, as the remote MCP answers; officialAvailable false with a note when the runbook service is not there (adopted copies are the record and always listed; offline the local cache, stale). Locally also drafts (runbook--<slug>.yaml, not runnable), builtins and their vocabulary.", verbs.RunbookList)
	add(s, c, "runbook_show", "One runbook in full, from gears-api (the same tool as the remote MCP): an adopted copy by slug or an official runbook by ref; title, source (adopted|official), frozen/editable, sourceRef, and each step with kind, params and, from the step-kind catalog, its tool, param spec and the evidence kinds that prove it. gears-api unreachable: the cached adopted copy with stale: true. Locally also a runbook--<slug>.yaml draft (source draft) or a cached run (run-...).", verbs.RunbookShow)
	add(s, c, "runbook_abilities", "The step-kind catalog (byz-orchestrator's abilities, through gears-api): every kind a step may take, with its tool, params {name, type, required} and the evidence kinds (session, artifact, commit, desk, card) that prove it. Read it before runbook_edit or inline runbook_start steps. Needs gears login and the runbook service.", verbs.RunbookAbilities)
	add(s, c, "runbook_new", "Create an empty runbook with a target (card | repo | none). The only way to author one; add steps with runbook_step_add.", verbs.RunbookNew)
	add(s, c, "runbook_step_add", "Append a step. Kinds: ask, do, gears, git, gate. Checks bind to the run: do=commit|clean; gears=session|desk|artifact|card:<State> (moves the card); git=branch|commit|pushed|merged:<base>|clean; gate=pushed|merged:<base>|card:<State>|desk. name a step so a later when:<name>=<value> can skip on its answer.", verbs.RunbookStepAdd)
	add(s, c, "runbook_step_update", "Edit one step in place: kind, text, check, name or when. Empty string clears check, name or when. Revalidated.", verbs.RunbookStepUpdate)
	add(s, c, "runbook_step_remove", "Remove a step; later steps renumber. Refused if a later when: refers to it.", verbs.RunbookStepRemove)
	add(s, c, "runbook_step_move", "Move a step to a new position. Refused if a when: would land before the step it refers to.", verbs.RunbookStepMove)
	add(s, c, "runbook_update", "Change a runbook's title or target. The slug stays. Target change is refused if a step's check needs a card.", verbs.RunbookUpdate)
	add(s, c, "runbook_delete", "Delete a runbook definition. Local definition files only.", verbs.RunbookDelete)
	add(s, c, "runbook_copy", "Copy a runbook into this project: a built-in (see runbook_list.builtins; single-ticket-development is the canonical one) or one from another project's directory. as renames the copy.", verbs.RunbookCopy)
	// Official runbooks (GRS-59): adopted into the workspace on gears-api.
	// GRS-73: these and the run tools below have the remote MCP's names and
	// args (testdata/runbook-tools.json); the draft tools above are local only.
	add(s, c, "runbook_adopt", "Adopt an official runbook into the workspace (gears-api). Frozen: a read-only copy whose runs always use the official steps; the authoring tools and runbook_edit refuse it. Editable: the workspace's own copy (default slug <ref>-copy) that runbook_edit changes. Start either with runbook_start runbook=<slug>.", verbs.RunbookAdopt)
	add(s, c, "runbook_unadopt", "Remove an adopted runbook (frozen or editable) from the workspace on gears-api by slug, and from the local cache. Un-adopting is not an edit, so a frozen copy may go; runs already started keep their steps. Needs no orchestrator.", verbs.RunbookUnadopt)
	add(s, c, "runbook_edit", "Change an adopted editable runbook on gears-api: title, description, or the whole step list ({id, kind?, title?, params?} on catalog abilities). A frozen copy is refused.", verbs.RunbookEdit)
	// Runs (GRS-51/56): through gears-api, which checks evidence and calls
	// byz-orchestrator. The local run engine's tools are gone.
	add(s, c, "runbook_start", "Start a runbook run on gears-api (byz-orchestrator drives it; never Temporal from here): an official runbook by its ref directly (ticket-development, sprint-development, audit-branch; no adoption needed), an adopted runbook by its slug, or inline steps. card ties it to a card on the board. Returns the run with step 1 current. Needs gears login.", verbs.RunbookStart)
	add(s, c, "runbook_next", "What to do now on a run, from gears-api (the same tool as the remote MCP; needs no orchestrator). The agent loop: runbook_next, do the work, runbook_step_complete with evidence, repeat until finished. Answers the current step {id, kind, title, params, executor system|agent, ability {title, description, tool, evidenceKinds, requiresEvidence}}, instructions, waitingOn (agent: you; system: the orchestrator does it, wait; human: a person approves on the run page or desk, never complete it yourself), done/total, the next steps' titles, a person's reject or change request (revise, then wait) and systemError (the system could not do it; do it by hand). Omit run for the one running run you own (the label that started it, until runbook_claim moves it); with several they are listed to pick from. gears-api offline: the cached run, stale.", verbs.RunbookNext)
	add(s, c, "runbook_claim", "Take over a running run on gears-api (the same tool as the remote MCP): the label you act as becomes its owner, the session kept working on it (the Stop hook, gears runbook next --hook) and the default run of runbook_next and the step tools. Any member of the workspace may claim; the move is on the run's timeline as run.claimed {from, to}. Claiming a run you own changes nothing (claimed false); a finished run is refused (run.finished). Needs gears login; no orchestrator.", verbs.RunbookClaim)
	add(s, c, "runbook_step_complete", "Complete a run's current step (or the step named) with evidence. gears-api checks every item exists in the workspace (session entry, artifact, desk item, card in the expected state; a commit sha on shape only) before the orchestrator sees it; a refusal leaves the step current. A system step is refused (409 step.system: the orchestrator does it); an approval or gate step too (403 step.needs_person: a person approves it). Sync first so local session entries and artifacts are on gears-api. Omit run for your one running run.", verbs.RunbookStepComplete)
	add(s, c, "runbook_step_skip", "Skip a run's current step (or the step named) with a reason that stays visible on the run. Omit run for your one running run.", verbs.RunbookStepSkip)
	add(s, c, "runbook_stop", "Stop (cancel) a running run on gears-api, with a reason that stays on the run (the same tool as the remote MCP). The current step becomes canceled, the run canceled; later steps stay pending and nothing more can be done on it. A finished run is refused (run.finished). Needs gears login and the runbook service.", verbs.RunbookStop)
	add(s, c, "runbook_history", "A run's timeline from the gears record (the same tool as the remote MCP), oldest first: run.started, each step.completed / step.skipped (with its evidence, note or skip reason), run.completed or run.canceled (with the reason); each with version, at and who (actor, label, via). Needs no orchestrator; gears-api offline: the timeline sync pulled, stale: true.", verbs.RunbookHistory)
	add(s, c, "runbook_status", "One run from the gears record (via gears-api) with each step's status, evidence and who did it; refresh reads the live state from the orchestrator first (stale: true with a note when it cannot). Without run, the workspace's runs newest first (filter status, card). Needs no orchestrator; gears-api offline: the local cache with stale: true.", verbs.RunbookStatus)
	add(s, c, "runbook_runs", "Runs of the workspace from the gears record (the same tool as the remote MCP), newest first, as summaries: runbook, status, current step {id, kind, title, position}, progress done/total, agent, owner, card, startedAt, finishedAt. Filter by status (running|active, completed, failed, canceled, ...), runbook, card, by (the agent label that started it), mine (runs you started) or owner (a label, or me: runs you own); page with limit and cursor (next). Needs no orchestrator; gears-api offline: the local cache with stale: true.", verbs.RunbookRuns)

	return s
}

// add registers one verb as a tool. The input struct is the schema; the
// output is returned both as structured content and as pretty JSON text.
func add[In, Out any](s *mcp.Server, c verbs.Ctx, name, desc string, fn func(verbs.Ctx, In) (*Out, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			if !anyWorkspaceState[name] {
				// GRS-80: a workspace deleted on gears-api refuses every
				// tool but these, with how to restore it.
				if err := c.CheckLive(); err != nil {
					return nil, nil, err
				}
			}
			out, err := fn(c, in)
			if err != nil {
				return nil, nil, err
			}
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
		})
}

// anyWorkspaceState are the tools that work on a workspace marked deleted:
// the workspace tools themselves, sync (which hears a restore) and whoami.
var anyWorkspaceState = map[string]bool{"workspace_list": true, "workspace_delete": true, "workspace_restore": true, "sync": true, "whoami": true}

// ServeStdio runs the server over stdin/stdout until the client goes away.
func ServeStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}
