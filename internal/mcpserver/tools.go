package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Headers the stdio proxy adds to every request it forwards.
const (
	HeaderSession       = "X-Quest-Map-Session"        // a short random id for this agent session
	HeaderCwd           = "X-Quest-Map-Cwd"            // the agent's working folder
	HeaderClientSession = "X-Quest-Map-Client-Session" // the client's own session id, if it has one
)

// callerOf builds the Caller for a tool request from the proxy's headers and the MCP session.
func (s *Service) callerOf(req *mcp.CallToolRequest) Caller {
	var h map[string][]string
	if req.Extra != nil {
		h = req.Extra.Header
	}
	get := func(k string) string {
		for key, v := range h {
			if strings.EqualFold(key, k) && len(v) > 0 {
				return v[0]
			}
		}
		return ""
	}
	client, version := "agent", ""
	if req.Session != nil {
		if ip := req.Session.InitializeParams(); ip != nil && ip.ClientInfo != nil {
			client, version = ip.ClientInfo.Name, ip.ClientInfo.Version
		}
	}
	short := safeShort(get(HeaderSession))
	if short == "" && req.Session != nil {
		short = s.shortFor(req.Session.ID())
	}
	c := Caller{ID: clientName(client) + ":" + short, Client: clientName(client), ClientSession: get(HeaderClientSession), Cwd: get(HeaderCwd)}
	if version != "" {
		c.Client += "/" + version
	}
	return c
}

// safeShort keeps a session id from a header to letters and digits, at most 32 of them.
func safeShort(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// shortFor gives an MCP session without proxy headers a stable short id.
func (s *Service) shortFor(mcpSession string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shortIDs == nil {
		s.shortIDs = map[string]string{}
	}
	if id, ok := s.shortIDs[mcpSession]; ok {
		return id
	}
	id := NewSessionID()
	s.shortIDs[mcpSession] = id
	return id
}

// NewSessionID returns a short random id for an agent session (6 hex digits).
func NewSessionID() string {
	var b [3]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// add registers one tool whose handler gets the Caller and returns reply text plus structured data.
//
// The reply text is sent twice: as the text content, and as a "message" field of the structured content.
// Clients that show only the structured content (Claude Code does, when there is any) would otherwise never
// see the lines that exist only in the text – "nothing to claim", the usage note, "Updates:", warnings –
// nor the session marker that links measured usage (usage.go).
func add[In, Out any](srv *mcp.Server, s *Service, name, desc string, fn func(Caller, In) (string, Out, error)) {
	schema, err := jsonschema.For[Out](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %s: output schema: %v", name, err))
	}
	if schema.Properties == nil {
		schema.Properties = map[string]*jsonschema.Schema{}
	}
	schema.Properties[messageField] = &jsonschema.Schema{Type: "string",
		Description: "the reply as text: what happened, notes, warnings and updates from others; read it"}
	tool := &mcp.Tool{Name: name, Description: desc, OutputSchema: schema}
	mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		c := s.callerOf(req)
		text, out, err := fn(c, in)
		if err != nil {
			return nil, nil, fmt.Errorf("%w\n%s%s", err, SessionMarker, c.ID)
		}
		// The marker line links the client's transcript to this session (usage.go).
		text = strings.TrimRight(text, "\n") + "\n" + SessionMarker + c.ID
		msg := text
		if b, ok := any(out).(brief); ok {
			// The structured fields already hold the reply's data; the message adds only the rest.
			msg = join(b.briefMessage(), SessionMarker+c.ID)
		}
		structured, err := withMessage(out, msg)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, structured, nil
	})
}

const messageField = "message"

// brief is a structured reply whose text mostly repeats its own fields (task_show, task_find). Its message is
// only briefMessage – notes, updates – plus the session marker, so a client does not get the same data twice.
type brief interface{ briefMessage() string }

// withMessage returns out as a JSON object with the reply text added under messageField.
func withMessage(out any, text string) (map[string]any, error) {
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("structured reply is not a JSON object: %w", err)
	}
	m[messageField] = text
	return m, nil
}

// NoArgs is the input of tools without arguments.
type NoArgs struct{}

// MCPServer returns an MCP server with the board's tools. The same server serves every session.
func (s *Service) MCPServer(version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "quest-map", Version: version}, &mcp.ServerOptions{Instructions: Instructions})
	add(srv, s, "project_list", "List projects with open task counts; marks the project linked to your working folder.",
		func(c Caller, _ NoArgs) (string, ProjectsOut, error) { return s.ProjectList(c) })
	add(srv, s, "project_create", "Create a project (key like RPG), optionally linked to a git repository.", s.ProjectCreate)
	add(srv, s, "repo_link", "Link a git remote (any URL form) to a project, so tools pick that project in its working folders.", s.RepoLink)
	add(srv, s, "repo_unlink", "Remove a git remote's link to its project.", s.RepoUnlink)
	add(srv, s, "task_find", "List tasks matching a filter (default: open tasks of your folder's project). Filter terms: status:, doer:, assignee:, discipline:, milestone:, label:, priority:, asset:, model:, blocked:yes|no, stale:yes|no, spent:yes|no, found:latest, free words; '-' negates.", s.Find)
	add(srv, s, "task_show", "Show a task in full: fields, body, log, usage spent, warnings.", s.Show)
	add(srv, s, "task_claim", "Claim the best unblocked todo task meant for agents (by priority, milestone date, age) and start it: it becomes doing and yours. Optional discipline/milestone/filter narrow the choice.", s.Claim)
	add(srv, s, "task_new", "Create a task.", s.New)
	add(srv, s, "task_edit", "Change task fields; status changes are logged. Moving out of doing clears your claim. Pass model/spent_tokens/spent_time to report usage.", s.Edit)
	add(srv, s, "task_note", "Add a line to a task's log: progress, findings, decisions. Pass model/spent_tokens/spent_time to report usage (recommended).", s.Note)
	add(srv, s, "task_release", "Give a claimed task back: claim cleared, status todo, with a note. Other sessions' claims can only be released when stale.", s.Release)
	add(srv, s, "task_archive", "Move a done task to the archive.", s.Archive)
	add(srv, s, "task_move", "Move a task to another project; it gets a new id there.", s.Move)
	add(srv, s, "pipeline_advance", "Move an item task (game dev: an asset) to the next step of its pipeline, or to a named step; logged on the task.", s.Advance)
	add(srv, s, "release_record", "Record a release (game dev: a build) with its target (platform), revision and notes, so tasks can say found_in / fixed_in.", s.RecordRelease)
	add(srv, s, "kb_find", "Search the knowledge base – answers earlier agents found – before exploring the code. Free words plus tag:, source:, stale:yes|no terms; scope board for every project. Each page says whether it is fresh for your working copy.", s.KBFind)
	add(srv, s, "kb_show", "Read a knowledge base page: its answer, sources, and whether it is fresh, stale (with the changed files) or unknown.", s.KBShow)
	add(srv, s, "kb_write", "Create or update a knowledge base page after an answer that took real exploring; sources are the repo files and tasks you checked. recheck: true marks a stale page as still holding; retired hides one.", s.KBWrite)
	add(srv, s, "insights", "Read-only: how well the agent setup works on a project (or one task) – first-pass acceptance, rework, reopened, cost per accepted task, abandonment, review wait, cycle time, coverage – agent tasks next to person tasks, optionally grouped by model, client, discipline, label or milestone.", s.Insights)
	add(srv, s, "feedback_report", "File a feedback report (game dev: a playtest note) as a backlog task with the project's intake defaults; optional release id and screenshot.", s.Feedback)
	return srv
}
