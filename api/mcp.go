// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"reflect"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		DockerRuntimeParams: goutilsRuntime.DockerRuntimeParams{
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
		},
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
	Description *string `json:"description" validate:"omitempty" jsonschema:"new session description, set to null to clear"`
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
