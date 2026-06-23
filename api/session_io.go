// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
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
	"github.com/gorilla/mux"
	"github.com/oklog/ulid/v2"
)

// SessionIOHandler session IO API handlers
type SessionIOHandler struct {
	goutils.RestAPIHandler
	validate *validator.Validate

	// persistence layer client
	persistence db.Client

	// redisClient redis Client handle
	redisClient goutilsRedis.Client
}

/*
NewSessionIOHandler define a new session IO REST API handler

	@param persistence db.Client - DB persistence layer
	@param redisClient goutilsRedis.Client - redis client
	@param logConfig common.HTTPRequestLogging - handler log settings
	@param metrics goutils.HTTPRequestMetricHelper - metric collection agent
	@returns new handler
*/
func NewSessionIOHandler(
	persistence db.Client,
	redisClient goutilsRedis.Client,
	logConfig models.HTTPRequestLogging,
	metrics goutils.HTTPRequestMetricHelper,
) (SessionIOHandler, error) {
	handler := SessionIOHandler{
		RestAPIHandler: goutils.RestAPIHandler{
			Component: goutils.Component{
				LogTags: log.Fields{"module": "api", "component": "session-io-handler"},
				LogTagModifiers: []goutils.LogMetadataModifier{
					goutils.ModifyLogMetadataByRestRequestParam,
				},
			},
			CallRequestIDHeaderField: &logConfig.RequestIDHeader,
			DoNotLogHeaders: func() map[string]bool {
				result := map[string]bool{}
				for _, v := range logConfig.DoNotLogHeaders {
					result[v] = true
				}
				return result
			}(),
			LogLevel:      logConfig.LogLevel,
			MetricsHelper: metrics,
		},
		validate:    validator.New(),
		persistence: persistence,
		redisClient: redisClient,
	}

	if err := models.RegisterWithValidator(handler.validate); err != nil {
		return handler, models.RuntimeError{
			Core: err, Message: "failed to install custom validation macros",
		}
	}

	return handler, nil
}

// ======================================================================================
// Session IO - Structured Input

// UserCommandRequest user command requests
type UserCommandRequest struct {
	// Commands the list of commands to send to the session
	Commands []models.SessionInputCommand `json:"commands" validate:"required,gte=1,dive"`
}

// SubmitUserCommandToSession godoc
// @Summary Submit user commands
// @Description Submit user commands to session runner
// @tags io,input,structured
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param param body UserCommandRequest true "User commands"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/io/input/commands [post]
func (h SessionIOHandler) SubmitUserCommandToSession(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]
	logTags["session"] = sessionName

	// ------------------------------------------------------------------------------------
	// Parse request

	if r.Body == nil {
		msg := "No payload provided containing user commands"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the user commands
	var params UserCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		msg := "Unable to parse user commands from request"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.WithError(err).WithFields(logTags).Error("Request body close error")
		}
	}()

	// Validate user commands
	if err := h.validate.Struct(&params); err != nil {
		msg := "User commands parameters not valid"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	{
		t, _ := json.Marshal(&params)
		log.
			WithFields(logTags).
			WithField("user-commands", string(t)).
			Debug("Submitting user commands to session")
	}

	// ------------------------------------------------------------------------------------
	// Fetch the sessionEntry
	var sessionEntry models.Session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); dbErr != nil {
		var unknownSession models.UnknownSessionError
		if errors.As(dbErr, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
			return
		}
		msg := "Failed to fetch session '" + sessionName + "'"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		return
	}

	// Verify session ready
	if sessionEntry.State != models.SessionStateReady {
		msg := "Session '" + sessionName + "' is not ready to accept user commands"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusConflict
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// ------------------------------------------------------------------------------------
	// Setup REDIS IPC queues

	// Get REDIS IPC queue to submit commands to the session runner
	reqQueue, err := h.redisClient.GetQueueHandle(
		r.Context(), session.BuildSessionIPCQueueName(sessionEntry.ID),
	)
	if err != nil {
		msg := "Failed to get session '" + sessionName + "' IPC request queue"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	requestID := ulid.Make().String()
	ipcRequest := models.IPCMessageReqRunCommands{
		BaseIPCMessage: models.BaseIPCMessage{
			RequestID: requestID,
			Type:      models.IPCMsgTypeReqRunCommands,
			Sender:    uuid.NewString(),
			Timestamp: time.Now().UTC(),
		},
		Commands: params.Commands,
	}

	// Get REDIS IPC queue to receive response from the session runner
	respQueue, err := h.redisClient.GetQueueHandle(
		r.Context(), session.BuildSessionIPCRespQueueName(requestID),
	)
	if err != nil {
		msg := "Failed to get IPC response queue"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	defer func() {
		lclCtx, lclCtxCancel := context.WithTimeout(context.Background(), time.Second*10)
		defer lclCtxCancel()
		err := h.redisClient.DeleteQueue(lclCtx, session.BuildSessionIPCRespQueueName(requestID))
		if err != nil {
			log.WithError(err).WithFields(logTags).Error("IPC Response queue cleanup failed")
		}
	}()

	// ------------------------------------------------------------------------------------
	// Submit request

	if _, err := reqQueue.PushRight(r.Context(), ipcRequest, nil); err != nil {
		msg := "Failed to submit commands to session '" + sessionName + "' runner via IPC"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// ------------------------------------------------------------------------------------
	// Wait for response

	resp, err := respQueue.PopLeft(r.Context(), true, common.GetTypedPtr(time.Second*60))
	if err != nil {
		msg := "Failed to receive response from session '" + sessionName + "' runner via IPC"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if resp == nil {
		msg := "Session '" + sessionName + "' runner returned empty response for user commands"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the response
	respContent, err := resp.StringPayload()
	if err != nil {
		msg := "Failed to read response from session '" + sessionName + "' runner"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	parsedResp, err := models.ParseIPCMessage(h.validate, []byte(respContent))
	if err != nil {
		msg := "Failed to parse response from session '" + sessionName + "' runner"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Check for success
	switch t := parsedResp.(type) {
	case models.IPCMessageRespUniversal:
		if !t.Success {
			errMsg := "NO ERROR PROVIDED"
			if t.ErrorMsg != nil {
				errMsg = *t.ErrorMsg
			}
			msg := "Session '" + sessionName + "' runner failed to process user commands"
			log.WithField("error", errMsg).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, errMsg)
			return
		}

	default:
		err := models.RuntimeError{
			Message: "unexpected IPC response payload type " + reflect.TypeOf(parsedResp).String(),
		}
		msg := "Response from session '" + sessionName + "' runner is wrong"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// ------------------------------------------------------------------------------------
	// Done

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session IO - Read One Output Chunk

// SessionOutputChunkResponse response containing one chunk of session output
type SessionOutputChunkResponse struct {
	goutils.RestAPIBaseResponse
	// ActualOffset the offset the returned data actually starts at. When the requested offset
	// has already aged out of the ring buffer, the read is moved forward to the oldest byte
	// still in the buffer, and this reports that position.
	ActualOffset int64 `json:"actual_offset" validate:"gte=0"`
	// Read number of bytes actually read from the buffer
	Read int `json:"read" validate:"gte=0"`
	// Data base64 encoded chunk read from the buffer
	Data string `json:"data"`
}

// ReadSessionOutputChunk godoc
// @Summary Read one chunk of session output
// @Description Read one chunk of data from a session's output ring buffer. As the buffer only
// @Description retains the most recent bytes, the requested offset may have aged out; in that
// @Description case the read is moved forward and "actual_offset" reports where the data starts.
// @tags io,output
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param offset query int true "Read index position within the session output stream"
// @Param limit query int true "Max number of bytes to read"
// @Success 200 {object} SessionOutputChunkResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/io/output/chunk [get]
func (h SessionIOHandler) ReadSessionOutputChunk(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]
	logTags["session"] = sessionName

	query := r.URL.Query()

	// ------------------------------------------------------------------------------------
	// Parse query parameters

	// Parse the read offset
	rawOffset := query.Get("offset")
	if rawOffset == "" {
		msg := "Query parameter 'offset' is required"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	offset, err := strconv.ParseInt(rawOffset, 10, 64)
	if err != nil {
		msg := "Query parameter 'offset' must be an integer"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if offset < 0 {
		msg := "Query parameter 'offset' must not be negative"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the read limit
	rawLimit := query.Get("limit")
	if rawLimit == "" {
		msg := "Query parameter 'limit' is required"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil {
		msg := "Query parameter 'limit' must be an integer"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if limit < 1 {
		msg := "Query parameter 'limit' must be at least 1"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// ------------------------------------------------------------------------------------
	// Fetch the session

	var sessionEntry models.Session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); dbErr != nil {
		var unknownSession models.UnknownSessionError
		if errors.As(dbErr, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
			return
		}
		msg := "Failed to fetch session '" + sessionName + "'"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		return
	}

	// A single read can never return more than the ring buffer can hold, so cap the read
	// length to the buffer capacity to bound the receive buffer allocation.
	if int64(limit) > sessionEntry.OutputBufferCapacity {
		limit = int(sessionEntry.OutputBufferCapacity)
	}

	// ------------------------------------------------------------------------------------
	// Read from the output ring buffer

	buffer, err := h.redisClient.GetRingBuffer(
		r.Context(),
		session.BuildSessionOutputBufferName(sessionEntry.ID),
		sessionEntry.OutputBufferCapacity,
	)
	if err != nil {
		msg := "Failed to get session '" + sessionName + "' output buffer"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	readBuf := make([]byte, limit)
	actualOffset, readCount, _, err := buffer.ReadAt(r.Context(), readBuf, offset)
	if err != nil {
		msg := "Failed to read session '" + sessionName + "' output buffer"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// ------------------------------------------------------------------------------------
	// Done

	respCode = http.StatusOK
	response = SessionOutputChunkResponse{
		RestAPIBaseResponse: h.GetStdRESTSuccessMsg(r.Context()),
		ActualOffset:        actualOffset,
		Read:                readCount,
		Data:                base64.StdEncoding.EncodeToString(readBuf[:readCount]),
	}
}

// ======================================================================================
// Session IO - Tail The Output Stream

// defaultTailPollPeriod default interval between buffer data availability checks when no
// "poll_period_msec" query parameter is provided.
const defaultTailPollPeriod = time.Millisecond * 250

// tailStreamReadChunkSize max number of bytes pulled from the buffer per streamed event.
const tailStreamReadChunkSize = 4096

// TailSessionOutput godoc
// @Summary Tail session output
// @Description Continuously stream a session's output via server-sent events (SSE). Each event
// @Description carries a base64 encoded chunk of bytes read from the output ring buffer. The
// @Description stream runs until the client disconnects.
// @tags io,output
// @Produce text/event-stream
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param offset query int true "Read index position to start tailing from"
// @Param poll_period_msec query int false "Milliseconds between buffer data availability checks (default 250)"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/io/output/tail [get]
func (h SessionIOHandler) TailSessionOutput(w http.ResponseWriter, r *http.Request) {
	logTags := h.GetLogTagsForContext(r.Context())
	sessionName := mux.Vars(r)["sessionName"]
	logTags["session"] = sessionName

	// writeError emits a standard REST error response. This is only valid before the SSE
	// stream is started; once the response header is written we can no longer change it and
	// must fall back to logging.
	writeError := func(code int, msg, detail string) {
		if err := h.WriteRESTResponse(
			w, code, h.GetStdRESTErrorMsg(r.Context(), code, msg, detail), nil,
		); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}

	query := r.URL.Query()

	// ------------------------------------------------------------------------------------
	// Parse query parameters

	// Parse the starting offset
	rawOffset := query.Get("offset")
	if rawOffset == "" {
		msg := "Query parameter 'offset' is required"
		log.WithFields(logTags).Error(msg)
		writeError(http.StatusBadRequest, msg, msg)
		return
	}
	offset, err := strconv.ParseInt(rawOffset, 10, 64)
	if err != nil {
		msg := "Query parameter 'offset' must be an integer"
		log.WithError(err).WithFields(logTags).Error(msg)
		writeError(http.StatusBadRequest, msg, err.Error())
		return
	}
	if offset < 0 {
		msg := "Query parameter 'offset' must not be negative"
		log.WithFields(logTags).Error(msg)
		writeError(http.StatusBadRequest, msg, msg)
		return
	}

	// Parse the optional poll period
	pollPeriod := defaultTailPollPeriod
	if raw := query.Get("poll_period_msec"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			msg := "Query parameter 'poll_period_msec' must be an integer"
			log.WithError(err).WithFields(logTags).Error(msg)
			writeError(http.StatusBadRequest, msg, err.Error())
			return
		}
		if parsed < 1 {
			msg := "Query parameter 'poll_period_msec' must be at least 1"
			log.WithFields(logTags).Error(msg)
			writeError(http.StatusBadRequest, msg, msg)
			return
		}
		pollPeriod = time.Duration(parsed) * time.Millisecond
	}

	// The SSE stream is written incrementally and flushed after every event, so the response
	// writer must support flushing.
	flusher, ok := w.(http.Flusher)
	if !ok {
		msg := "Streaming unsupported by this connection"
		log.WithFields(logTags).Error(msg)
		writeError(http.StatusInternalServerError, msg, msg)
		return
	}

	// ------------------------------------------------------------------------------------
	// Fetch the session

	var sessionEntry models.Session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); dbErr != nil {
		var unknownSession models.UnknownSessionError
		if errors.As(dbErr, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			writeError(http.StatusNotFound, msg, dbErr.Error())
			return
		}
		msg := "Failed to fetch session '" + sessionName + "'"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		writeError(http.StatusInternalServerError, msg, dbErr.Error())
		return
	}

	// ------------------------------------------------------------------------------------
	// Prepare the output ring buffer reader

	buffer, err := h.redisClient.GetRingBuffer(
		r.Context(),
		session.BuildSessionOutputBufferName(sessionEntry.ID),
		sessionEntry.OutputBufferCapacity,
	)
	if err != nil {
		msg := "Failed to get session '" + sessionName + "' output buffer"
		log.WithError(err).WithFields(logTags).Error(msg)
		writeError(http.StatusInternalServerError, msg, err.Error())
		return
	}

	// The reader's working context is derived from the request context, so a client
	// disconnect cancels it and the blocking Read returns io.EOF, ending the stream.
	reader := buffer.AsReadWriteCloser(r.Context(), offset, pollPeriod)
	defer func() {
		if err := reader.Close(); err != nil {
			log.WithError(err).WithFields(logTags).Error("Output stream reader close error")
		}
	}()

	// ------------------------------------------------------------------------------------
	// Start the SSE stream

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	log.WithFields(logTags).Debug("Started session output tail stream")
	defer func() {
		log.WithFields(logTags).Debug("Ended session output tail stream")
	}()

	readBuf := make([]byte, tailStreamReadChunkSize)
	for {
		n, readErr := reader.Read(readBuf)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(readBuf[:n])
			if _, err := fmt.Fprintf(w, "event: output\ndata: %s\n\n", encoded); err != nil {
				// The client connection is likely gone; stop streaming.
				log.WithError(err).WithFields(logTags).Debug("Output stream write failed; ending stream")
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			// io.EOF is the clean shutdown signal (context cancelled / client disconnect).
			if !errors.Is(readErr, io.EOF) {
				log.WithError(readErr).WithFields(logTags).Error("Output stream read failure")
			}
			return
		}
	}
}
