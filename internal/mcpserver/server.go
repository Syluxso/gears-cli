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
		"Cold-start packet for the whole workspace. Run first in every session: every project (manifest, projects/ on disk, gears-api when logged in) with tech, git remote, branch, last commit, dirty count, ahead/behind and README line; open desk items for this agent, active runs, artifact counts and shape problems, today's session entries, and a suggested next action.",
		verbs.Status)

	add(s, c, "desk_list", "Open desk items addressed to this agent or to anyone. Set all to include closed items.", verbs.DeskList)
	add(s, c, "desk_send", "Place an item on a desk. Type is lifecycle: info closes when read; ask closes when answered; event closes when dismissed; work closes when done; handoff never closes.", verbs.DeskSend)
	add(s, c, "desk_read", "Read one item. Reading an info item consumes it.", verbs.DeskRead)
	add(s, c, "desk_answer", "Close an ask with a reply. The reply is required.", verbs.DeskAnswer)
	add(s, c, "desk_done", "Close a work item.", verbs.DeskDone)
	add(s, c, "desk_dismiss", "Close an event without making it a card.", verbs.DeskDismiss)

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

	add(s, c, "session_new", "Append one entry to today's session log. Kinds: done, decision, problem, next. Write as the work happens. Returns the entry id (a UUID, the same on every machine) and short (its last 8 hex digits); a runbook step with check=session accepts either as evidence.", verbs.SessionNew)
	add(s, c, "session_list", "Session entries from the last N days (default 3), oldest first, every agent folded by time.", verbs.SessionList)

	add(s, c, "artifact_new", "Create an artifact: the row first, then a file seeded from its shape. Prefixes: adr (numbered, never rewritten), story, idea, index, free. The new file fails its shape until its sections are filled; fill it, then artifact_validate.", verbs.ArtifactNew)
	add(s, c, "artifact_list", "All artifacts with tags and shape status. Reconciles rows and files first. Filter by tag.", verbs.ArtifactList)
	add(s, c, "artifact_tag", "Add or remove tags on an artifact. Tags live on the row, never in the file.", verbs.ArtifactTag)
	add(s, c, "artifact_delete", "Delete an artifact: the row and the file together, so reconcile neither regenerates nor re-adopts it. Works offline; the next sync soft-deletes it on the server and other machines drop row and file. Give the file name, id, or short id.", verbs.ArtifactDelete)
	add(s, c, "artifact_reconcile", "Make rows and files agree: regenerate missing files from rows, adopt files with no row, update rows whose file changed (file wins for body, title, status).", verbs.ArtifactReconcile)
	add(s, c, "artifact_validate", "Check artifacts against the shapes in force. Returns problems per file.", verbs.ArtifactValidate)
	add(s, c, "artifact_shapes", "The shapes in force: required front matter, required sections, minimum body, and whether built-in or a project override.", verbs.Shapes)

	add(s, c, "projects_list", "The workspace's projects from .gears/projects.yaml: id, slug, path (projects/<repo>), git remote, and missing when the repo is gone. current is the project this server runs in. Pass a slug or id as project on session_new or artifact_new to tag work against another one.", verbs.ProjectsList)
	add(s, c, "projects_scan", "Scan <workspace root>/projects/*/ for git repos and record them in .gears/projects.yaml. New repos take the id gears-api already holds for their remote (when logged in), else a fresh one that sync reconciles; repos that are gone are marked missing, never dropped. Only the workspace root has .gears.", verbs.ProjectsScan)
	add(s, c, "sync", "Push local desk, session, artifact and card changes to gears-api, then pull and apply changes from the workspace. Needs gears login first. Cards also sync on their own, at every card write and read.", verbs.Sync)
	add(s, c, "whoami", "Which API host, whether the saved device token works, and the workspace this project syncs to.", verbs.Whoami)
	add(s, c, "export", "Dump the local record (every table, every row) as JSON, or to a file with out. The record is not committed; this is its backup until sync exists.", verbs.Export)

	add(s, c, "runbook_list", "Runbooks in this project, the active runs, the step vocabulary and the rules (one run per agent; owner advances; a run owns its card; evidence is Gears or git objects only).", verbs.RunbookList)
	add(s, c, "runbook_show", "A runbook's steps, or a run's progress: card, branch, evidence and verification per step.", verbs.RunbookShow)
	add(s, c, "runbook_new", "Create an empty runbook with a target (card | repo | none). The only way to author one; add steps with runbook_step_add.", verbs.RunbookNew)
	add(s, c, "runbook_step_add", "Append a step. Kinds: ask, do, gears, git, gate. Checks bind to the run: do=commit|clean; gears=session|desk|artifact|card:<State> (moves the card); git=branch|commit|pushed|merged:<base>|clean; gate=pushed|merged:<base>|card:<State>|desk. name a step so a later when:<name>=<value> can skip on its answer.", verbs.RunbookStepAdd)
	add(s, c, "runbook_step_update", "Edit one step in place: kind, text, check, name or when. Empty string clears check, name or when. Revalidated; refused while a run is active on the runbook.", verbs.RunbookStepUpdate)
	add(s, c, "runbook_step_remove", "Remove a step; later steps renumber. Refused if a later when: refers to it, or while a run is active on the runbook.", verbs.RunbookStepRemove)
	add(s, c, "runbook_step_move", "Move a step to a new position. Refused if a when: would land before the step it refers to.", verbs.RunbookStepMove)
	add(s, c, "runbook_update", "Change a runbook's title or target. The slug stays. Target change is refused if a step's check needs a card.", verbs.RunbookUpdate)
	add(s, c, "runbook_delete", "Delete a runbook definition. Refused while a run is active on it; finished runs keep their copied steps.", verbs.RunbookDelete)
	add(s, c, "runbook_copy", "Copy a runbook into this project: a built-in (see runbook_list.builtins; single-ticket-development is the canonical one) or one from another project's directory. as renames the copy.", verbs.RunbookCopy)
	add(s, c, "runbook_start", "Start a run. For a card runbook, pass the card key: the card is assigned to you and owned by the run. Refused if you already have an active run or the card has one. Step 1 lands on your desk.", verbs.RunbookStart)
	add(s, c, "runbook_step_done", "Perform or verify the current step and advance. gears steps act (log the session entry, move the card); git, do and gate steps verify against the run's branch, card and start time; ask records the answer. Failure is an error and the step stays current. Only the run's agent may call this.", verbs.RunStepDone)
	add(s, c, "runbook_skip", "Skip the current step with a reason. The skip stays visible on the run. Only the run's agent may call this.", verbs.RunSkip)
	add(s, c, "runbook_takeover", "Take another agent's active run, with a reason. Logged to the session and the card; its desk item moves to you.", verbs.RunTakeover)
	add(s, c, "runbook_abandon", "End a run without completing it. Reason required; logged to the session as a problem. The card is released where it stands.", verbs.RunAbandon)

	return s
}

// add registers one verb as a tool. The input struct is the schema; the
// output is returned both as structured content and as pretty JSON text.
func add[In, Out any](s *mcp.Server, c verbs.Ctx, name, desc string, fn func(verbs.Ctx, In) (*Out, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
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

// ServeStdio runs the server over stdin/stdout until the client goes away.
func ServeStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}
