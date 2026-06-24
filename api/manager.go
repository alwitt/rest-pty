// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/gorilla/mux"
)

// SessionManagerHandler session management API handlers
type SessionManagerHandler struct {
	goutils.RestAPIHandler
	validate *validator.Validate

	// persistence layer client
	persistence db.Client

	// manager of sessions
	manager session.Manager
}

/*
NewSessionManagerHandler define a new session manager REST API handler

	@param persistence db.Client - DB persistence layer
	@param manager session.Manager - session manager
	@param logConfig common.HTTPRequestLogging - handler log settings
	@param metrics goutils.HTTPRequestMetricHelper - metric collection agent
	@returns new handler
*/
func NewSessionManagerHandler(
	persistence db.Client,
	manager session.Manager,
	logConfig models.HTTPRequestLogging,
	metrics goutils.HTTPRequestMetricHelper,
) (SessionManagerHandler, error) {
	handler := SessionManagerHandler{
		RestAPIHandler: goutils.RestAPIHandler{
			Component: goutils.Component{
				LogTags: log.Fields{"module": "api", "component": "session-manager-handler"},
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
		manager:     manager,
	}

	if err := models.RegisterWithValidator(handler.validate); err != nil {
		return handler, goutils.NewRuntimeError(
			"failed to install custom validation macros", err, true,
		)
	}

	return handler, nil
}

// Alive godoc
// @Summary API liveness check
// @Description Will return success to indicate REST API module is live
// @tags util,management
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /liveness/alive [get]
func (h SessionManagerHandler) Alive(w http.ResponseWriter, r *http.Request) {
	logTags := h.GetLogTagsForContext(r.Context())
	if err := h.WriteRESTResponse(
		w, http.StatusOK, h.GetStdRESTSuccessMsg(r.Context()), nil,
	); err != nil {
		log.WithError(err).WithFields(logTags).Error("Failed to form response")
	}
}

// Ready godoc
// @Summary API readiness check
// @Description Will return success to indicate REST API module is ready
// @tags util,management
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /liveness/ready [get]
func (h SessionManagerHandler) Ready(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	if err := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(_ context.Context, dbClient db.Database) error {
			return dbClient.Ready()
		},
	); err != nil {
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(
			r.Context(), http.StatusInternalServerError, "not ready", err.Error(),
		)
	} else {
		respCode = http.StatusOK
		response = h.GetStdRESTSuccessMsg(r.Context())
	}
}

// ======================================================================================
// Session CRUD - Create Session

// NewSessionRequest parameters to define a new session
type NewSessionRequest struct {
	// Name session name, can only contain alphanumeric characters and -
	Name string `json:"name" validate:"required,session_name_type"`
	// Description session description
	Description *string `json:"description,omitempty" validate:"omitempty"`
	// Command the session will operate
	Command models.SessionCommand `json:"command" validate:"required"`
	// OutputBufferCapacity buffering capacity for holding command output history
	OutputBufferCapacity int64 `json:"io_buf_cap" validate:"required,gte=16384"`
	// DriverType indicate which driver the session uses
	DriverType models.SessionDriverTypeENUMType `json:"driver" validate:"required,session_driver_type"`
	// DriverMetadata metadata relating to the session driver
	DriverMetadata json.RawMessage `json:"driver_metadata,omitempty"`
}

// SessionEntryResponse response containing information for one session
type SessionEntryResponse struct {
	goutils.RestAPIBaseResponse
	// Session the session
	Session models.Session `json:"session" validate:"required"`
}

// DefineNewSession godoc
// @Summary Define a new session
// @Description Define a new session
// @tags management,session
// @Accept json
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param param body NewSessionRequest true "Session parameters"
// @Success 200 {object} SessionEntryResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions [post]
func (h SessionManagerHandler) DefineNewSession(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	if r.Body == nil {
		msg := "No payload provided to define new session"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the create parameters
	var params NewSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		msg := "Unable to parse new session parameters from request"
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

	{
		t, _ := json.Marshal(&params)
		log.WithFields(logTags).WithField("new-session", string(t)).Debug("Defining new session")
	}

	// Validate parameters
	if err := h.validate.Struct(&params); err != nil {
		msg := "New session parameters not valid"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Process driver metadata
	var driverMetadata interface{}
	switch params.DriverType {
	case models.SessionDriverTypePTY:
		var ptyDriverMetadata models.SessionDriverPTYParams
		if err := json.Unmarshal(params.DriverMetadata, &ptyDriverMetadata); err != nil {
			msg := "Unable to parse new session PTY driver parameters from request"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		if err := h.validate.Struct(&ptyDriverMetadata); err != nil {
			msg := "New session PTY driver parameters not valid"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		driverMetadata = ptyDriverMetadata

	case models.SessionDriverTypeDocker:
		var dockerDriverMetadata models.SessionDriverDockerParams
		if err := json.Unmarshal(params.DriverMetadata, &dockerDriverMetadata); err != nil {
			msg := "Unable to parse new session docker driver parameters from request"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		if err := h.validate.Struct(&dockerDriverMetadata); err != nil {
			msg := "New session docker driver parameters not valid"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		driverMetadata = dockerDriverMetadata

	default:
		msg := "New session driver type " + string(params.DriverType) + " not supported"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Define the session
	var newSession models.Session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			var err error
			newSession, err = dbClient.DefineNewSession(
				ctx,
				params.Name,
				params.Description,
				params.Command,
				params.OutputBufferCapacity,
				driverMetadata,
			)
			return err
		},
	); dbErr != nil {
		msg := "Failed to define new session"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		return
	}

	// Return new video source
	respCode = http.StatusOK
	response = SessionEntryResponse{
		RestAPIBaseResponse: h.GetStdRESTSuccessMsg(r.Context()), Session: newSession,
	}
}

// ======================================================================================
// Session CRUD - List Sessions

// SessionListResponse response containing a list of sessions
type SessionListResponse struct {
	goutils.RestAPIBaseResponse
	// Sessions the list of sessions
	Sessions []models.Session `json:"sessions,omitempty" validate:"omitempty,dive"`
}

// ListSessions godoc
// @Summary List sessions
// @Description List the known sessions, with optional filtering and ordering
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param name query string false "Filter by sessions whose name is similar to this, case insensitive"
// @Param offset query int false "Number of leading entries to skip"
// @Param limit query int false "Max number of entries to return"
// @Param driver query []string false "Filter by session driver type" collectionFormat(multi)
// @Param state query []string false "Filter by session state" collectionFormat(multi)
// @Param order_by_name query bool false "Whether to order the results by session name"
// @Param order_dir query string false "Ordering direction, defaults to 'ASC'" Enums(asc, desc, ASC, DESC)
// @Success 200 {object} SessionListResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions [get]
func (h SessionManagerHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	query := r.URL.Query()
	var filters db.SessionQueryFilter

	// Parse pagination parameters
	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			msg := "Query parameter 'offset' must be an integer"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		filters.Offset = &parsed
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			msg := "Query parameter 'limit' must be an integer"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		filters.Limit = &parsed
	}

	// Parse similar name filter
	if raw := query.Get("name"); raw != "" {
		filters.SimilarName = &raw
	}

	// Parse driver type filter list
	for _, entry := range query["driver"] {
		filters.TargetDriverType = append(
			filters.TargetDriverType, models.SessionDriverTypeENUMType(entry),
		)
	}

	// Parse state filter list
	for _, entry := range query["state"] {
		filters.TargetStates = append(
			filters.TargetStates, models.SessionStateENUMType(entry),
		)
	}

	// Parse ordering parameters
	if raw := query.Get("order_by_name"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			msg := "Query parameter 'order_by_name' must be a boolean"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		filters.OrderByName = parsed
	}
	if raw := query.Get("order_dir"); raw != "" {
		filters.OrderDirection = &raw
	} else {
		defaultDir := "ASC"
		filters.OrderDirection = &defaultDir
	}

	{
		t, _ := json.Marshal(&filters)
		log.WithFields(logTags).WithField("filters", string(t)).Debug("Listing sessions")
	}

	// Validate parameters
	if err := h.validate.Struct(&filters); err != nil {
		msg := "Session list query filters not valid"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Fetch the sessions
	var sessions []models.Session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessions, err = dbClient.ListSessions(ctx, filters)
			return err
		},
	); dbErr != nil {
		msg := "Failed to list sessions"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		return
	}

	// Return the sessions
	respCode = http.StatusOK
	response = SessionListResponse{
		RestAPIBaseResponse: h.GetStdRESTSuccessMsg(r.Context()), Sessions: sessions,
	}
}

// ======================================================================================
// Session CRUD - Get One Session

// GetSession godoc
// @Summary Get one session
// @Description Fetch one session by its name
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Success 200 {object} SessionEntryResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName} [get]
func (h SessionManagerHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Fetch the session
	var session models.Session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			var err error
			session, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
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

	// Return the session
	respCode = http.StatusOK
	response = SessionEntryResponse{
		RestAPIBaseResponse: h.GetStdRESTSuccessMsg(r.Context()), Session: session,
	}
}

// ======================================================================================
// Session CRUD - Update Session Output Buffer Capacity

// UpdateSessionOutputBufCapacity godoc
// @Summary Change session output buffer capacity
// @Description Change the output buffer capacity of a session. Only permitted on IDLE sessions.
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param capacity query int true "New output buffer capacity"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/output-buf-cap [put]
func (h SessionManagerHandler) UpdateSessionOutputBufCapacity(
	w http.ResponseWriter, r *http.Request,
) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Parse the new capacity
	raw := r.URL.Query().Get("capacity")
	if raw == "" {
		msg := "Query parameter 'capacity' is required"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	newCap, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		msg := "Query parameter 'capacity' must be an integer"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	if newCap < 16384 {
		msg := "Query parameter 'capacity' must be at least 16384"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Apply the new capacity
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionOutputBufCapacity(ctx, sessionName, newCap)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		switch {
		case errors.As(dbErr, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		case errors.As(dbErr, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing this change"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		default:
			msg := "Failed to update output buffer capacity for session '" + sessionName + "'"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Update Session Run Mode

// UpdateSessionRunMode godoc
// @Summary Change session runner mode
// @Description Change the runner mode of a session. Only permitted on IDLE sessions.
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param mode query string true "New session runner mode" Enums(COMMANDED, BY_PASSED)
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/run-mode [put]
func (h SessionManagerHandler) UpdateSessionRunMode(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Parse the new runner mode
	raw := r.URL.Query().Get("mode")
	if raw == "" {
		msg := "Query parameter 'mode' is required"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	if err := h.validate.Var(raw, "session_runner_mode_type"); err != nil {
		msg := "Query parameter 'mode' is not a valid session runner mode"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}
	newMode := models.SessionRunnerModeTypeENUMType(raw)

	// Apply the new runner mode
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionRunMode(ctx, sessionName, newMode)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		switch {
		case errors.As(dbErr, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		case errors.As(dbErr, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing this change"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		default:
			msg := "Failed to update runner mode for session '" + sessionName + "'"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Update Session Command

// UpdateSessionCommand godoc
// @Summary Change session command
// @Description Change the command a session runs. Only permitted on IDLE sessions.
// @tags management,session
// @Accept json
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param param body models.SessionCommand true "New session command"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/command [put]
func (h SessionManagerHandler) UpdateSessionCommand(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	if r.Body == nil {
		msg := "No payload provided to update session command"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the new command
	var newCommand models.SessionCommand
	if err := json.NewDecoder(r.Body).Decode(&newCommand); err != nil {
		msg := "Unable to parse new session command from request"
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

	{
		t, _ := json.Marshal(&newCommand)
		log.WithFields(logTags).WithField("new-command", string(t)).Debug("Updating session command")
	}

	// Validate parameters
	if err := h.validate.Struct(&newCommand); err != nil {
		msg := "New session command not valid"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Apply the new command
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionCommand(ctx, sessionName, newCommand)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		switch {
		case errors.As(dbErr, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		case errors.As(dbErr, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing this change"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		default:
			msg := "Failed to update command for session '" + sessionName + "'"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Update Session Driver

// UpdateSessionDriverRequest parameters to change a session driver
type UpdateSessionDriverRequest struct {
	// DriverType indicate which driver the session uses
	DriverType models.SessionDriverTypeENUMType `json:"driver" validate:"required,session_driver_type"`
	// DriverMetadata metadata relating to the session driver
	DriverMetadata json.RawMessage `json:"driver_metadata,omitempty"`
}

// UpdateSessionDriver godoc
// @Summary Change session driver
// @Description Change the driver a session uses, along with its setup metadata. Only permitted on IDLE sessions.
// @tags management,session
// @Accept json
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param param body UpdateSessionDriverRequest true "New session driver parameters"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/driver [put]
func (h SessionManagerHandler) UpdateSessionDriver(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	if r.Body == nil {
		msg := "No payload provided to update session driver"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the new driver parameters
	var params UpdateSessionDriverRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		msg := "Unable to parse new session driver parameters from request"
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

	{
		t, _ := json.Marshal(&params)
		log.WithFields(logTags).WithField("new-driver", string(t)).Debug("Updating session driver")
	}

	// Validate parameters
	if err := h.validate.Struct(&params); err != nil {
		msg := "New session driver parameters not valid"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Process driver metadata
	var driverMetadata interface{}
	switch params.DriverType {
	case models.SessionDriverTypePTY:
		var ptyDriverMetadata models.SessionDriverPTYParams
		if err := json.Unmarshal(params.DriverMetadata, &ptyDriverMetadata); err != nil {
			msg := "Unable to parse new session PTY driver parameters from request"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		if err := h.validate.Struct(&ptyDriverMetadata); err != nil {
			msg := "New session PTY driver parameters not valid"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		driverMetadata = ptyDriverMetadata

	default:
		msg := "New session driver type " + string(params.DriverType) + " not supported"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Apply the new driver
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(ctx, sessionName, driverMetadata)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		var validation goutils.ValidationError
		switch {
		case errors.As(dbErr, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		case errors.As(dbErr, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing this change"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		case errors.As(dbErr, &validation):
			msg := "New session driver parameters not valid"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		default:
			msg := "Failed to update driver for session '" + sessionName + "'"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Update Session Name

// UpdateSessionName godoc
// @Summary Change session name
// @Description Change the name of a session
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param name query string true "New session name, can only contain alphanumeric characters and -"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/name [put]
func (h SessionManagerHandler) UpdateSessionName(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Parse the new name
	newName := r.URL.Query().Get("name")
	if newName == "" {
		msg := "Query parameter 'name' is required"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}
	if err := h.validate.Var(newName, "session_name_type"); err != nil {
		msg := "Query parameter 'name' is not a valid session name"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Apply the new name
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionName(ctx, sessionName, newName)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		if errors.As(dbErr, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
			return
		}
		msg := "Failed to update name for session '" + sessionName + "'"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Update Session Description

// UpdateSessionDescriptionRequest parameters to change a session description
type UpdateSessionDescriptionRequest struct {
	// Description new session description, set to null to clear
	Description *string `json:"description" validate:"omitempty"`
}

// UpdateSessionDescription godoc
// @Summary Change session description
// @Description Change the description of a session
// @tags management,session
// @Accept json
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param param body UpdateSessionDescriptionRequest true "New session description"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/description [put]
func (h SessionManagerHandler) UpdateSessionDescription(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	if r.Body == nil {
		msg := "No payload provided to update session description"
		log.WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, msg)
		return
	}

	// Parse the new description
	var params UpdateSessionDescriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		msg := "Unable to parse new session description from request"
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

	{
		t, _ := json.Marshal(&params)
		log.WithFields(logTags).
			WithField("new-description", string(t)).Debug("Updating session description")
	}

	// Validate parameters
	if err := h.validate.Struct(&params); err != nil {
		msg := "New session description not valid"
		log.WithError(err).WithFields(logTags).Error(msg)
		respCode = http.StatusBadRequest
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		return
	}

	// Apply the new description
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDescription(ctx, sessionName, params.Description)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		if errors.As(dbErr, &unknownSession) {
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
			return
		}
		msg := "Failed to update description for session '" + sessionName + "'"
		log.WithError(dbErr).WithFields(logTags).Error(msg)
		respCode = http.StatusInternalServerError
		response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Delete Session

// DeleteSession godoc
// @Summary Delete a session
// @Description Delete a session. Only permitted on IDLE sessions.
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName} [delete]
func (h SessionManagerHandler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Delete the session
	if dbErr := h.persistence.UseDatabaseInTransaction(
		r.Context(), func(ctx context.Context, dbClient db.Database) error {
			return dbClient.DeleteSession(ctx, sessionName)
		},
	); dbErr != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		switch {
		case errors.As(dbErr, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		case errors.As(dbErr, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing deletion"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		default:
			msg := "Failed to delete session '" + sessionName + "'"
			log.WithError(dbErr).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, dbErr.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Start Session

// StartSession godoc
// @Summary Start a session
// @Description Bring up a session runner for an existing session and start it. By default
// @Description the request to the session manager is non-blocking; set "block" to true to
// @Description wait for the start to complete.
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param block query bool false "Whether to block until the start completes"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/start [post]
func (h SessionManagerHandler) StartSession(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Parse the optional blocking flag
	blocking := false
	if raw := r.URL.Query().Get("block"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			msg := "Invalid 'block' query parameter"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		blocking = parsed
	}

	// Start the session
	if err := h.manager.StartSession(r.Context(), sessionName, blocking); err != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		switch {
		case errors.As(err, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		case errors.As(err, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing start"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		default:
			msg := "Failed to start session '" + sessionName + "'"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}

// ======================================================================================
// Session CRUD - Stop Session

// StopSession godoc
// @Summary Stop a session
// @Description Bring a session back to IDLE and unload its runner. By default the request
// @Description to the session manager is non-blocking; set "block" to true to wait for the
// @Description stop to complete.
// @tags management,session
// @Produce json
// @Param X-Request-ID header string false "Request ID"
// @Param sessionName path string true "Session name"
// @Param block query bool false "Whether to block until the stop completes"
// @Success 200 {object} goutils.RestAPIBaseResponse "success"
// @Failure 400 {object} goutils.RestAPIBaseResponse "error"
// @Failure 403 {object} goutils.RestAPIBaseResponse "error"
// @Failure 404 {string} string "error"
// @Failure 409 {object} goutils.RestAPIBaseResponse "error"
// @Failure 500 {object} goutils.RestAPIBaseResponse "error"
// @Router /v1/sessions/{sessionName}/stop [post]
func (h SessionManagerHandler) StopSession(w http.ResponseWriter, r *http.Request) {
	var respCode int
	var response interface{}
	logTags := h.GetLogTagsForContext(r.Context())
	defer func() {
		if err := h.WriteRESTResponse(w, respCode, response, nil); err != nil {
			log.WithError(err).WithFields(logTags).Error("Failed to form response")
		}
	}()

	sessionName := mux.Vars(r)["sessionName"]

	// Parse the optional blocking flag
	blocking := false
	if raw := r.URL.Query().Get("block"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			msg := "Invalid 'block' query parameter"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusBadRequest
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
			return
		}
		blocking = parsed
	}

	// Stop the session
	if err := h.manager.StopSession(r.Context(), sessionName, blocking); err != nil {
		var unknownSession goutils.NotFoundError
		var consistency goutils.ConsistencyError
		switch {
		case errors.As(err, &unknownSession):
			msg := "No session '" + sessionName + "' found"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusNotFound
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		case errors.As(err, &consistency):
			msg := "Session '" + sessionName + "' is not in a state allowing stop"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusConflict
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		default:
			msg := "Failed to stop session '" + sessionName + "'"
			log.WithError(err).WithFields(logTags).Error(msg)
			respCode = http.StatusInternalServerError
			response = h.GetStdRESTErrorMsg(r.Context(), respCode, msg, err.Error())
		}
		return
	}

	// Report success
	respCode = http.StatusOK
	response = h.GetStdRESTSuccessMsg(r.Context())
}
