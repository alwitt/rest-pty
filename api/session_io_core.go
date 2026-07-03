// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"context"
	"reflect"
	"regexp"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/common"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
)

// SessionIOCore transport-agnostic session IO business logic. It holds the dependencies shared
// by the REST handlers and the MCP tools so the actual persistence / redis IPC operations live
// in one place independent of transport.
type SessionIOCore struct {
	// validate request/parameter validator
	validate *validator.Validate

	// persistence layer client
	persistence db.Client

	// redisClient redis Client handle
	redisClient goutilsRedis.Client
}

/*
SubmitUserCommandToSession submit a batch of user commands to a session's runner and wait for the
runner's acknowledgement, exchanged over the redis IPC queues.

The session must exist and be in the READY state to accept commands. Errors are returned as-is:
a goutils.NotFoundError if the session is unknown, a goutils.ConsistencyError if the session is
not ready, and other errors for IPC / runner failures.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to submit commands to
	@param commands []models.SessionInputCommand - the commands to submit
	@returns error encountered while submitting the commands
*/
func (c SessionIOCore) SubmitUserCommandToSession(
	ctx context.Context, sessionName string, commands []models.SessionInputCommand,
) error {
	// ------------------------------------------------------------------------------------
	// Fetch the sessionEntry
	var sessionEntry models.Session
	if err := c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); err != nil {
		return goutils.NewRuntimeError(
			"failed to fetch session '"+sessionName+"'", err, true,
		)
	}

	// Verify session ready
	if sessionEntry.State != models.SessionStateReady {
		return goutils.NewConsistencyError(
			"session '"+sessionName+"' is not ready to accept user commands", nil, true,
		)
	}

	// ------------------------------------------------------------------------------------
	// Setup REDIS IPC queues

	// Get REDIS IPC queue to submit commands to the session runner
	reqQueue, err := c.redisClient.GetQueueHandle(
		ctx, session.BuildSessionIPCQueueName(sessionEntry.ID),
	)
	if err != nil {
		return goutils.NewRuntimeError(
			"failed to get session '"+sessionName+"' IPC request queue", err, true,
		)
	}

	requestID := ulid.Make().String()
	ipcRequest := models.IPCMessageReqRunCommands{
		BaseIPCMessage: models.BaseIPCMessage{
			RequestID: requestID,
			Type:      models.IPCMsgTypeReqRunCommands,
			Sender:    uuid.NewString(),
			Timestamp: time.Now().UTC(),
		},
		Commands: commands,
	}

	// Get REDIS IPC queue to receive response from the session runner
	respQueue, err := c.redisClient.GetQueueHandle(
		ctx, session.BuildSessionIPCRespQueueName(requestID),
	)
	if err != nil {
		return goutils.NewRuntimeError("failed to get IPC response queue", err, true)
	}
	defer func() {
		lclCtx, lclCtxCancel := context.WithTimeout(context.Background(), time.Second*10)
		defer lclCtxCancel()
		if err := c.redisClient.DeleteQueue(
			lclCtx, session.BuildSessionIPCRespQueueName(requestID),
		); err != nil {
			log.WithError(err).Error("IPC Response queue cleanup failed")
		}
	}()

	// ------------------------------------------------------------------------------------
	// Submit request

	if _, err := reqQueue.PushRight(ctx, ipcRequest, nil); err != nil {
		return goutils.NewRuntimeError(
			"failed to submit commands to session '"+sessionName+"' runner via IPC", err, true,
		)
	}

	// ------------------------------------------------------------------------------------
	// Wait for response

	resp, err := respQueue.PopLeft(ctx, true, common.GetTypedPtr(time.Second*60))
	if err != nil {
		return goutils.NewRuntimeError(
			"failed to receive response from session '"+sessionName+"' runner via IPC", err, true,
		)
	}
	if resp == nil {
		return goutils.NewRuntimeError(
			"session '"+sessionName+"' runner returned empty response for user commands", nil, true,
		)
	}

	// Parse the response
	respContent, err := resp.StringPayload()
	if err != nil {
		return goutils.NewRuntimeError(
			"failed to read response from session '"+sessionName+"' runner", err, true,
		)
	}
	parsedResp, err := models.ParseIPCMessage(c.validate, []byte(respContent))
	if err != nil {
		return goutils.NewRuntimeError(
			"failed to parse response from session '"+sessionName+"' runner", err, true,
		)
	}

	// Check for success
	switch t := parsedResp.(type) {
	case models.IPCMessageRespUniversal:
		if !t.Success {
			errMsg := "NO ERROR PROVIDED"
			if t.ErrorMsg != nil {
				errMsg = *t.ErrorMsg
			}
			return goutils.NewRuntimeError(
				"session '"+sessionName+"' runner failed to process user commands: "+errMsg, nil, true,
			)
		}

	default:
		return goutils.NewRuntimeError(
			"unexpected IPC response payload type "+reflect.TypeOf(parsedResp).String(), nil, true,
		)
	}

	return nil
}

// ======================================================================================
// Session IO - Output ANSI escape stripping

// ansiEscapeSequence matches ANSI / VT escape sequences in raw terminal output: the 7-bit
// "ESC <Fe>" forms, the standalone 8-bit C1 controls, and CSI sequences (both the 7-bit "ESC ["
// and 8-bit 0x9B introducers).
var ansiEscapeSequence = regexp.MustCompile(
	`(?:\x1B[@-Z\\-_]|[\x80-\x9A\x9C-\x9F]|(?:\x1B\[|\x9B)[0-?]*[ -/]*[@-~])`,
)

// stripANSIEscapes removes all ANSI escape sequences from the given output bytes, returning the
// cleaned slice. Filtering is per-call, so an escape sequence split across two streamed chunks is
// not stripped; this is acceptable for the line-oriented output the filter targets.
func stripANSIEscapes(data []byte) []byte {
	return ansiEscapeSequence.ReplaceAll(data, nil)
}

// ======================================================================================
// Session IO - Read Output

// maxOutputReadBytes hard upper bound on the number of bytes a single output read may allocate
// and return. The requested "limit" is capped to this regardless of the session's buffer
// capacity, so a user-provided value can never drive an unbounded receive buffer allocation.
const maxOutputReadBytes = 1 << 20 // 1 MiB

// cappedReadLimit returns the read length to use for a single output read: the requested limit,
// bounded by both the absolute maxOutputReadBytes ceiling and the session's buffer capacity (a
// single read can never return more than the ring buffer can hold).
func cappedReadLimit(limit int, bufferCapacity int64) int {
	if limit > maxOutputReadBytes {
		limit = maxOutputReadBytes
	}
	if int64(limit) > bufferCapacity {
		limit = int(bufferCapacity)
	}
	return limit
}

// SessionOutputRead the result of a single session output ring buffer read.
type SessionOutputRead struct {
	// ActualOffset the offset the returned data actually starts at. When the requested offset
	// has already aged out of the ring buffer, the read is moved forward to the oldest byte
	// still in the buffer, and this reports that position.
	ActualOffset int64
	// Read number of bytes actually read from the buffer (in terms of buffer bytes, before any
	// ANSI stripping is applied to Data).
	Read int
	// Data the chunk read from the buffer, ANSI-stripped when requested.
	Data []byte
}

/*
getSessionOutputBuffer fetch a session by name and return a handle to its output ring buffer along
with the read length to use, capped against the session's buffer capacity (see cappedReadLimit).
Errors are returned as-is: a goutils.NotFoundError if the session is unknown.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session whose output buffer to read
	@param limit int - the requested read length
	@returns the ring buffer handle and the capped read length
*/
func (c SessionIOCore) getSessionOutputBuffer(
	ctx context.Context, sessionName string, limit int,
) (goutilsRedis.RingBuffer, int, error) {
	var sessionEntry models.Session
	if err := c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); err != nil {
		return nil, 0, goutils.NewRuntimeError(
			"failed to fetch session '"+sessionName+"'", err, true,
		)
	}

	buffer, err := c.redisClient.GetRingBuffer(
		ctx,
		session.BuildSessionOutputBufferName(sessionEntry.ID),
		sessionEntry.OutputBufferCapacity,
	)
	if err != nil {
		return nil, 0, goutils.NewRuntimeError(
			"failed to get session '"+sessionName+"' output buffer", err, true,
		)
	}

	// Cap the read length to bound the receive buffer allocation against the user-provided
	// limit (see cappedReadLimit).
	return buffer, cappedReadLimit(limit, sessionEntry.OutputBufferCapacity), nil
}

/*
GetSessionOutputBuffer fetch a session by name and return a handle to its output ring buffer. This
is the transport-agnostic entry point for callers that drive their own reads against the buffer
(e.g. the REST tail streamer), where the per-read length is chosen by the caller rather than by a
"limit" parameter. Errors are returned wrapped: a goutils.NotFoundError if the session is unknown.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session whose output buffer to fetch
	@returns the ring buffer handle
*/
func (c SessionIOCore) GetSessionOutputBuffer(
	ctx context.Context, sessionName string,
) (goutilsRedis.RingBuffer, error) {
	// The read length is irrelevant here since the caller drives its own reads; pass the absolute
	// ceiling so the cap is a no-op.
	buffer, _, err := c.getSessionOutputBuffer(ctx, sessionName, maxOutputReadBytes)
	return buffer, err
}

/*
ReadSessionOutputChunk read one chunk of data from a session's output ring buffer, starting at the
given offset. As the buffer only retains the most recent bytes, the requested offset may have aged
out; in that case the read is moved forward and the returned ActualOffset reports where the data
starts. Errors are returned as-is: a goutils.NotFoundError if the session is unknown.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to read from
	@param offset int64 - the stream offset to start reading at
	@param limit int - the max number of bytes to read
	@param stripANSI bool - whether to strip ANSI escape sequences from the returned data
	@returns the read result
*/
func (c SessionIOCore) ReadSessionOutputChunk(
	ctx context.Context, sessionName string, offset int64, limit int, stripANSI bool,
) (SessionOutputRead, error) {
	buffer, readLimit, err := c.getSessionOutputBuffer(ctx, sessionName, limit)
	if err != nil {
		return SessionOutputRead{}, err
	}

	readBuf := make([]byte, readLimit)
	actualOffset, readCount, _, err := buffer.ReadAt(ctx, readBuf, offset)
	if err != nil {
		return SessionOutputRead{}, goutils.NewRuntimeError(
			"failed to read session '"+sessionName+"' output buffer", err, true,
		)
	}

	// The chunk content is stripped of ANSI escapes when requested, but "Read" and "ActualOffset"
	// stay in terms of buffer bytes so the client can still advance its offset.
	outData := readBuf[:readCount]
	if stripANSI {
		outData = stripANSIEscapes(outData)
	}

	return SessionOutputRead{ActualOffset: actualOffset, Read: readCount, Data: outData}, nil
}

/*
ReadSessionOutputNewest read the newest bytes from a session's output ring buffer. Unlike
ReadSessionOutputChunk no offset is given; the read is anchored to the end of the stream and
returns up to "limit" of the most recently written bytes. The returned ActualOffset reports the
stream position the returned data starts at. Errors are returned as-is: a goutils.NotFoundError if
the session is unknown.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to read from
	@param limit int - the max number of bytes to read
	@param stripANSI bool - whether to strip ANSI escape sequences from the returned data
	@returns the read result
*/
func (c SessionIOCore) ReadSessionOutputNewest(
	ctx context.Context, sessionName string, limit int, stripANSI bool,
) (SessionOutputRead, error) {
	buffer, readLimit, err := c.getSessionOutputBuffer(ctx, sessionName, limit)
	if err != nil {
		return SessionOutputRead{}, err
	}

	readBuf := make([]byte, readLimit)
	actualOffset, readCount, _, err := buffer.ReadNewest(ctx, readBuf)
	if err != nil {
		return SessionOutputRead{}, goutils.NewRuntimeError(
			"failed to read session '"+sessionName+"' output buffer", err, true,
		)
	}

	// The chunk content is stripped of ANSI escapes when requested, but "Read" and "ActualOffset"
	// stay in terms of buffer bytes so the client can still advance its offset.
	outData := readBuf[:readCount]
	if stripANSI {
		outData = stripANSIEscapes(outData)
	}

	return SessionOutputRead{ActualOffset: actualOffset, Read: readCount, Data: outData}, nil
}
