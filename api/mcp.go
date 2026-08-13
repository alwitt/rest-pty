// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"reflect"
	"strings"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/alwitt/rest-pty/workspace"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ======================================================================================
// MCP server instructions
//
// A client MAY fold server instructions into the model's system prompt and MAY ignore them
// entirely, so nothing load-bearing lives in either constant below: every precondition an
// agent must respect is also stated in the description of the tool that enforces it. What
// these carry instead is what no single tool description can - the shape of the interaction
// as a whole, and what a workspace volume is shared with.

// instructionsPreamble the fixed opening section, present on every deployment.
//
// What it spends its words on is what a model is least likely to assume correctly, because
// rest-pty is the opposite of the one-shot tool call an agent is used to: the process outlives
// the call, submitting input returns no result for what was typed, and the container is
// read-only unless the session declared otherwise. That last one bites immediately and
// silently - a program that cannot write its output file just fails, mid-session.
const instructionsPreamble = `rest-pty gives you a persistent terminal. Each session is a
long-lived process running inside its own sandboxed container, and you drive it the way a
person drives a terminal: send keystrokes in, read the screen back. Unlike a one-shot tool
call, the process keeps running between your calls - its working directory, environment, shell
state, and whatever program you left in the foreground are all still there the next time you
look. Use this when a task needs that continuity: an interactive program, a long-running
process you check on, or a sequence of commands that build on each other.

A typical session:
  1. define_new_session          - creates it in the IDLE state
  2. start_session               - IDLE -> READY; the container and process come up
  3. submit_user_input           - e.g. TEXT "ls -l", then ENTER
  4. read_session_output_newest  - read back what happened
  5. repeat 3 and 4 as needed
  6. stop_session, then delete_session

Input and output are separate, asynchronous operations. Submitting input returns as soon as the
session accepts the keystrokes, NOT when the program you triggered has finished; there is no
exit code and no result attached to what you typed. To learn what happened you read the
session's output, and you may need to read more than once - a slow command is still running
when your first read comes back. Input is keystrokes, not commands: send TEXT then ENTER to run
a line, CTRL "C" to interrupt, CTRL "D" for end-of-file. Output comes from a bounded scrollback
buffer that drops its oldest bytes as new output arrives, so read often enough to keep up, and
use the returned actual_offset plus read to know where to continue from.

The state a session is in decides what you can do to it. Input and output need a READY session;
changing a session's own settings - its command, its buffer capacity, its workspace - needs an
IDLE one, as does deleting it.

The container is deliberately restrictive, and the restriction that surprises people is that
almost nothing in it is writable. Its root filesystem is read-only, including the default
working directory, so a program that writes a file will fail unless you declared that path in
writable_dirs when defining the session. Those directories are memory-backed and, like the rest
of the container, are destroyed when the session stops - starting it again gives you a fresh
container, not the one you left. The session also runs as an unprivileged user with no added
capabilities, and has no network access unless you selected a network mode.

Sessions are named, server-wide state that outlives the process that created them and survives
restarts. Stopping a session does not delete it: the definition stays, so it can be started
again. A session you are finished with should be stopped and deleted rather than left behind.`

// instructionsCairn the workspace section, appended only when the cairn integration is
// enabled. On a deployment without it every session naming a workspace refuses to start, so
// advertising the capability there would be actively misleading.
//
// The mount path is interpolated from the constant so this does not become a second definition
// of it - cairn owns that path, and this text has to follow it.
const instructionsCairn = `This deployment is connected to cairn, which provides shared
workspaces. A workspace is a named space that owns two different places bytes can live: a
persistent volume, and a set of durable artifacts.

The persistent volume is a real filesystem shared by everything working in that workspace -
your session, other sessions, and cairn's own file-transfer helpers. Assign a workspace to an
IDLE session with update_session_workspace, or name one when you define the session, and that
volume is mounted read-write at ` + workspace.MountPath + ` the next time the session starts.
That path is the exception to everything said above about the container being disposable: what
you write there outlives the session and is visible to everything else in the same workspace.
Everything outside it is still destroyed when the session stops.

An artifact is a named, durable file belonging to a workspace, held in cairn's object store
rather than on the volume. The volume is scratch and may be reclaimed; the object store is the
record. So: anything that has to survive must be saved as an artifact. cairn moves bytes
between the two on request - downloading an artifact writes it into ` + workspace.MountPath + `
for your session to read, and uploading promotes a file your session produced there into a
durable artifact. If you also hold cairn's own tools, that is what they are for.

The workspace must already exist and have a provisioned volume. You cannot create either; an
operator does. A session assigned a workspace that is missing or has no volume refuses to start
rather than starting without it, so you will not silently lose work you meant to keep there.

One quirk of files cairn placed there for you: a downloaded artifact is owned by root and your
session cannot write to it. You can read it and you can delete it, but you cannot edit it in
place - copy it elsewhere, or delete and rewrite it.`

/*
serverInstructions assemble the server-level instructions returned with the MCP initialize
result.

Unlike multitool's equivalent there is no operator-authored section: rest-pty has one tool set
and one purpose, so the only thing that varies is whether workspaces exist on this deployment.

	@param cairnEnabled bool - whether this deployment can resolve workspaces against cairn
	@returns the assembled instructions
*/
func serverInstructions(cairnEnabled bool) string {
	sections := []string{instructionsPreamble}

	if cairnEnabled {
		sections = append(sections, instructionsCairn)
	}

	return strings.Join(sections, "\n\n")
}

// ======================================================================================
// MCP tool call parameters
//
// These structs mirror the session management REST endpoints in manager_rest.go as MCP
// tool call parameters. Unlike REST, an MCP tool call carries a single JSON object, so a
// request's path parameter (session name), query parameters, and body are flattened into
// one struct per tool. The "block" query parameter is intentionally omitted: a tool call
// is a discrete request/response, so the underlying operations are always performed
// synchronously.

// MCPDockerDriverSettings the docker driver settings an agent is permitted to set
// when defining a session via MCP.
//
// This is a deliberately restricted projection of models.SessionDriverDockerParams. Agent
// sessions are always run in a sandboxed docker container, so only the fields safe for an
// agent to control are exposed here: notably, host bind-mounts (HostMounts), added Linux
// capabilities (AddCapabilities), and the container hardening toggles (read-only rootfs,
// drop-all-capabilities, no-new-privileges, etc.) are NOT exposed and keep their
// hardened defaults. Any field left unset falls back to the default defined on
// models.SessionDriverDockerParams. If a session needs capabilities beyond what this
// allows, an operator can adjust the driver settings out-of-band via the REST API.
type MCPDockerDriverSettings struct {
	// Image container image reference to run
	Image string `json:"image" validate:"required" jsonschema:"container image reference to run"`

	// DisplayRows TTY number of rows (in cells).
	DisplayRows uint16 `json:"display_rows" validate:"gte=30" jsonschema:"TTY number of rows (in cells); must be >= 30"`
	// DisplayCols TTY number of columns (in cells).
	DisplayCols uint16 `json:"display_cols" validate:"gte=80" jsonschema:"TTY number of columns (in cells); must be >= 80"`

	// WorkingDir working directory for the container process; defaults to '/tmp'
	WorkingDir string `json:"working_dir,omitempty" jsonschema:"working directory for the container process; defaults to '/tmp'"`

	// WritableDirs tmpfs mounts providing writable directories within the read-only rootfs
	WritableDirs []goutilsRuntime.InMemoryWritableDir `json:"writable_dirs,omitempty" validate:"omitempty,dive" jsonschema:"tmpfs mounts providing writable directories within the read-only rootfs"`

	// NetworkMode the container network mode (e.g. "none", "bridge"); defaults to 'none'.
	// Must be routable when PublishPorts is set.
	NetworkMode string `json:"network_mode,omitempty" jsonschema:"the container network mode (e.g. \"none\", \"bridge\"); defaults to 'none'. Must be routable when publish_ports is set."`
	// PublishPorts container ports published to the host for inbound connections
	PublishPorts []goutilsRuntime.DockerPortPublish `json:"publish_ports,omitempty" validate:"omitempty,dive" jsonschema:"container ports published to the host for inbound connections"`
	// ExtraHosts additional host-to-IP mappings for the container
	ExtraHosts []goutilsRuntime.ContainerExtraHost `json:"extra_hosts,omitempty" validate:"omitempty,dive" jsonschema:"additional host-to-IP mappings for the container"`
	// Environment additional environment variables for the container process
	Environment []goutilsRuntime.ContainerEnvVar `json:"environment,omitempty" validate:"omitempty,dive" jsonschema:"additional environment variables for the container process"`
}

// ToDriverParams project the restricted agent-facing settings onto the full docker driver
// parameters. Fields not exposed to the agent are left at their zero value so the driver
// applies its hardened defaults (read-only rootfs, all capabilities dropped, no host
// mounts, no-new-privileges, etc.).
func (s MCPDockerDriverSettings) ToDriverParams() models.SessionDriverDockerParams {
	return models.SessionDriverDockerParams{
		ContainerRuntimeParams: goutilsRuntime.ContainerRuntimeParams{
			Image:        s.Image,
			WorkingDir:   s.WorkingDir,
			WritableDirs: s.WritableDirs,
			ExtraHosts:   s.ExtraHosts,
			Environment:  s.Environment,
			Streaming: goutils.GetTypedPtr(goutilsRuntime.StreamIOParams{
				DisplayRows: s.DisplayRows, DisplayCols: s.DisplayCols,
			}),
		},
		NetworkMode:  s.NetworkMode,
		PublishPorts: s.PublishPorts,
	}
}

// MCPDefineNewSessionParams parameters for the define-new-session tool.
//
// Loosely mirrors NewSessionRequest (POST /v1/sessions), but the driver is fixed: an agent
// may only define sessions backed by a sandboxed docker container. There is no driver-type
// selection and no raw driver metadata; the permitted docker settings are given by the
// typed Driver field.
type MCPDefineNewSessionParams struct {
	// Name session name, can only contain alphanumeric characters and -
	Name string `json:"name" validate:"required,session_name_type" jsonschema:"session name, can only contain alphanumeric characters and -"`
	// Description session description
	Description *string `json:"description,omitempty" validate:"omitempty" jsonschema:"session description"`
	// Command the session will operate
	Command models.SessionCommand `json:"command" validate:"required" jsonschema:"the command the session will run"`
	// OutputBufferCapacity buffering capacity for holding command output history
	OutputBufferCapacity int64 `json:"io_buf_cap" validate:"required,gte=16384" jsonschema:"buffering capacity, in bytes, for holding command output history; must be >= 16384"`
	// Driver the sandboxed docker container settings for the session
	Driver MCPDockerDriverSettings `json:"driver" validate:"required" jsonschema:"the sandboxed docker container settings for the session"`
	// WorkspaceName the cairn workspace to assign to the session
	WorkspaceName *string `json:"workspace_name,omitempty" validate:"omitnil,workspace_name_type" jsonschema:"name of the cairn workspace to assign to the session; can only contain alphanumeric characters, - and _. Omit to define the session without a workspace."`
}

// MCPListSessionsParams parameters for the list-sessions tool.
//
// Mirrors the GET /v1/sessions query filters. This is db.SessionQueryFilter directly, so
// the tool exposes the same filtering, pagination, and ordering the core already consumes.
type MCPListSessionsParams = db.SessionQueryFilter

// MCPGetSessionParams parameters for the get-session tool.
//
// Mirrors GET /v1/sessions/{sessionName}.
type MCPGetSessionParams struct {
	// SessionName name of the session to fetch
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to fetch"`
}

// MCPUpdateSessionOutputBufCapacityParams parameters for the
// update-session-output-buffer-capacity tool.
//
// Mirrors PUT /v1/sessions/{sessionName}/output-buf-cap. Only permitted on IDLE sessions.
type MCPUpdateSessionOutputBufCapacityParams struct {
	// SessionName name of the session to update
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to update"`
	// Capacity new output buffer capacity
	Capacity int64 `json:"capacity" validate:"required,gte=16384" jsonschema:"new output buffer capacity, in bytes; must be >= 16384"`
}

// MCPUpdateSessionCommandParams parameters for the update-session-command tool.
//
// Mirrors PUT /v1/sessions/{sessionName}/command. Only permitted on IDLE sessions.
type MCPUpdateSessionCommandParams struct {
	// SessionName name of the session to update
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to update"`
	// Command new command the session runs
	Command models.SessionCommand `json:"command" validate:"required" jsonschema:"the new command the session will run"`
}

// MCPUpdateSessionNameParams parameters for the update-session-name tool.
//
// Mirrors PUT /v1/sessions/{sessionName}/name.
type MCPUpdateSessionNameParams struct {
	// SessionName current name of the session to rename
	SessionName string `json:"session_name" validate:"required" jsonschema:"current name of the session to rename"`
	// NewName new session name, can only contain alphanumeric characters and -
	NewName string `json:"new_name" validate:"required,session_name_type" jsonschema:"new session name, can only contain alphanumeric characters and -"`
}

// MCPUpdateSessionDescriptionParams parameters for the update-session-description tool.
//
// Mirrors UpdateSessionDescriptionRequest (PUT /v1/sessions/{sessionName}/description).
type MCPUpdateSessionDescriptionParams struct {
	// SessionName name of the session to update
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to update"`
	// Description new session description, set to null to clear
	Description *string `json:"description,omitempty" validate:"omitempty" jsonschema:"new session description, set to null to clear"`
}

// MCPUpdateSessionWorkspaceParams parameters for the update-session-workspace tool.
//
// Mirrors UpdateSessionWorkspaceRequest (PUT /v1/sessions/{sessionName}/workspace). Only
// permitted on IDLE sessions.
//
// WorkspaceName carries `omitempty` so it stays OPTIONAL in the emitted schema, which is what
// makes clearing an assignment expressible: a required property has its inferred
// ["null", "string"] type collapsed to a plain "string", and the SDK validates arguments
// against that schema before this tool runs - so neither omitting it nor sending null would
// reach the handler.
//
// The DOCKER-only rule is unreachable from here by construction: define_new_session hard-codes
// the DOCKER driver and there is no MCP tool to change a session's driver. Adding one would
// make this tool able to fail with "not a DOCKER session".
type MCPUpdateSessionWorkspaceParams struct {
	// SessionName name of the session to update
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to update"`
	// WorkspaceName new workspace name; omit or send null to clear the assignment
	WorkspaceName *string `json:"workspace_name,omitempty" validate:"omitnil,workspace_name_type" jsonschema:"name of the cairn workspace to assign; can only contain alphanumeric characters, - and _. Omit or set to null to clear the assignment."`
}

// MCPDeleteSessionParams parameters for the delete-session tool.
//
// Mirrors DELETE /v1/sessions/{sessionName}. Only permitted on IDLE sessions.
type MCPDeleteSessionParams struct {
	// SessionName name of the session to delete
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to delete"`
}

// MCPStartSessionParams parameters for the start-session tool.
//
// Mirrors POST /v1/sessions/{sessionName}/start.
type MCPStartSessionParams struct {
	// SessionName name of the session to start
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to start"`
}

// MCPStopSessionParams parameters for the stop-session tool.
//
// Mirrors POST /v1/sessions/{sessionName}/stop.
type MCPStopSessionParams struct {
	// SessionName name of the session to stop
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to stop"`
}

// ======================================================================================
// Session IO tool call parameters
//
// These mirror the session IO REST handlers in session_io_rest.go (excluding the SSE tail
// stream, which has no MCP equivalent). As with the management tools, a request's path
// parameter and body/query are flattened into a single struct per tool.

// MCPSubmitUserInputParams parameters for the submit-user-command tool.
//
// Mirrors UserCommandRequest (POST /v1/sessions/{sessionName}/io/input/commands). Only
// permitted on READY sessions.
//
// Note: the input-events field is deliberately named "inputs" here rather than "commands"
// (its name on the REST DTO). These are keystroke-level input events fed to the session's
// running process, NOT the {cmd, args} "command" a session is defined to run; keeping the
// names distinct on the agent-facing surface prevents an agent from conflating the two.
type MCPSubmitUserInputParams struct {
	// SessionName name of the session to submit input to
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to submit input to"`
	// Inputs the list of input events to send to the session's running process
	Inputs []models.SessionInputCommand `json:"inputs" validate:"required,gte=1,dive" jsonschema:"the ordered list of input events to send to the session's running process (keystrokes: text, control characters, Enter, or raw bytes); processed in sequence. This is session input fed to STDIN, not the {cmd, args} command the session is defined to run. Must contain at least 1 input."`
}

// MCPReadSessionOutputChunkParams parameters for the read-session-output-chunk tool.
//
// Mirrors GET /v1/sessions/{sessionName}/io/output/chunk. As the buffer only retains the
// most recent bytes, the requested offset may have aged out; in that case the read is
// moved forward to the oldest byte still buffered.
type MCPReadSessionOutputChunkParams struct {
	// SessionName name of the session to read output from
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to read output from"`
	// Offset read index position within the session output stream
	Offset int64 `json:"offset" validate:"gte=0" jsonschema:"byte offset to start reading from within the session output stream; 0 is the start of the stream. To continue reading sequentially, use a prior read's actual_offset + read as the next offset. Must be >= 0. If the offset has aged out of the ring buffer the read is moved forward to the oldest buffered byte (see the returned actual_offset)."`
	// Limit max number of bytes to read
	Limit int `json:"limit" validate:"required,gte=1" jsonschema:"max number of bytes to read; must be >= 1"`
}

// MCPReadSessionOutputNewestParams parameters for the read-session-output-newest tool.
//
// Mirrors GET /v1/sessions/{sessionName}/io/output/newest. Unlike the chunk tool no offset
// is given; the read is anchored to the end of the stream and returns up to "limit" of the
// most recently written bytes.
type MCPReadSessionOutputNewestParams struct {
	// SessionName name of the session to read output from
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to read output from"`
	// Limit max number of bytes to read
	Limit int `json:"limit" validate:"required,gte=1" jsonschema:"max number of bytes to read; must be >= 1"`
}

// ======================================================================================
// MCP tool call responses
//
// Read/query tools return structured output (via the SDK-derived output schema and
// StructuredContent) so the agent sees real fields rather than prose. Action tools
// (start/stop/delete/define/update/submit) do not use these; they return a plain text
// confirmation and signal failure through the handler's error return, which the SDK
// surfaces as CallToolResult.IsError. Unlike the REST DTOs these responses intentionally
// omit goutils.RestAPIBaseResponse: MCP already carries the success/error signal and
// request correlation at the protocol level, so replicating it here would be redundant.

// MCPSession the agent-facing projection of models.Session.
//
// This mirrors models.Session but deliberately omits DriverMetadata: the agent is not
// permitted to inspect or drive the underlying driver settings, so exposing them would be
// both unnecessary and misleading. Omitting the field here also sidesteps a schema/value
// mismatch: models.Session.DriverMetadata is a datatypes.JSON (i.e. []byte), which
// jsonschema-go infers as a "null"/"array" schema, while the marshaled value is the raw
// driver-metadata object — the output validator rejects that discrepancy.
type MCPSession struct {
	// ID PTY session ID
	ID string `json:"id" jsonschema:"PTY session ID"`
	// Name for the session
	Name string `json:"name" jsonschema:"name for the session"`
	// Description an optional description for the session
	Description *string `json:"description" jsonschema:"an optional description for the session"`
	// Command being executed by the session
	Command models.SessionCommand `json:"command" jsonschema:"command being executed by the session"`
	// State of the session
	State models.SessionStateENUMType `json:"state" jsonschema:"state of the session [IDLE, READY]"`
	// DriverType indicate which driver the session uses
	DriverType models.SessionDriverTypeENUMType `json:"driver" jsonschema:"indicate which driver the session uses"`
	// WorkspaceName the cairn workspace assigned to the session
	WorkspaceName *string `json:"workspace_name" jsonschema:"the cairn workspace assigned to the session; null when none is assigned"`
	// OutputBufferCapacity buffering capacity for holding command output history
	OutputBufferCapacity int64 `json:"io_buf_cap" jsonschema:"buffering capacity for holding command output history"`
	// RunnerMode session runner operating mode
	RunnerMode models.SessionRunnerModeTypeENUMType `json:"runner_mode" jsonschema:"session runner operating mode"`
	// CreatedAt entry creation timestamp
	CreatedAt time.Time `json:"created_at" jsonschema:"entry creation timestamp"`
	// UpdatedAt entry update timestamp
	UpdatedAt time.Time `json:"updated_at" jsonschema:"entry update timestamp"`
}

// mcpSessionFrom project a stored models.Session onto the agent-facing MCPSession, dropping
// the driver metadata the agent is not permitted to see.
func mcpSessionFrom(s models.Session) MCPSession {
	return MCPSession{
		ID:                   s.ID,
		Name:                 s.Name,
		Description:          s.Description,
		Command:              s.Command,
		State:                s.State,
		DriverType:           s.DriverType,
		WorkspaceName:        s.WorkspaceName,
		OutputBufferCapacity: s.OutputBufferCapacity,
		RunnerMode:           s.RunnerMode,
		CreatedAt:            s.CreatedAt,
		UpdatedAt:            s.UpdatedAt,
	}
}

// MCPGetSessionResp response for the get-session tool.
type MCPGetSessionResp struct {
	// Session the requested session
	Session MCPSession `json:"session" jsonschema:"the requested session"`
}

// MCPListSessionsResp response for the list-sessions tool.
type MCPListSessionsResp struct {
	// Sessions sessions matching the query filter
	Sessions []MCPSession `json:"sessions" jsonschema:"sessions matching the query filter"`
}

// MCPReadSessionOutputResp structured result for the output-read tools.
//
// The decoded terminal output is ALSO returned to the agent as a text content block; the
// same text is duplicated here in Output so it is delivered regardless of whether a client
// surfaces the text content block or only the structured content. The struct additionally
// carries the positional metadata an agent needs to continue reading (i.e. compute the next
// offset). This value is emitted as the tool's structured content.
type MCPReadSessionOutputResp struct {
	// Output the decoded terminal text (ANSI escape sequences removed). This is the same
	// text returned in the tool result's text content block, duplicated here so clients
	// that surface only structured content still receive it.
	Output string `json:"output" jsonschema:"the decoded terminal text with ANSI escape sequences removed; the same text is also returned as the tool result's text content block"`
	// ActualOffset the offset the returned data actually starts at. When the requested
	// offset has already aged out of the ring buffer, the read is moved forward to the
	// oldest byte still in the buffer, and this reports that position.
	ActualOffset int64 `json:"actual_offset" jsonschema:"the offset the returned data actually starts at; when the requested offset has aged out of the buffer the read is moved forward to the oldest byte still buffered, reported here"`
	// Read number of bytes actually read from the buffer
	Read int `json:"read" jsonschema:"number of bytes actually read from the buffer"`
}

// ======================================================================================
// Main MCP Handler

// MCPHandler MCP request handler
type MCPHandler struct {
	goutils.MCPHandler

	// manage transport-agnostic session management logic
	manage SessionManagerCore

	// io transport-agnostic session IO business logic
	io SessionIOCore
}

/*
NewSessionMCPHandler define a new session MCP request handler

	@param persistence db.Client - DB persistence layer
	@param manager session.Manager - session manager
	@param redisClient goutilsRedis.Client - redis client
	@returns new handler
*/
func NewSessionMCPHandler(
	persistence db.Client,
	manager session.Manager,
	redisClient goutilsRedis.Client,
	logConfig models.HTTPRequestLogging,
) (MCPHandler, error) {
	handler := MCPHandler{
		MCPHandler: goutils.MCPHandler{
			Component: goutils.Component{
				LogTags: log.Fields{"module": "api", "component": "session-mcp-handler"},
				LogTagModifiers: []goutils.LogMetadataModifier{
					goutils.ModifyLogMetadataByRestRequestParam,
					goutils.ModifyLogMetadataByMCPRequestParam,
				},
			},
			LogLevel:        logConfig.LogLevel,
			EnumTypeSchemas: map[reflect.Type]*jsonschema.Schema{},
		},
		manage: SessionManagerCore{
			validate:    validator.New(),
			persistence: persistence,
			manager:     manager,
		},
		io: SessionIOCore{
			validate:    validator.New(),
			persistence: persistence,
			redisClient: redisClient,
		},
	}

	// Register the enumerated schema for every models ENUM that can appear in a tool's input, so
	// the tool schemas advertise the permitted members rather than a bare string. Keep this list
	// in lock-step with the const blocks in the models package.
	goutils.MCPInstallEnumSchema[models.SessionStateENUMType](&handler.MCPHandler)
	goutils.MCPInstallEnumSchema[models.SessionDriverTypeENUMType](&handler.MCPHandler)
	goutils.MCPInstallEnumSchema[models.SessionRunnerModeTypeENUMType](&handler.MCPHandler)
	goutils.MCPInstallEnumSchema[models.SessionInputCommandTypeENUMType](&handler.MCPHandler)

	if err := models.RegisterWithValidator(handler.manage.validate); err != nil {
		return handler, goutils.NewRuntimeError(
			"failed to install custom validation macros", err, true,
		)
	}

	if err := models.RegisterWithValidator(handler.io.validate); err != nil {
		return handler, goutils.NewRuntimeError("failed to install custom validation macros", err, true)
	}

	return handler, nil
}

/*
RegisterTools register all session MCP tools against the given MCP server.

	@param server *mcp.Server - target MCP server
*/
func (h MCPHandler) RegisterTools(server *mcp.Server) error {
	registrations := []func(*mcp.Server) error{
		// Session Management Tools
		h.registerDefineNewSessionTool,
		h.registerListSessionsTool,
		h.registerGetSessionTool,
		h.registerUpdateSessionOutputBufCapacityTool,
		h.registerUpdateSessionCommandTool,
		h.registerUpdateSessionNameTool,
		h.registerUpdateSessionDescriptionTool,
		h.registerUpdateSessionWorkspaceTool,
		h.registerDeleteSessionTool,
		h.registerStartSessionTool,
		h.registerStopSessionTool,
		// Session IO Tools
		h.registerSubmitUserInputTool,
		h.registerReadSessionOutputChunkTool,
		h.registerReadSessionOutputNewestTool,
	}
	for _, register := range registrations {
		if err := register(server); err != nil {
			return err
		}
	}
	return nil
}
