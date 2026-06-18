package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/api"
	"github.com/alwitt/rest-pty/db"
	mockdb "github.com/alwitt/rest-pty/mocks/db"
	mocksession "github.com/alwitt/rest-pty/mocks/session"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// apiHandlerTestMocks bundles the mock collaborators a REST API handler is built on top of
type apiHandlerTestMocks struct {
	persistence *mockdb.Client
	database    *mockdb.Database
	manager     *mocksession.Manager
}

// newAPIHandlerTestMocks construct a fresh set of manager mock collaborators bound to `t`.
func newAPIHandlerTestMocks(t *testing.T) *apiHandlerTestMocks {
	return &apiHandlerTestMocks{
		persistence: mockdb.NewClient(t),
		database:    mockdb.NewDatabase(t),
		manager:     mocksession.NewManager(t),
	}
}

/*
passthroughTransaction wires the persistence client so every `UseDatabaseInTransaction` simply runs
the supplied core logic against the database mock. Left open (no `.Times`) so it satisfies however
many transactions a given flow performs.
*/
func (m *apiHandlerTestMocks) passthroughTransaction() {
	m.persistence.EXPECT().
		UseDatabaseInTransaction(mock.Anything, mock.Anything).
		RunAndReturn(func(
			ctx context.Context, coreLogic func(context.Context, db.Database) error,
		) error {
			return coreLogic(ctx, m.database)
		})
}

// buildSessionManagerHandler helper function to streamline SessionManagerHandler initialization
func buildSessionManagerHandler(
	assert *assert.Assertions, mocks *apiHandlerTestMocks,
) api.SessionManagerHandler {
	uut, err := api.NewSessionManagerHandler(
		mocks.persistence, mocks.manager, models.HTTPRequestLogging{
			LogLevel:        goutils.HTTPLogLevelWARN,
			HealthLogLevel:  goutils.HTTPLogLevelWARN,
			RequestIDHeader: "unit-test",
			DoNotLogHeaders: []string{},
		}, nil,
	)
	assert.Nil(err)
	assert.NotNil(uut)
	return uut
}

func TestSessionManagerHandlerLivenessAPIs(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	// Case 0: Alive endpoint is always returning success
	t.Run("alive endpoint always success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest("GET", "/v1/alive", nil)
		assert.Nil(err)

		// Setup HTTP handling
		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/alive", uut.LoggingMiddleware(uut.Alive))

		// Request
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: Ready endpoint return success
	t.Run("ready endpoint return success on DB ready", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.EXPECT().Ready().Return(nil).Once()

		req, err := http.NewRequest("GET", "/v1/ready", nil)
		assert.Nil(err)

		// Setup HTTP handling
		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/ready", uut.LoggingMiddleware(uut.Ready))

		// Request
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 0: Ready endpoint return failure
	t.Run("ready endpoint return success on DB not ready", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			Ready().
			Return(models.PersistenceError{Message: "dummy error"}).
			Once()

		req, err := http.NewRequest("GET", "/v1/ready", nil)
		assert.Nil(err)

		// Setup HTTP handling
		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/ready", uut.LoggingMiddleware(uut.Ready))

		// Request
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}

// sampleSession builds a representative session entry for response assertions
func sampleSession(name string) models.Session {
	desc := "unit test session"
	return models.Session{
		ID:                   "session-id-" + name,
		Name:                 name,
		Description:          &desc,
		Command:              models.SessionCommand{Command: "bash", Arguments: []string{"-l"}},
		State:                models.SessionStateIdle,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}
}

// jsonBody marshals `payload` into an HTTP request body reader
func jsonBody(assert *assert.Assertions, payload interface{}) *bytes.Reader {
	raw, err := json.Marshal(payload)
	assert.Nil(err)
	return bytes.NewReader(raw)
}

func TestSessionManagerHandlerDefineNewSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	validRequest := func() api.NewSessionRequest {
		desc := "unit test session"
		return api.NewSessionRequest{
			Name:                 "test-session",
			Description:          &desc,
			Command:              models.SessionCommand{Command: "bash", Arguments: []string{"-l"}},
			OutputBufferCapacity: 16384,
			DriverType:           models.SessionDriverTypePTY,
			DriverMetadata: json.RawMessage(
				`{"display_rows": 40, "display_cols": 120}`,
			),
		}
	}

	// Case 0: define a new session successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		expected := sampleSession("test-session")
		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			DefineNewSession(
				mock.Anything,
				"test-session",
				mock.Anything,
				mock.Anything,
				int64(16384),
				mock.Anything,
			).
			Return(expected, nil).
			Once()

		req, err := http.NewRequest(
			"POST", "/v1/sessions", jsonBody(assert, validRequest()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.DefineNewSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
		var parsed api.SessionEntryResponse
		assert.Nil(json.Unmarshal(respRecorder.Body.Bytes(), &parsed))
		assert.Equal(expected.Name, parsed.Session.Name)
	})

	// Case 1: unparsable payload is rejected
	t.Run("malformed payload", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"POST", "/v1/sessions", bytes.NewReader([]byte("{not-json")),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.DefineNewSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: invalid parameters (bad name) fail validation
	t.Run("invalid parameters", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		bad := validRequest()
		bad.Name = "not a valid name!"

		req, err := http.NewRequest("POST", "/v1/sessions", jsonBody(assert, bad))
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.DefineNewSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: unsupported driver type is rejected
	t.Run("unsupported driver", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		bad := validRequest()
		bad.DriverType = models.SessionDriverTypeDocker

		req, err := http.NewRequest("POST", "/v1/sessions", jsonBody(assert, bad))
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.DefineNewSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 4: persistence failure surfaces as 500
	t.Run("persistence failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			DefineNewSession(
				mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything,
			).
			Return(models.Session{}, models.PersistenceError{Message: "dummy error"}).
			Once()

		req, err := http.NewRequest(
			"POST", "/v1/sessions", jsonBody(assert, validRequest()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.DefineNewSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}

func TestSessionManagerHandlerListSessions(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	// Case 0: list sessions successfully, with filters parsed off the query string
	t.Run("success with filters", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		expected := []models.Session{sampleSession("alpha"), sampleSession("beta")}
		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			ListSessions(mock.Anything, mock.MatchedBy(func(f db.SessionQueryFilter) bool {
				return f.Offset != nil && *f.Offset == 5 &&
					f.Limit != nil && *f.Limit == 10 &&
					f.SimilarName != nil && *f.SimilarName == "alp" &&
					len(f.TargetDriverType) == 1 && f.TargetDriverType[0] == models.SessionDriverTypePTY &&
					len(f.TargetStates) == 1 && f.TargetStates[0] == models.SessionStateIdle &&
					f.OrderByName
			})).
			Return(expected, nil).
			Once()

		req, err := http.NewRequest(
			"GET",
			"/v1/sessions?offset=5&limit=10&name=alp&driver=PTY&state=IDLE&order_by_name=true",
			nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.ListSessions))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
		var parsed api.SessionListResponse
		assert.Nil(json.Unmarshal(respRecorder.Body.Bytes(), &parsed))
		assert.Len(parsed.Sessions, 2)
	})

	// Case 1: non-integer pagination parameter is rejected
	t.Run("invalid offset", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest("GET", "/v1/sessions?offset=abc", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.ListSessions))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: non-boolean order_by_name is rejected
	t.Run("invalid order_by_name", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest("GET", "/v1/sessions?order_by_name=maybe", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.ListSessions))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: persistence failure surfaces as 500
	t.Run("persistence failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return(nil, models.PersistenceError{Message: "dummy error"}).
			Once()

		req, err := http.NewRequest("GET", "/v1/sessions", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc("/v1/sessions", uut.LoggingMiddleware(uut.ListSessions))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}

func TestSessionManagerHandlerGetSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	// Case 0: fetch one session successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		expected := sampleSession("test-session")
		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(expected, nil).
			Once()

		req, err := http.NewRequest("GET", "/v1/sessions/test-session", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(
			"/v1/sessions/{sessionName}", uut.LoggingMiddleware(uut.GetSession),
		)
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
		var parsed api.SessionEntryResponse
		assert.Nil(json.Unmarshal(respRecorder.Body.Bytes(), &parsed))
		assert.Equal(expected.Name, parsed.Session.Name)
	})

	// Case 1: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "missing").
			Return(models.Session{}, models.UnknownSessionError{Message: "unknown"}).
			Once()

		req, err := http.NewRequest("GET", "/v1/sessions/missing", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(
			"/v1/sessions/{sessionName}", uut.LoggingMiddleware(uut.GetSession),
		)
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})

	// Case 2: persistence failure surfaces as 500
	t.Run("persistence failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(models.Session{}, models.PersistenceError{Message: "dummy error"}).
			Once()

		req, err := http.NewRequest("GET", "/v1/sessions/test-session", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(
			"/v1/sessions/{sessionName}", uut.LoggingMiddleware(uut.GetSession),
		)
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}

func TestSessionManagerHandlerUpdateSessionOutputBufCapacity(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/output-buf-cap"

	// Case 0: update capacity successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionOutputBufCapacity(mock.Anything, "test-session", int64(32768)).
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/output-buf-cap?capacity=32768", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionOutputBufCapacity))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: missing capacity query param is rejected
	t.Run("missing capacity", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/output-buf-cap", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionOutputBufCapacity))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: capacity below the minimum is rejected
	t.Run("capacity below minimum", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/output-buf-cap?capacity=1024", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionOutputBufCapacity))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionOutputBufCapacity(mock.Anything, "missing", int64(32768)).
			Return(models.UnknownSessionError{Message: "unknown"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/missing/output-buf-cap?capacity=32768", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionOutputBufCapacity))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})

	// Case 4: session in wrong state yields 409
	t.Run("consistency error", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionOutputBufCapacity(mock.Anything, "test-session", int64(32768)).
			Return(models.ConsistencyError{Message: "not idle"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/output-buf-cap?capacity=32768", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionOutputBufCapacity))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusConflict, respRecorder.Code)
	})
}

func TestSessionManagerHandlerUpdateSessionRunMode(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/run-mode"

	// Case 0: update runner mode successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionRunMode(
				mock.Anything, "test-session", models.SessionRunnerModeTypeByPassed,
			).
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/run-mode?mode=BY_PASSED", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionRunMode))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: missing mode query param is rejected
	t.Run("missing mode", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest("PUT", "/v1/sessions/test-session/run-mode", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionRunMode))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: invalid mode value is rejected
	t.Run("invalid mode", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/run-mode?mode=NONSENSE", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionRunMode))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: session in wrong state yields 409
	t.Run("consistency error", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionRunMode(
				mock.Anything, "test-session", models.SessionRunnerModeTypeByPassed,
			).
			Return(models.ConsistencyError{Message: "not idle"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/run-mode?mode=BY_PASSED", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionRunMode))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusConflict, respRecorder.Code)
	})
}

func TestSessionManagerHandlerUpdateSessionCommand(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/command"
	validCommand := models.SessionCommand{Command: "bash", Arguments: []string{"-l"}}

	// Case 0: update command successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionCommand(mock.Anything, "test-session", validCommand).
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/command", jsonBody(assert, validCommand),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionCommand))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: unparsable payload is rejected
	t.Run("malformed payload", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/command", bytes.NewReader([]byte("{bad")),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionCommand))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: invalid command (empty cmd) fails validation
	t.Run("invalid command", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT",
			"/v1/sessions/test-session/command",
			jsonBody(assert, models.SessionCommand{Arguments: []string{"-l"}}),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionCommand))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionCommand(mock.Anything, "missing", validCommand).
			Return(models.UnknownSessionError{Message: "unknown"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/missing/command", jsonBody(assert, validCommand),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionCommand))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})
}

func TestSessionManagerHandlerUpdateSessionDriver(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/driver"
	validRequest := func() api.UpdateSessionDriverRequest {
		return api.UpdateSessionDriverRequest{
			DriverType: models.SessionDriverTypePTY,
			DriverMetadata: json.RawMessage(
				`{"display_rows": 40, "display_cols": 120}`,
			),
		}
	}

	// Case 0: update driver successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionDriver(mock.Anything, "test-session", mock.Anything).
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/driver", jsonBody(assert, validRequest()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDriver))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: invalid driver metadata (rows below minimum) fails validation
	t.Run("invalid driver metadata", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		bad := validRequest()
		bad.DriverMetadata = json.RawMessage(`{"display_rows": 1, "display_cols": 120}`)

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/driver", jsonBody(assert, bad),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDriver))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: unsupported driver type is rejected
	t.Run("unsupported driver", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		bad := validRequest()
		bad.DriverType = models.SessionDriverTypeDocker

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/driver", jsonBody(assert, bad),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDriver))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: persistence validation error yields 400
	t.Run("validation error from persistence", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionDriver(mock.Anything, "test-session", mock.Anything).
			Return(models.ValidationError{Message: "bad driver params"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/driver", jsonBody(assert, validRequest()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDriver))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 4: session in wrong state yields 409
	t.Run("consistency error", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionDriver(mock.Anything, "test-session", mock.Anything).
			Return(models.ConsistencyError{Message: "not idle"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/driver", jsonBody(assert, validRequest()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDriver))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusConflict, respRecorder.Code)
	})
}

func TestSessionManagerHandlerUpdateSessionName(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/name"

	// Case 0: update name successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionName(mock.Anything, "test-session", "renamed-session").
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/name?name=renamed-session", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionName))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: missing name query param is rejected
	t.Run("missing name", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest("PUT", "/v1/sessions/test-session/name", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionName))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: invalid new name is rejected
	t.Run("invalid name", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/test-session/name?name=not%20valid!", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionName))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionName(mock.Anything, "missing", "renamed-session").
			Return(models.UnknownSessionError{Message: "unknown"}).
			Once()

		req, err := http.NewRequest(
			"PUT", "/v1/sessions/missing/name?name=renamed-session", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionName))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})
}

func TestSessionManagerHandlerUpdateSessionDescription(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/description"

	// Case 0: update description successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		newDesc := "an updated description"
		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionDescription(
				mock.Anything, "test-session", mock.MatchedBy(func(d *string) bool {
					return d != nil && *d == newDesc
				}),
			).
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT",
			"/v1/sessions/test-session/description",
			jsonBody(assert, api.UpdateSessionDescriptionRequest{Description: &newDesc}),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDescription))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: null description clears it successfully
	t.Run("clear description", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionDescription(
				mock.Anything, "test-session", mock.MatchedBy(func(d *string) bool {
					return d == nil
				}),
			).
			Return(nil).
			Once()

		req, err := http.NewRequest(
			"PUT",
			"/v1/sessions/test-session/description",
			bytes.NewReader([]byte(`{"description": null}`)),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDescription))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 2: unparsable payload is rejected
	t.Run("malformed payload", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		req, err := http.NewRequest(
			"PUT",
			"/v1/sessions/test-session/description",
			bytes.NewReader([]byte("{bad")),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDescription))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		newDesc := "an updated description"
		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			UpdateSessionDescription(mock.Anything, "missing", mock.Anything).
			Return(models.UnknownSessionError{Message: "unknown"}).
			Once()

		req, err := http.NewRequest(
			"PUT",
			"/v1/sessions/missing/description",
			jsonBody(assert, api.UpdateSessionDescriptionRequest{Description: &newDesc}),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.UpdateSessionDescription))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})
}

func TestSessionManagerHandlerDeleteSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}"

	// Case 0: delete session successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			DeleteSession(mock.Anything, "test-session").
			Return(nil).
			Once()

		req, err := http.NewRequest("DELETE", "/v1/sessions/test-session", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.DeleteSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			DeleteSession(mock.Anything, "missing").
			Return(models.UnknownSessionError{Message: "unknown"}).
			Once()

		req, err := http.NewRequest("DELETE", "/v1/sessions/missing", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.DeleteSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})

	// Case 2: session in wrong state yields 409
	t.Run("consistency error", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			DeleteSession(mock.Anything, "test-session").
			Return(models.ConsistencyError{Message: "not idle"}).
			Once()

		req, err := http.NewRequest("DELETE", "/v1/sessions/test-session", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.DeleteSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusConflict, respRecorder.Code)
	})

	// Case 3: persistence failure surfaces as 500
	t.Run("persistence failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newAPIHandlerTestMocks(t)
		uut := buildSessionManagerHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			DeleteSession(mock.Anything, "test-session").
			Return(models.PersistenceError{Message: "dummy error"}).
			Once()

		req, err := http.NewRequest("DELETE", "/v1/sessions/test-session", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.DeleteSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}
