// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"context"
	"fmt"

	"github.com/alwitt/goutils"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpStripANSI whether the output-read tools strip ANSI escape sequences before returning the
// decoded terminal output to the agent. This is always true: an agent reads plain text, so the
// raw escape sequences are noise (see the removed strip_ansi tool parameter).
const mcpStripANSI = true

// registerSubmitUserInputTool register the submit-user-input tool. Only permitted on READY
// sessions.
func (h MCPHandler) registerSubmitUserInputTool(server *mcp.Server) error {
	toolName := "submit_user_input"
	toolDescription :=
		"Submit a batch of input events (keystrokes) to a READY session's runner and wait for " +
			"it to acknowledge them. Inputs are processed in order (e.g. type text, then press " +
			"ENTER). These are fed to the session's running process as STDIN, not the command the " +
			"session is defined to run. This does not return the resulting output; read the " +
			"session output separately."

	return MCPAddTool(
		&h,
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPSubmitUserInputParams,
		) (*mcp.CallToolResult, any, error) {
			if err := h.io.SubmitUserCommandToSession(ctx, in.SessionName, in.Inputs); err != nil {
				exitErr := goutils.NewRuntimeError("Failed to submit user input to session", err, true)
				return nil, nil, exitErr
			}

			return mcpTextResult(fmt.Sprintf(
				"submitted %d input(s) to session '%s'", len(in.Inputs), in.SessionName,
			)), nil, nil
		},
	)
}

// registerReadSessionOutputChunkTool register the read-session-output-chunk tool. The decoded
// terminal text is returned as a text content block; the positional metadata (actual offset and
// bytes read) is returned as structured content so the agent can advance its read offset.
func (h MCPHandler) registerReadSessionOutputChunkTool(server *mcp.Server) error {
	toolName := "read_session_output_chunk"
	toolDescription :=
		"Read a chunk of a session's output starting at the given stream offset. The output ring " +
			"buffer only retains the most recent bytes, so if the requested offset has aged out the " +
			"read is moved forward to the oldest byte still buffered; the returned actual_offset " +
			"reports where the data actually starts. The decoded terminal text (ANSI escape " +
			"sequences removed) is returned both as the result's text content and in the structured " +
			"result's output field. Use actual_offset + read to compute the next offset."

	return MCPAddTool(
		&h,
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPReadSessionOutputChunkParams,
		) (*mcp.CallToolResult, MCPReadSessionOutputResp, error) {
			read, err := h.io.ReadSessionOutputChunk(
				ctx, in.SessionName, in.Offset, in.Limit, mcpStripANSI,
			)
			if err != nil {
				exitErr := goutils.NewRuntimeError("Failed to read session output chunk", err, true)
				return nil, MCPReadSessionOutputResp{}, exitErr
			}

			return outputReadResult(read)
		},
	)
}

// registerReadSessionOutputNewestTool register the read-session-output-newest tool. Like the
// chunk tool but anchored to the end of the stream, returning up to "limit" of the most recently
// written bytes.
func (h MCPHandler) registerReadSessionOutputNewestTool(server *mcp.Server) error {
	toolName := "read_session_output_newest"
	toolDescription :=
		"Read the most recently written bytes of a session's output, up to the given limit. No " +
			"offset is given; the read is anchored to the end of the stream. The returned " +
			"actual_offset reports the stream position the returned data starts at. The decoded " +
			"terminal text (ANSI escape sequences removed) is returned both as the result's text " +
			"content and in the structured result's output field."

	return MCPAddTool(
		&h,
		server,
		&mcp.Tool{Name: toolName, Description: toolDescription},
		func(
			ctx context.Context, _ *mcp.CallToolRequest, in MCPReadSessionOutputNewestParams,
		) (*mcp.CallToolResult, MCPReadSessionOutputResp, error) {
			read, err := h.io.ReadSessionOutputNewest(ctx, in.SessionName, in.Limit, mcpStripANSI)
			if err != nil {
				exitErr := goutils.NewRuntimeError("Failed to read newest session output", err, true)
				return nil, MCPReadSessionOutputResp{}, exitErr
			}

			return outputReadResult(read)
		},
	)
}

// outputReadResult build the tool result for an output read. The decoded terminal text is placed
// in the result's Content as a text block AND duplicated in the structured value's Output field.
// The positional metadata (actual offset and bytes read) is returned alongside it as the typed
// output value, which the SDK marshals into StructuredContent. Providing Content explicitly keeps
// the agent-facing text as the actual terminal output (the SDK only falls back to serializing the
// structured value into a text block when Content is unset); duplicating the text into Output means
// clients that surface only structured content still receive it.
func outputReadResult(read SessionOutputRead) (
	*mcp.CallToolResult, MCPReadSessionOutputResp, error,
) {
	text := string(read.Data)
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
	return result, MCPReadSessionOutputResp{
		Output: text, ActualOffset: read.ActualOffset, Read: read.Read,
	}, nil
}
