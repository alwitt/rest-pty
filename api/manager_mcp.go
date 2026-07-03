// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDefineNewSessionTool register the define-new-session tool. The agent may only define
// sessions backed by a sandboxed docker container: the typed docker settings are projected onto
// the full driver params, marshaled to the raw metadata the core expects, and submitted with the
// driver type fixed to DOCKER.
func (h MCPHandler) registerDefineNewSessionTool(server *mcp.Server) error {
	toolName := "define_new_session"
	toolDescription :=
		"Define a new session backed by a sandboxed docker container. The session is " +
			"created in the IDLE state and must be started before it can accept input or produce " +
			"output. Only docker-backed, hardened sessions can be defined here; host directory " +
			"mounting and added capabilities are not available."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPDefineNewSessionParams,
		) (*mcp.CallToolResult, MCPGetSessionResp, error) {
			// Project the restricted agent-facing docker settings onto the full driver params and
			// carry them as the raw metadata the core's driver resolution expects.
			driverMetadata, err := json.Marshal(in.Driver.ToDriverParams())
			if err != nil {
				exitErr := goutils.NewBadInputError(
					"Failed to encode docker session driver settings", err, true,
				)
				return nil, MCPGetSessionResp{}, exitErr
			}

			session, err := h.manage.DefineNewSession(ctx, NewSessionRequest{
				Name:                 in.Name,
				Description:          in.Description,
				Command:              in.Command,
				OutputBufferCapacity: in.OutputBufferCapacity,
				DriverType:           models.SessionDriverTypeDocker,
				DriverMetadata:       driverMetadata,
			})
			if err != nil {
				exitErr := goutils.NewRuntimeError("Failed to define new session", err, true)
				return nil, MCPGetSessionResp{}, exitErr
			}

			return nil, MCPGetSessionResp{Session: mcpSessionFrom(session)}, nil
		},
	)
}

// registerListSessionsTool register the list-sessions tool.
func (h MCPHandler) registerListSessionsTool(server *mcp.Server) error {
	toolName := "list_sessions"
	toolDescription :=
		"List the known sessions, optionally filtered by name, driver type, or state, " +
			"with pagination and ordering."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPListSessionsParams,
		) (*mcp.CallToolResult, MCPListSessionsResp, error) {
			sessions, err := h.manage.ListSessions(ctx, in)
			if err != nil {
				exitErr := goutils.NewRuntimeError("Failed to list sessions", err, true)
				return nil, MCPListSessionsResp{}, exitErr
			}

			out := make([]MCPSession, len(sessions))
			for i, s := range sessions {
				out[i] = mcpSessionFrom(s)
			}
			return nil, MCPListSessionsResp{Sessions: out}, nil
		},
	)
}

// registerGetSessionTool register the get-session tool.
func (h MCPHandler) registerGetSessionTool(server *mcp.Server) error {
	toolName := "get_session"
	toolDescription :=
		"Fetch one session by its name, including its current state and driver settings."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPGetSessionParams,
		) (*mcp.CallToolResult, MCPGetSessionResp, error) {
			session, err := h.manage.GetSession(ctx, in.SessionName)
			if err != nil {
				exitErr := goutils.NewRuntimeError("Failed to fetch session "+in.SessionName, err, true)
				return nil, MCPGetSessionResp{}, exitErr
			}

			return nil, MCPGetSessionResp{Session: mcpSessionFrom(session)}, nil
		},
	)
}

// registerUpdateSessionOutputBufCapacityTool register the update-session-output-buffer-capacity
// tool. Only permitted on IDLE sessions.
func (h MCPHandler) registerUpdateSessionOutputBufCapacityTool(
	server *mcp.Server,
) error {
	toolName := "update_session_output_buffer_capacity"
	toolDescription :=
		"Change the output buffer capacity of an IDLE session. The change is applied synchronously. " +
			"IMPORTANT: THIS WILL DELETED ALL EXISTING BUFFERED CONTENT"

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPUpdateSessionOutputBufCapacityParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.UpdateSessionOutputBufCapacity(
				ctx, in.SessionName, in.Capacity, true,
			); err != nil {
				exitErr := goutils.NewRuntimeError(
					"Failed to update session "+in.SessionName+" output buffer capacity", err, true,
				)
				return nil, nil, exitErr
			}

			return mcpTextResult(fmt.Sprintf(
				"session '%s' output buffer capacity updated to %d", in.SessionName, in.Capacity,
			)), nil, nil
		},
	)
}

// registerUpdateSessionCommandTool register the update-session-command tool. Only permitted on
// IDLE sessions.
func (h MCPHandler) registerUpdateSessionCommandTool(server *mcp.Server) error {
	toolName := "update_session_command"
	toolDescription := "Change the command an IDLE session runs."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPUpdateSessionCommandParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.UpdateSessionCommand(ctx, in.SessionName, in.Command); err != nil {
				exitErr := goutils.NewRuntimeError(
					"Failed to update session "+in.SessionName+" command", err, true,
				)
				return nil, nil, exitErr
			}

			return mcpTextResult("session '" + in.SessionName + "' command updated"), nil, nil
		},
	)
}

// registerUpdateSessionNameTool register the update-session-name tool.
func (h MCPHandler) registerUpdateSessionNameTool(server *mcp.Server) error {
	toolName := "update_session_name"
	toolDescription := "Rename a session."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPUpdateSessionNameParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.UpdateSessionName(ctx, in.SessionName, in.NewName); err != nil {
				exitErr := goutils.NewRuntimeError(
					"Failed to rename session "+in.SessionName+" to "+in.NewName, err, true,
				)
				return nil, nil, exitErr
			}

			return mcpTextResult("session '" + in.SessionName + "' renamed to '" + in.NewName + "'"),
				nil,
				nil
		},
	)
}

// registerUpdateSessionDescriptionTool register the update-session-description tool.
func (h MCPHandler) registerUpdateSessionDescriptionTool(server *mcp.Server) error {
	toolName := "update_session_description"
	toolDescription := "Change a session's description. Omit the description to clear it."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPUpdateSessionDescriptionParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.UpdateSessionDescription(ctx, in.SessionName, in.Description); err != nil {
				exitErr := goutils.NewRuntimeError(
					"Failed to update session "+in.SessionName+"description", err, true,
				)
				return nil, nil, exitErr
			}

			return mcpTextResult("session '" + in.SessionName + "' description updated"), nil, nil
		},
	)
}

// registerDeleteSessionTool register the delete-session tool. Only permitted on IDLE sessions.
func (h MCPHandler) registerDeleteSessionTool(server *mcp.Server) error {
	toolName := "delete_session"
	toolDescription := "Delete an IDLE session."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPDeleteSessionParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.DeleteSession(ctx, in.SessionName); err != nil {
				exitErr := goutils.NewRuntimeError("Failed to delete session "+in.SessionName, err, true)
				return nil, nil, exitErr
			}

			return mcpTextResult("session '" + in.SessionName + "' deleted"), nil, nil
		},
	)
}

// registerStartSessionTool register the start-session tool.
func (h MCPHandler) registerStartSessionTool(server *mcp.Server) error {
	toolName := "start_session"
	toolDescription :=
		"Start a session: bring up its runner and move it to the READY state so it can " +
			"accept input and produce output. The start is performed synchronously."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPStartSessionParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.StartSession(ctx, in.SessionName, true); err != nil {
				exitErr := goutils.NewRuntimeError("Failed to start session "+in.SessionName, err, true)
				return nil, nil, exitErr
			}

			return mcpTextResult("session '" + in.SessionName + "' started"), nil, nil
		},
	)
}

// registerStopSessionTool register the stop-session tool.
func (h MCPHandler) registerStopSessionTool(server *mcp.Server) error {
	toolName := "stop_session"
	toolDescription :=
		"Stop a session: unload its runner and return it to the IDLE state. The stop is " +
			"performed synchronously."

	return mcpAddTool(
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPStopSessionParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.manage.StopSession(ctx, in.SessionName, true); err != nil {
				exitErr := goutils.NewRuntimeError("Failed to stop session", err, true)
				return nil, nil, exitErr
			}

			return mcpTextResult("session '" + in.SessionName + "' stopped"), nil, nil
		},
	)
}
