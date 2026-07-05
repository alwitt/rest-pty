// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/gorilla/mux"
)

// SessionIOHandler session IO API handlers
type SessionIOHandler struct {
	goutils.RestAPIHandler

	// core transport-agnostic session IO business logic
	core SessionIOCore
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
			LogLevel:          logConfig.LogLevel,
			LogRequestPayload: logConfig.LogRequestPayload,
			MetricsHelper:     metrics,
		},
		core: SessionIOCore{
			validate:    validator.New(),
			persistence: persistence,
			redisClient: redisClient,
		},
	}

	if err := models.RegisterWithValidator(handler.core.validate); err != nil {
		return handler, goutils.NewRuntimeError("failed to install custom validation macros", err, true)
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
// @tags io
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
			log.
				WithError(err).
				WithFields(goutils.UpdateCodePositionInTags(logTags)).
				Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]
	logTags["session"] = sessionName

	// ------------------------------------------------------------------------------------
	// Parse request

	if r.Body == nil {
		msg := "No payload provided containing user commands"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the user commands
	var params UserCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		msg := "Unable to parse user commands from request"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.
				WithError(err).
				WithFields(goutils.UpdateCodePositionInTags(logTags)).
				Error("Request body close error")
		}
	}()

	// Validate user commands
	if err := h.core.validate.Struct(&params); err != nil {
		msg := "User commands parameters not valid"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	{
		t, _ := json.Marshal(&params)
		log.
			WithFields(goutils.UpdateCodePositionInTags(logTags)).
			WithField("user-commands", string(t)).
			Debug("Submitting user commands to session")
	}

	// ------------------------------------------------------------------------------------
	// Submit the commands to the session runner

	if err := h.core.SubmitUserCommandToSession(
		r.Context(), sessionName, params.Commands,
	); err != nil {
		var unknownSession goutils.NotFoundError
		var notReady goutils.ConsistencyError
		switch {
		case errors.As(err, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		case errors.As(err, &notReady):
			msg := "Session '" + sessionName + "' is not ready to accept user commands"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		default:
			msg := "Failed to submit commands to session '" + sessionName + "' runner"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		}
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
// @tags io
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param offset query int true "Read index position within the session output stream"
// @Param limit query int true "Max number of bytes to read"
// @Param strip_ansi query bool false "Strip ANSI escape sequences from the returned data"
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
			log.
				WithError(err).
				WithFields(goutils.UpdateCodePositionInTags(logTags)).
				Error("Failed to form response")
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
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	offset, err := strconv.ParseInt(rawOffset, 10, 64)
	if err != nil {
		msg := "Query parameter 'offset' must be an integer"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if offset < 0 {
		msg := "Query parameter 'offset' must not be negative"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the read limit
	rawLimit := query.Get("limit")
	if rawLimit == "" {
		msg := "Query parameter 'limit' is required"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil {
		msg := "Query parameter 'limit' must be an integer"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if limit < 1 {
		msg := "Query parameter 'limit' must be at least 1"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the optional ANSI-strip flag
	stripANSI := false
	if raw := query.Get("strip_ansi"); raw != "" {
		stripANSI, err = strconv.ParseBool(raw)
		if err != nil {
			msg := "Query parameter 'strip_ansi' must be a boolean"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
	}

	// ------------------------------------------------------------------------------------
	// Read from the output ring buffer

	readResult, err := h.core.ReadSessionOutputChunk(
		r.Context(), sessionName, offset, limit, stripANSI,
	)
	if err != nil {
		var unknownSession goutils.NotFoundError
		if errors.As(err, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		msg := "Failed to read session '" + sessionName + "' output"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// ------------------------------------------------------------------------------------
	// Done

	respCode = http.StatusOK
	response = SessionOutputChunkResponse{
		RestAPIBaseResponse: h.GetStdRESTSuccessMsg(r.Context()),
		ActualOffset:        readResult.ActualOffset,
		Read:                readResult.Read,
		Data:                base64.StdEncoding.EncodeToString(readResult.Data),
	}
}

// ======================================================================================
// Session IO - Read The Newest Output

// ReadSessionOutputNewest godoc
// @Summary Read the newest session output
// @Description Read the newest bytes from a session's output ring buffer. Unlike the "chunk"
// @Description endpoint no offset is given; the read is anchored to the end of the stream and
// @Description returns up to "limit" of the most recently written bytes. "actual_offset" reports
// @Description the stream position the returned data starts at.
// @tags io
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param limit query int true "Max number of bytes to read"
// @Param strip_ansi query bool false "Strip ANSI escape sequences from the returned data"
// @Success 200 {object} SessionOutputChunkResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/io/output/newest [get]
func (h SessionIOHandler) ReadSessionOutputNewest(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.
				WithError(err).
				WithFields(goutils.UpdateCodePositionInTags(logTags)).
				Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]
	logTags["session"] = sessionName

	query := r.URL.Query()

	// ------------------------------------------------------------------------------------
	// Parse query parameters

	// Parse the read limit
	rawLimit := query.Get("limit")
	if rawLimit == "" {
		msg := "Query parameter 'limit' is required"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil {
		msg := "Query parameter 'limit' must be an integer"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if limit < 1 {
		msg := "Query parameter 'limit' must be at least 1"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the optional ANSI-strip flag
	stripANSI := false
	if raw := query.Get("strip_ansi"); raw != "" {
		stripANSI, err = strconv.ParseBool(raw)
		if err != nil {
			msg := "Query parameter 'strip_ansi' must be a boolean"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
	}

	// ------------------------------------------------------------------------------------
	// Read from the output ring buffer

	readResult, err := h.core.ReadSessionOutputNewest(r.Context(), sessionName, limit, stripANSI)
	if err != nil {
		var unknownSession goutils.NotFoundError
		if errors.As(err, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		msg := "Failed to read session '" + sessionName + "' output"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// ------------------------------------------------------------------------------------
	// Done

	respCode = http.StatusOK
	response = SessionOutputChunkResponse{
		RestAPIBaseResponse: h.GetStdRESTSuccessMsg(r.Context()),
		ActualOffset:        readResult.ActualOffset,
		Read:                readResult.Read,
		Data:                base64.StdEncoding.EncodeToString(readResult.Data),
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
// @tags io
// @Produce text/event-stream
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param offset query int true "Read index position to start tailing from"
// @Param poll_period_msec query int false "Milliseconds between buffer data availability checks (default 250)"
// @Param strip_ansi query bool false "Strip ANSI escape sequences from the streamed data"
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
			log.
				WithError(err).
				WithFields(goutils.UpdateCodePositionInTags(logTags)).
				Error("Failed to form response")
		}
	}

	query := r.URL.Query()

	// ------------------------------------------------------------------------------------
	// Parse query parameters

	// Parse the starting offset
	rawOffset := query.Get("offset")
	if rawOffset == "" {
		msg := "Query parameter 'offset' is required"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		writeError(http.StatusBadRequest, msg, msg)
		return
	}
	offset, err := strconv.ParseInt(rawOffset, 10, 64)
	if err != nil {
		msg := "Query parameter 'offset' must be an integer"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		writeError(http.StatusBadRequest, msg, err.Error())
		return
	}
	if offset < 0 {
		msg := "Query parameter 'offset' must not be negative"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		writeError(http.StatusBadRequest, msg, msg)
		return
	}

	// Parse the optional poll period
	pollPeriod := defaultTailPollPeriod
	if raw := query.Get("poll_period_msec"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			msg := "Query parameter 'poll_period_msec' must be an integer"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			writeError(http.StatusBadRequest, msg, err.Error())
			return
		}
		if parsed < 1 {
			msg := "Query parameter 'poll_period_msec' must be at least 1"
			log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			writeError(http.StatusBadRequest, msg, msg)
			return
		}
		pollPeriod = time.Duration(parsed) * time.Millisecond
	}

	// Parse the optional ANSI-strip flag
	stripANSI := false
	if raw := query.Get("strip_ansi"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			msg := "Query parameter 'strip_ansi' must be a boolean"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			writeError(http.StatusBadRequest, msg, err.Error())
			return
		}
		stripANSI = parsed
	}

	// The SSE stream is written incrementally and flushed after every event, so the response
	// writer must support flushing.
	flusher, ok := w.(http.Flusher)
	if !ok {
		msg := "Streaming unsupported by this connection"
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		writeError(http.StatusInternalServerError, msg, msg)
		return
	}

	// ------------------------------------------------------------------------------------
	// Prepare the output ring buffer reader

	buffer, err := h.core.GetSessionOutputBuffer(r.Context(), sessionName)
	if err != nil {
		var unknownSession goutils.NotFoundError
		if errors.As(err, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
			writeError(http.StatusNotFound, msg, err.Error())
			return
		}
		msg := "Failed to get session '" + sessionName + "' output buffer"
		log.WithError(err).WithFields(goutils.UpdateCodePositionInTags(logTags)).Error(msg)
		writeError(http.StatusInternalServerError, msg, err.Error())
		return
	}

	// The reader's working context is derived from the request context, so a client
	// disconnect cancels it and the blocking Read returns io.EOF, ending the stream.
	reader := buffer.AsReadWriteCloser(r.Context(), offset, pollPeriod)
	defer func() {
		if err := reader.Close(); err != nil {
			log.
				WithError(err).
				WithFields(goutils.UpdateCodePositionInTags(logTags)).
				Error("Output stream reader close error")
		}
	}()

	// ------------------------------------------------------------------------------------
	// Start the SSE stream

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Debug("Started session output tail stream")
	defer func() {
		log.WithFields(goutils.UpdateCodePositionInTags(logTags)).Debug("Ended session output tail stream")
	}()

	readBuf := make([]byte, tailStreamReadChunkSize)
	for {
		n, readErr := reader.Read(readBuf)
		if n > 0 {
			chunk := readBuf[:n]
			if stripANSI {
				chunk = stripANSIEscapes(chunk)
			}
			encoded := base64.StdEncoding.EncodeToString(chunk)
			if _, err := fmt.Fprintf(w, "event: output\ndata: %s\n\n", encoded); err != nil {
				// The client connection is likely gone; stop streaming.
				log.
					WithError(err).
					WithFields(goutils.UpdateCodePositionInTags(logTags)).
					Debug("Output stream write failed; ending stream")
				return
			}
			flusher.Flush()
		}
		if readErr != nil {
			// io.EOF is the clean shutdown signal (context cancelled / client disconnect).
			if !errors.Is(readErr, io.EOF) {
				log.
					WithError(readErr).
					WithFields(goutils.UpdateCodePositionInTags(logTags)).
					Error("Output stream read failure")
			}
			return
		}
	}
}
