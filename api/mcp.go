// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
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
	DisplayRows uint16 `json:"display_rows" validate:"gte=30" jsonschema:"TTY number of rows (in cells)"`
	// DisplayCols TTY number of columns (in cells).
	DisplayCols uint16 `json:"display_cols" validate:"gte=80" jsonschema:"TTY number of columns (in cells)"`

	// WorkingDir working directory for the container process; defaults to '/tmp'
	WorkingDir string `json:"working_dir,omitempty" jsonschema:"working directory for the container process; defaults to '/tmp'"`

	// WritableDirs tmpfs mounts providing writable directories within the read-only rootfs
	WritableDirs []models.ContainerTmpfsMount `json:"writable_dirs,omitempty" validate:"omitempty,dive" jsonschema:"tmpfs mounts providing writable directories within the read-only rootfs"`

	// NetworkMode the container network mode (e.g. "none", "bridge"); defaults to 'none'.
	// Must be routable when PublishPorts is set.
	NetworkMode string `json:"network_mode,omitempty" jsonschema:"the container network mode (e.g. \"none\", \"bridge\"); defaults to 'none'. Must be routable when publish_ports is set."`
	// PublishPorts container ports published to the host for inbound connections
	PublishPorts []models.ContainerPortPublish `json:"publish_ports,omitempty" validate:"omitempty,dive" jsonschema:"container ports published to the host for inbound connections"`
	// ExtraHosts additional host-to-IP mappings for the container
	ExtraHosts []models.ContainerExtraHost `json:"extra_hosts,omitempty" validate:"omitempty,dive" jsonschema:"additional host-to-IP mappings for the container"`
	// Environment additional environment variables for the container process
	Environment []models.ContainerEnvVar `json:"environment,omitempty" validate:"omitempty,dive" jsonschema:"additional environment variables for the container process"`
}

// ToDriverParams project the restricted agent-facing settings onto the full docker driver
// parameters. Fields not exposed to the agent are left at their zero value so the driver
// applies its hardened defaults (read-only rootfs, all capabilities dropped, no host
// mounts, no-new-privileges, etc.).
func (s MCPDockerDriverSettings) ToDriverParams() models.SessionDriverDockerParams {
	return models.SessionDriverDockerParams{
		Image:        s.Image,
		DisplayRows:  s.DisplayRows,
		DisplayCols:  s.DisplayCols,
		WorkingDir:   s.WorkingDir,
		WritableDirs: s.WritableDirs,
		NetworkMode:  s.NetworkMode,
		PublishPorts: s.PublishPorts,
		ExtraHosts:   s.ExtraHosts,
		Environment:  s.Environment,
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
	Command models.SessionCommand `json:"command" validate:"required" jsonschema:"the command the session will operate"`
	// OutputBufferCapacity buffering capacity for holding command output history
	OutputBufferCapacity int64 `json:"io_buf_cap" validate:"required,gte=16384" jsonschema:"buffering capacity for holding command output history"`
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
	Capacity int64 `json:"capacity" validate:"required,gte=16384" jsonschema:"new output buffer capacity"`
}

// MCPUpdateSessionCommandParams parameters for the update-session-command tool.
//
// Mirrors PUT /v1/sessions/{sessionName}/command. Only permitted on IDLE sessions.
type MCPUpdateSessionCommandParams struct {
	// SessionName name of the session to update
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to update"`
	// Command new command the session runs
	Command models.SessionCommand `json:"command" validate:"required" jsonschema:"new command the session runs"`
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

// MCPSubmitUserCommandParams parameters for the submit-user-command tool.
//
// Mirrors UserCommandRequest (POST /v1/sessions/{sessionName}/io/input/commands). Only
// permitted on READY sessions.
type MCPSubmitUserCommandParams struct {
	// SessionName name of the session to submit commands to
	SessionName string `json:"session_name" validate:"required" jsonschema:"name of the session to submit commands to"`
	// Commands the list of commands to send to the session
	Commands []models.SessionInputCommand `json:"commands" validate:"required,gte=1,dive" jsonschema:"the list of commands to send to the session"`
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
	Offset int64 `json:"offset" validate:"gte=0" jsonschema:"read index position within the session output stream"`
	// Limit max number of bytes to read
	Limit int `json:"limit" validate:"required,gte=1" jsonschema:"max number of bytes to read"`
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
	Limit int `json:"limit" validate:"required,gte=1" jsonschema:"max number of bytes to read"`
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

// MCPReadSessionOutputResp structured chaining metadata for the output-read tools.
//
// The decoded terminal output is returned to the agent as a text content block; this
// struct carries only the positional metadata an agent needs to continue reading (i.e.
// compute the next offset), and is emitted as the tool's structured content.
type MCPReadSessionOutputResp struct {
	// ActualOffset the offset the returned data actually starts at. When the requested
	// offset has already aged out of the ring buffer, the read is moved forward to the
	// oldest byte still in the buffer, and this reports that position.
	ActualOffset int64 `json:"actual_offset" jsonschema:"the offset the returned data actually starts at; when the requested offset has aged out of the buffer the read is moved forward to the oldest byte still buffered, reported here"`
	// Read number of bytes actually read from the buffer
	Read int `json:"read" jsonschema:"number of bytes actually read from the buffer"`
}

// ======================================================================================
// ENUM schema support
//
// The go-playground "validate" tags on the parameter structs are enforced by the cores at
// call time, but they do not describe the allowed ENUM values in the tool's JSON schema.
// jsonschema-go infers an ENUM-typed (string) field as a bare "string" with no enumeration,
// so the agent would not learn the permitted values from the schema alone. To advertise them
// we build the input schema explicitly for tools carrying ENUM fields, supplying a per-type
// schema (with the Enum populated from each type's Values() method) via ForOptions.TypeSchemas.

// mcpBuildEnumSchema build a string JSON schema whose enum is populated from the
// given ENUM values.
func mcpBuildEnumSchema[T ~string](values []T) *jsonschema.Schema {
	enum := make([]any, len(values))
	for i, v := range values {
		enum[i] = string(v)
	}
	return &jsonschema.Schema{Type: "string", Enum: enum}
}

// mcpEnumTypeSchemas the per-type schemas for every models ENUM that can appear in a tool's
// input, keyed by Go type. Handed to jsonschema-go so ENUM fields emit a proper enumeration
// rather than a bare string. Each type's members come from its Values() method, keeping this
// map in lock-step with the const blocks in the models package.
var mcpEnumTypeSchemas = map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[models.SessionStateENUMType]():          mcpBuildEnumSchema(models.SessionStateENUMType("").Values()),
	reflect.TypeFor[models.SessionDriverTypeENUMType]():     mcpBuildEnumSchema(models.SessionDriverTypeENUMType("").Values()),
	reflect.TypeFor[models.SessionRunnerModeTypeENUMType](): mcpBuildEnumSchema(models.SessionRunnerModeTypeENUMType("").Values()),
	reflect.TypeFor[models.SessionInputCommandTypeENUMType](): mcpBuildEnumSchema(
		models.SessionInputCommandTypeENUMType("").Values(),
	),
}

// mcpInputSchemaFor build the input JSON schema for tool parameter type In, resolving any ENUM
// fields to their enumerated schemas. The result is assigned to Tool.InputSchema so the SDK
// uses it verbatim instead of inferring a schema that would omit the ENUM values.
func mcpInputSchemaFor[In any]() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[In](&jsonschema.ForOptions{TypeSchemas: mcpEnumTypeSchemas})
	if err != nil {
		return nil, fmt.Errorf(
			"failed to infer input schema for %s: %w", reflect.TypeFor[In]().Name(), err,
		)
	}
	mcpDenullRequired(schema)
	return schema, nil
}

// mcpDenullRequired collapse the "null" member out of the type of every REQUIRED property whose
// type was inferred as a two-member ["null", X] union, recursively through the schema tree.
//
// jsonschema-go renders a Go slice or pointer field as a nullable type union (e.g. a []T becomes
// Types: ["null", "array"]) because a nil value is representable. For a field carrying
// validate:"required" that union is misleading: the field can never legitimately be null. Beyond
// reading cleanly to an agent, a plain single type also avoids the type-array form that weaker MCP
// client schema converters mishandle or drop, which can leave the agent guessing a tool's shape.
func mcpDenullRequired(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}

	required := make(map[string]struct{}, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = struct{}{}
	}

	for name, prop := range schema.Properties {
		if _, isRequired := required[name]; isRequired {
			mcpDenullType(prop)
		}
	}

	// Recurse into nested schemas so required properties at any depth are covered.
	for _, prop := range schema.Properties {
		mcpDenullRequired(prop)
	}
	mcpDenullRequired(schema.Items)
}

// mcpDenullType collapse a two-member ["null", X] type union on the given schema down to the
// single non-null type X. Any other type shape is left untouched.
func mcpDenullType(schema *jsonschema.Schema) {
	if schema == nil || len(schema.Types) != 2 {
		return
	}

	var nonNull string
	sawNull := false
	for _, t := range schema.Types {
		if t == "null" {
			sawNull = true
			continue
		}
		nonNull = t
	}
	if !sawNull || nonNull == "" {
		return
	}

	schema.Types = nil
	schema.Type = nonNull
}

// ======================================================================================
// Helper functions

// mcpAddTool register a typed tool, building its input schema with ENUM support. It is a thin
// wrapper over mcp.AddTool that pre-populates Tool.InputSchema (see inputSchemaFor); passing a
// Tool with a nil InputSchema to mcp.AddTool would infer a schema without ENUM enumerations.
func mcpAddTool[In, Out any](
	server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out],
) error {
	schema, err := mcpInputSchemaFor[In]()
	if err != nil {
		return err
	}
	tool.InputSchema = schema
	mcp.AddTool(server, tool, handler)
	return nil
}

// mcpTextResult build a successful tool result carrying a single plain-text content block. Used
// by the action tools, whose meaningful result is a short confirmation string.
func mcpTextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// ======================================================================================
// Logging Support

// MCPRequestParamKey associated key for MCPRequestParam when storing in request context
type MCPRequestParamKey struct{}

// MCPRequestParam a helper object for logging a MCP request's parameters into its context
type MCPRequestParam struct {
	// ID is the request session ID
	ID string `json:"id"`
	// IsToolCall whether the request is tool call
	IsToolCall bool `json:"is_tool_call"`
	// Method is the request method
	Method string `json:"method"`
	// ToolName tool being called
	ToolName string `json:"tool_name,omitempty"`
	// ToolArgs tool call arguments
	ToolArgs json.RawMessage `json:"tool_args,omitempty"`
	// Timestamp is when the request is first received
	Timestamp time.Time
}

// updateLogTags updates Apex log.Fields map with values the requests's parameters
func (i *MCPRequestParam) updateLogTags(tags log.Fields) {
	tags["mcp_request_session_id"] = i.ID
	tags["mcp_request_is_tool_call"] = i.IsToolCall
	tags["mcp_request_method"] = i.Method
	tags["mcp_request_timestamp"] = i.Timestamp.UTC().Format(time.RFC3339Nano)
	if i.IsToolCall {
		tags["mcp_request_tool"] = i.ToolName
		tags["mcp_request_tool_args"] = string(i.ToolArgs)
	}
}

/*
modifyLogMetadataByMCPRequestParam update log metadata with info from MCPRequestParam
*/
func modifyLogMetadataByMCPRequestParam(ctx context.Context, theTags log.Fields) {
	if ctx.Value(MCPRequestParamKey{}) != nil {
		v, ok := ctx.Value(MCPRequestParamKey{}).(MCPRequestParam)
		if ok {
			v.updateLogTags(theTags)
		}
	}
}

// ======================================================================================
// Main MCP Handler

// MCPHandler MCP request handler
type MCPHandler struct {
	goutils.Component

	LogLevel goutils.HTTPRequestLogLevel

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
		Component: goutils.Component{
			LogTags: log.Fields{"module": "api", "component": "session-mcp-handler"},
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
				modifyLogMetadataByMCPRequestParam,
			},
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
		LogLevel: logConfig.LogLevel,
	}

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
		h.registerSubmitUserCommandTool,
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

// LoggingMiddleware support middleware to log MCP requests
func (h MCPHandler) LoggingMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
		// Construct the request param tracking structure
		requestParams := MCPRequestParam{
			ID:         req.GetSession().ID(),
			IsToolCall: false,
			Method:     method,
			Timestamp:  time.Now().UTC(),
		}
		if toolCallParam, ok := req.(*mcp.CallToolRequest); ok {
			requestParams.IsToolCall = true
			requestParams.ToolName = toolCallParam.Params.Name
			requestParams.ToolArgs = toolCallParam.Params.Arguments
		}

		// Construct new context
		workingCtx := context.WithValue(ctx, MCPRequestParamKey{}, requestParams)
		logTags := h.GetLogTagsForContext(workingCtx)

		// Continue request
		start := time.Now().UTC()
		resp, err := next(workingCtx, method, req)
		duration := time.Since(start)
		respTimestamp := time.Now().UTC()

		// Build string presentation of the request
		mcpRequestStr := ""
		{
			builder := strings.Builder{}
			_, _ = builder.WriteString("\nMCP Method: " + method + "\n")
			if requestParams.IsToolCall {
				_, _ = builder.WriteString("Tool: " + requestParams.ToolName + "\n")
				args := map[string]interface{}{}
				_ = json.Unmarshal(requestParams.ToolArgs, &args)
				_, _ = builder.WriteString("Tool Args:\n")
				argsPretty, _ := json.MarshalIndent(&args, "", "  ")
				_, _ = builder.Write(argsPretty)
				_, _ = builder.WriteString("\n")
			}
			_, _ = builder.WriteString("\n")
			mcpRequestStr = builder.String()
		}

		logHandle := log.WithFields(logTags).
			WithField("mcp_response_timestamp", respTimestamp.UTC().Format(time.RFC3339Nano)).
			WithField("mcp_request_duration_ms", duration.Milliseconds())
		if err != nil {
			stackTraceErr := goutils.DeepestErrorWithTrace(err)
			l := logHandle.WithError(err)
			if stackTraceErr != nil {
				l.Errorf("MCP Request failed:\n%+v\n%s", stackTraceErr, mcpRequestStr)
			} else {
				l.Errorf("MCP Request failed\n%s", mcpRequestStr)
			}
		} else {
			switch h.LogLevel {
			case goutils.HTTPLogLevelDEBUG:
				logHandle.Debugf("MCP Request success\n%s", mcpRequestStr)

			case goutils.HTTPLogLevelINFO:
				logHandle.Infof("MCP Request success\n%s", mcpRequestStr)

			default:
				logHandle.Warnf("MCP Request success\n%s", mcpRequestStr)
			}
		}

		return resp, err
	}
}
