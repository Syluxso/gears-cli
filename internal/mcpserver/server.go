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
		"Cold-start packet. Run first in every session: project, open desk items for this agent, active runs, artifact counts and shape problems, today's session entries, and a suggested next action.",
		verbs.Status)

	add(s, c, "desk_list", "Open desk items addressed to this agent or to anyone. Set all to include closed items.", verbs.DeskList)
	add(s, c, "desk_send", "Place an item on a desk. Type is lifecycle: info closes when read; ask closes when answered; event closes when dismissed; work closes when done; handoff never closes.", verbs.DeskSend)
	add(s, c, "desk_read", "Read one item. Reading an info item consumes it.", verbs.DeskRead)
	add(s, c, "desk_answer", "Close an ask with a reply. The reply is required.", verbs.DeskAnswer)
	add(s, c, "desk_done", "Close a work item.", verbs.DeskDone)
	add(s, c, "desk_dismiss", "Close an event without making it a card.", verbs.DeskDismiss)

	add(s, c, "card_new", "Create a card on this project's board. Refused without a full story (asA, iWant, soThat) and at least one acceptance line, whatever the type. Types: story (default), defect, spike, chore; the type's own section is seeded empty beside them. scenarios and uat are offered, never seeded. Lands in the default state with key <PREFIX>-<n>.", verbs.CardNew)
	add(s, c, "card_list", "The board: states in order with counts, then open cards. Filter by state or type; set all to include completed cards.", verbs.CardList)
	add(s, c, "card_show", "One card by key (GRS-14), bare number, or id, with its sections.", verbs.CardShow)
	add(s, c, "card_update", "Change a card's title, body, type, priority, artifact or externalKey, or its sections. story and acceptance replace; other cardData keys merge. The card must still pass the story-and-acceptance rule.", verbs.CardUpdate)
	add(s, c, "card_move", "Move a card to another state by name. Completed stamps completedAt.", verbs.CardMove)
	add(s, c, "card_delete", "Soft-delete a card. Its number is never reused.", verbs.CardDelete)

	add(s, c, "session_new", "Append one entry to today's session log. Kinds: done, decision, problem, next. Write as the work happens. Returns the entry id, which a runbook step with check=session accepts as evidence.", verbs.SessionNew)
	add(s, c, "session_list", "Session entries from the last N days (default 3), oldest first, every agent folded by time.", verbs.SessionList)

	add(s, c, "artifact_new", "Create an artifact: the row first, then a file seeded from its shape. Prefixes: adr (numbered, never rewritten), story, idea, index, free. The new file fails its shape until its sections are filled; fill it, then artifact_validate.", verbs.ArtifactNew)
	add(s, c, "artifact_list", "All artifacts with tags and shape status. Reconciles rows and files first. Filter by tag.", verbs.ArtifactList)
	add(s, c, "artifact_tag", "Add or remove tags on an artifact. Tags live on the row, never in the file.", verbs.ArtifactTag)
	add(s, c, "artifact_reconcile", "Make rows and files agree: regenerate missing files from rows, adopt files with no row, update rows whose file changed (file wins for body, title, status).", verbs.ArtifactReconcile)
	add(s, c, "artifact_validate", "Check artifacts against the shapes in force. Returns problems per file.", verbs.ArtifactValidate)
	add(s, c, "artifact_shapes", "The shapes in force: required front matter, required sections, minimum body, and whether built-in or a project override.", verbs.Shapes)

	add(s, c, "runbook_list", "Runbooks in this project and the active runs.", verbs.RunbookList)
	add(s, c, "runbook_show", "A runbook's steps, or a run's progress with evidence and verification per step.", verbs.RunbookShow)
	add(s, c, "runbook_new", "Create an empty runbook. The only way to author one; add steps with runbook_step_add.", verbs.RunbookNew)
	add(s, c, "runbook_step_add", "Append a step. Kinds: ask, do, gears, git, gate. Checks by kind: do=commit; gears=session|desk|artifact|card; git=branch|commit|pushed.", verbs.RunbookStepAdd)
	add(s, c, "runbook_start", "Start a run of a runbook. Step 1 lands on this agent's desk as a work item.", verbs.RunbookStart)
	add(s, c, "runbook_step_done", "Complete the current step with evidence. Evidence naming a branch, commit, remote ref, session entry, desk item or artifact is verified against the real thing; failure is an error and the step stays current. Steps with no check are recorded as done, not verified.", verbs.RunStepDone)
	add(s, c, "runbook_skip", "Skip the current step with a reason. The skip stays visible on the run.", verbs.RunSkip)
	add(s, c, "runbook_abandon", "End a run without completing it. Reason required; logged to the session as a problem.", verbs.RunAbandon)

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
