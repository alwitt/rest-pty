package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/api"
	"github.com/alwitt/rest-pty/db"
	mockdb "github.com/alwitt/rest-pty/mocks/db"
	mocktest "github.com/alwitt/rest-pty/mocks/test"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// sessionIOTestMocks bundles the mock collaborators the session IO handler is built on top of
type sessionIOTestMocks struct {
	persistence *mockdb.Client
	database    *mockdb.Database
	redis       *mocktest.RedisClientForTest
}

// newSessionIOTestMocks construct a fresh set of session IO mock collaborators bound to `t`.
func newSessionIOTestMocks(t *testing.T) *sessionIOTestMocks {
	return &sessionIOTestMocks{
		persistence: mockdb.NewClient(t),
		database:    mockdb.NewDatabase(t),
		redis:       mocktest.NewRedisClientForTest(t),
	}
}

/*
passthroughTransaction wires the persistence client so every `UseDatabaseInTransaction` simply runs
the supplied core logic against the database mock. Left open (no `.Times`) so it satisfies however
many transactions a given flow performs.
*/
func (m *sessionIOTestMocks) passthroughTransaction() {
	m.persistence.EXPECT().
		UseDatabaseInTransaction(mock.Anything, mock.Anything).
		RunAndReturn(func(
			ctx context.Context, coreLogic func(context.Context, db.Database) error,
		) error {
			return coreLogic(ctx, m.database)
		})
}

// buildSessionIOHandler helper function to streamline SessionIOHandler initialization
func buildSessionIOHandler(
	assert *assert.Assertions, mocks *sessionIOTestMocks,
) api.SessionIOHandler {
	uut, err := api.NewSessionIOHandler(
		mocks.persistence, mocks.redis, models.HTTPRequestLogging{
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

// ioSampleSession builds a representative session entry in a given state for IO handler tests
func ioSampleSession(name string, state models.SessionStateENUMType) models.Session {
	desc := "unit test session"
	return models.Session{
		ID:                   "session-id-" + name,
		Name:                 name,
		Description:          &desc,
		Command:              models.SessionCommand{Command: "bash", Arguments: []string{"-l"}},
		State:                state,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}
}

// ioJSONBody marshals `payload` into an HTTP request body reader
func ioJSONBody(assert *assert.Assertions, payload interface{}) *bytes.Reader {
	raw, err := json.Marshal(payload)
	assert.Nil(err)
	return bytes.NewReader(raw)
}

// ======================================================================================
// Session IO - Submit User Commands

func TestSessionIOHandlerSubmitUserCommand(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/io/input/commands"

	validCommands := func() api.UserCommandRequest {
		text := "ls -l"
		return api.UserCommandRequest{
			Commands: []models.SessionInputCommand{
				{Type: models.SessionInputCommandTypeText, Content: &text},
				{Type: models.SessionInputCommandTypeCR},
			},
		}
	}

	// runnerResponse builds an IPC response envelope carrying the universal response payload
	runnerResponse := func(success bool, errMsg *string) models.IPCMessageEnvelope {
		return models.IPCMessageRespUniversal{
			BaseIPCMessage: models.BaseIPCMessage{
				RequestID: "req-id",
				Type:      models.IPCMsgTypeRespRunCommands,
				Sender:    "unit-test-runner",
				Timestamp: time.Now().UTC(),
			},
			Success:  success,
			ErrorMsg: errMsg,
		}
	}

	/*
		expectIPCRoundTrip wires the request/response IPC queue handles and the response queue
		cleanup. The request queue (suffix ".ipc") accepts the pushed command; the response queue
		(prefix "req.") yields `runnerResp`.
	*/
	expectIPCRoundTrip := func(mocks *sessionIOTestMocks, runnerResp models.IPCMessageEnvelope) {
		reqQueue := mocktest.NewRedisQueueForTest(t)
		respQueue := mocktest.NewRedisQueueForTest(t)

		mocks.redis.EXPECT().
			GetQueueHandle(mock.Anything, mock.MatchedBy(func(name string) bool {
				return strings.HasSuffix(name, ".ipc")
			})).
			Return(reqQueue, nil).
			Once()
		mocks.redis.EXPECT().
			GetQueueHandle(mock.Anything, mock.MatchedBy(func(name string) bool {
				return strings.HasPrefix(name, "req.")
			})).
			Return(respQueue, nil).
			Once()
		mocks.redis.EXPECT().
			DeleteQueue(mock.Anything, mock.Anything).
			Return(nil).
			Once()

		reqQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Return(uint64(1), nil).
			Once()
		respQueue.EXPECT().
			PopLeft(mock.Anything, true, mock.Anything).
			Return(runnerResp, nil).
			Once()
	}

	// Case 0: submit commands successfully, runner reports success
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()
		expectIPCRoundTrip(testMocks, runnerResponse(true, nil))

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/test-session/io/input/commands",
			ioJSONBody(assert, validCommands()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 1: no payload at all is rejected
	t.Run("missing payload", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"POST", "/v1/sessions/test-session/io/input/commands", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 2: unparsable payload is rejected
	t.Run("malformed payload", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/test-session/io/input/commands",
			bytes.NewReader([]byte("{not-json")),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: empty command list fails validation
	t.Run("invalid commands", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/test-session/io/input/commands",
			ioJSONBody(assert, api.UserCommandRequest{Commands: []models.SessionInputCommand{}}),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 4: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "missing").
			Return(models.Session{}, goutils.NewNotFoundError("unknown", nil, false)).
			Once()

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/missing/io/input/commands",
			ioJSONBody(assert, validCommands()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})

	// Case 5: persistence failure surfaces as 500
	t.Run("persistence failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(models.Session{}, models.NewPersistenceError("dummy error", nil, false)).
			Once()

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/test-session/io/input/commands",
			ioJSONBody(assert, validCommands()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})

	// Case 6: session not in READY state yields 409
	t.Run("session not ready", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateIdle), nil).
			Once()

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/test-session/io/input/commands",
			ioJSONBody(assert, validCommands()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusConflict, respRecorder.Code)
	})

	// Case 7: runner reports failure for the commands -> 500
	t.Run("runner reports failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()
		runnerErr := "command rejected"
		expectIPCRoundTrip(testMocks, runnerResponse(false, &runnerErr))

		req, err := http.NewRequest(
			"POST",
			"/v1/sessions/test-session/io/input/commands",
			ioJSONBody(assert, validCommands()),
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.SubmitUserCommandToSession))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}

// ======================================================================================
// Session IO - Read One Output Chunk

func TestSessionIOHandlerReadSessionOutputChunk(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/io/output/chunk"

	// Case 0: read a chunk successfully
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()

		payload := []byte("hello world")
		buffer := mocktest.NewRedisBufferForTest(t)
		testMocks.redis.EXPECT().
			GetRingBuffer(mock.Anything, mock.Anything, int64(16384)).
			Return(buffer, nil).
			Once()
		buffer.EXPECT().
			ReadAt(mock.Anything, mock.Anything, int64(5)).
			RunAndReturn(func(_ context.Context, buf []byte, _ int64) (int64, int, int64, error) {
				n := copy(buf, payload)
				return 5, n, int64(len(payload)), nil
			}).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=5&limit=128", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
		var parsed api.SessionOutputChunkResponse
		assert.Nil(json.Unmarshal(respRecorder.Body.Bytes(), &parsed))
		assert.Equal(int64(5), parsed.ActualOffset)
		assert.Equal(len(payload), parsed.Read)
		decoded, err := base64.StdEncoding.DecodeString(parsed.Data)
		assert.Nil(err)
		assert.Equal(payload, decoded)
	})

	// Case 1: limit larger than buffer capacity is capped to capacity
	t.Run("limit capped to capacity", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()

		buffer := mocktest.NewRedisBufferForTest(t)
		testMocks.redis.EXPECT().
			GetRingBuffer(mock.Anything, mock.Anything, int64(16384)).
			Return(buffer, nil).
			Once()
		// The receive buffer is allocated at the capped length (16384), not the request's 99999.
		buffer.EXPECT().
			ReadAt(mock.Anything, mock.MatchedBy(func(buf []byte) bool {
				return len(buf) == 16384
			}), int64(0)).
			Return(int64(0), 0, int64(0), nil).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=0&limit=99999", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 2: missing offset is rejected
	t.Run("missing offset", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?limit=128", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: non-integer offset is rejected
	t.Run("invalid offset", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=abc&limit=128", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 4: negative offset is rejected
	t.Run("negative offset", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=-1&limit=128", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 5: missing limit is rejected
	t.Run("missing limit", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=0", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 6: limit below minimum is rejected
	t.Run("limit below minimum", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=0&limit=0", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 7: unknown session yields 404
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "missing").
			Return(models.Session{}, goutils.NewNotFoundError("unknown", nil, false)).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/missing/io/output/chunk?offset=0&limit=128", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})

	// Case 8: buffer read failure surfaces as 500
	t.Run("buffer read failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()

		buffer := mocktest.NewRedisBufferForTest(t)
		testMocks.redis.EXPECT().
			GetRingBuffer(mock.Anything, mock.Anything, int64(16384)).
			Return(buffer, nil).
			Once()
		buffer.EXPECT().
			ReadAt(mock.Anything, mock.Anything, int64(0)).
			Return(int64(0), 0, int64(0), models.NewPersistenceError("boom", nil, false)).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/chunk?offset=0&limit=128", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.ReadSessionOutputChunk))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}

// ======================================================================================
// Session IO - Tail The Output Stream

/*
scriptedReadWriteCloser is a fake io.ReadWriteCloser standing in for the ring buffer reader. Each
Read returns the next scripted chunk; once the chunks are exhausted it returns io.EOF, which is the
same clean-shutdown signal a client disconnect produces, so the streaming loop terminates on its
own. This lets the SSE handler be exercised end-to-end against an httptest recorder.
*/
type scriptedReadWriteCloser struct {
	chunks [][]byte
	idx    int
	closed bool
}

func (s *scriptedReadWriteCloser) Read(p []byte) (int, error) {
	if s.idx >= len(s.chunks) {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[s.idx])
	s.idx++
	return n, nil
}

func (s *scriptedReadWriteCloser) Write(p []byte) (int, error) { return len(p), nil }

func (s *scriptedReadWriteCloser) Close() error {
	s.closed = true
	return nil
}

func TestSessionIOHandlerTailSessionOutput(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	route := "/v1/sessions/{sessionName}/io/output/tail"

	// Case 0: tail the stream successfully, emitting one SSE event per chunk until EOF
	t.Run("success streams chunks", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()

		chunkA := []byte("first")
		chunkB := []byte("second")
		reader := &scriptedReadWriteCloser{chunks: [][]byte{chunkA, chunkB}}

		buffer := mocktest.NewRedisBufferForTest(t)
		testMocks.redis.EXPECT().
			GetRingBuffer(mock.Anything, mock.Anything, int64(16384)).
			Return(buffer, nil).
			Once()
		buffer.EXPECT().
			AsReadWriteCloser(mock.Anything, int64(0), mock.Anything).
			Return(reader).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/tail?offset=0", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
		assert.Equal("text/event-stream", respRecorder.Header().Get("Content-Type"))
		assert.True(reader.closed, "reader should be closed when the stream ends")

		body := respRecorder.Body.String()
		expectedA := "event: output\ndata: " + base64.StdEncoding.EncodeToString(chunkA) + "\n\n"
		expectedB := "event: output\ndata: " + base64.StdEncoding.EncodeToString(chunkB) + "\n\n"
		assert.Equal(expectedA+expectedB, body)
	})

	// Case 1: honors a custom poll period and forwards it to the buffer reader
	t.Run("custom poll period", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()

		reader := &scriptedReadWriteCloser{}
		buffer := mocktest.NewRedisBufferForTest(t)
		testMocks.redis.EXPECT().
			GetRingBuffer(mock.Anything, mock.Anything, int64(16384)).
			Return(buffer, nil).
			Once()
		buffer.EXPECT().
			AsReadWriteCloser(mock.Anything, int64(10), time.Millisecond*500).
			Return(reader).
			Once()

		req, err := http.NewRequest(
			"GET",
			"/v1/sessions/test-session/io/output/tail?offset=10&poll_period_msec=500",
			nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusOK, respRecorder.Code)
	})

	// Case 2: missing offset is rejected before the stream starts
	t.Run("missing offset", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest("GET", "/v1/sessions/test-session/io/output/tail", nil)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 3: non-integer offset is rejected
	t.Run("invalid offset", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/tail?offset=abc", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 4: invalid poll period is rejected
	t.Run("invalid poll period", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/tail?offset=0&poll_period_msec=0", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusBadRequest, respRecorder.Code)
	})

	// Case 5: unknown session yields 404 (still before the stream starts)
	t.Run("unknown session", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "missing").
			Return(models.Session{}, goutils.NewNotFoundError("unknown", nil, false)).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/missing/io/output/tail?offset=0", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusNotFound, respRecorder.Code)
	})

	// Case 6: failure obtaining the output buffer surfaces as 500
	t.Run("buffer acquisition failure", func(t *testing.T) {
		assert := assert.New(t)
		testMocks := newSessionIOTestMocks(t)
		uut := buildSessionIOHandler(assert, testMocks)

		testMocks.passthroughTransaction()
		testMocks.database.
			EXPECT().
			GetSessionByName(mock.Anything, "test-session").
			Return(ioSampleSession("test-session", models.SessionStateReady), nil).
			Once()
		testMocks.redis.EXPECT().
			GetRingBuffer(mock.Anything, mock.Anything, int64(16384)).
			Return(nil, models.NewPersistenceError("boom", nil, false)).
			Once()

		req, err := http.NewRequest(
			"GET", "/v1/sessions/test-session/io/output/tail?offset=0", nil,
		)
		assert.Nil(err)

		router := mux.NewRouter()
		respRecorder := httptest.NewRecorder()
		router.HandleFunc(route, uut.LoggingMiddleware(uut.TailSessionOutput))
		router.ServeHTTP(respRecorder, req)

		assert.Equal(http.StatusInternalServerError, respRecorder.Code)
	})
}
