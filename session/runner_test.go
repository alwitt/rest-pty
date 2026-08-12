package session_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/alwitt/goutils"
	mockGoutils "github.com/alwitt/goutils/mocks/goutils"
	mockRedis "github.com/alwitt/goutils/mocks/redis"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/db"
	mockdb "github.com/alwitt/rest-pty/mocks/db"
	mocksession "github.com/alwitt/rest-pty/mocks/session"
	mocktest "github.com/alwitt/rest-pty/mocks/test"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/apex/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// runnerTestMocks bundles the mock collaborators a session runner is built on top of.
type runnerTestMocks struct {
	persistence  *mockdb.Client
	database     *mockdb.Database
	driver       *mocksession.Driver
	redisClient  *mockRedis.Client
	worker       *mockGoutils.TaskProcessor
	dummyManager *mocktest.ForTextSessionManager
}

// newRunnerTestMocks construct a fresh set of runner mock collaborators bound to `t`.
func newRunnerTestMocks(t *testing.T) *runnerTestMocks {
	return &runnerTestMocks{
		persistence:  mockdb.NewClient(t),
		database:     mockdb.NewDatabase(t),
		driver:       mocksession.NewDriver(t),
		redisClient:  mockRedis.NewClient(t),
		worker:       mockGoutils.NewTaskProcessor(t),
		dummyManager: mocktest.NewForTextSessionManager(t),
	}
}

/*
constructParams build a happy-path `NewSessionRunnerParams`, with each factory returning the
corresponding mock. Expectations on those mocks are set by each test case as needed.
*/
func (m *runnerTestMocks) constructParams(sessionName string) session.NewSessionRunnerParams {
	return session.NewSessionRunnerParams{
		SessionName: sessionName,
		PersistenceFactory: func() (db.Client, error) {
			return m.persistence, nil
		},
		RedisClient: m.redisClient,
		DriverFactory: func(
			_ context.Context, _ models.Session, _ goutilsRedis.Client, _ func(),
		) (session.Driver, error) {
			return m.driver, nil
		},
		WorkerFactory: func(
			_ context.Context,
			_ string,
			_ int,
			_ log.Fields,
			_ goutils.TaskProcessorMetricHelper,
		) (goutils.TaskProcessor, error) {
			return m.worker, nil
		},
		SessionIdleNotify:     m.dummyManager.SessionIdleNotify,
		IPCProcessErrorNotify: m.dummyManager.IPCProcessErrorNotify,
	}
}

/*
buildRunner construct a ready-to-use runner backed by the mocks, wiring the construction-time
expectations (session load + handler registration). The transaction and `GetSessionByName`
expectations are intentionally left open (no `.Times`) so they also satisfy the calls a handler
makes after construction.
*/
func (m *runnerTestMocks) buildRunner(
	ctx context.Context, t *testing.T, sess models.Session,
) session.Runner {
	assert := assert.New(t)

	m.database.EXPECT().GetSessionByName(mock.Anything, sess.Name).Return(sess, nil)
	m.persistence.EXPECT().
		UseDatabaseInTransaction(mock.Anything, mock.Anything).
		RunAndReturn(func(
			ctx context.Context, coreLogic func(context.Context, db.Database) error,
		) error {
			return coreLogic(ctx, m.database)
		})
	// The three request handlers (start, stop, submit-commands) get registered at construction
	m.worker.EXPECT().AddToTaskExecutionMap(mock.Anything, mock.Anything).Return(nil).Times(3)

	uut, err := session.NewSessionRunner(ctx, m.constructParams(sess.Name))
	assert.Nil(err)
	assert.NotNil(uut)
	return uut
}

// TestSessionRunnerConstruct verifies the behavior of `NewSessionRunner`.
func TestSessionRunnerConstruct(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateIdle,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// expectSessionLoad wires the persistence layer so the runner can load the session: the
	// transaction callback is invoked against the database mock, which returns `result`/`err`.
	expectSessionLoad := func(m *runnerTestMocks, result models.Session, err error) {
		m.database.EXPECT().GetSessionByName(mock.Anything, sessionName).Return(result, err)
		m.persistence.EXPECT().
			UseDatabaseInTransaction(mock.Anything, mock.Anything).
			RunAndReturn(func(
				ctx context.Context, coreLogic func(context.Context, db.Database) error,
			) error {
				return coreLogic(ctx, m.database)
			})
	}

	// Case 0: initialization parameters fail validation
	t.Run("param validation failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		// An empty session name fails the `required,session_name_type` constraint
		params := m.constructParams("")

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var validationErr goutils.ValidationError
		assert.True(errors.As(err, &validationErr))
	})

	// Case 1: the persistence factory fails
	t.Run("persistence factory failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		params := m.constructParams(sessionName)
		params.PersistenceFactory = func() (db.Client, error) {
			return nil, fmt.Errorf("persistence factory boom")
		}

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 2: loading the session from persistence fails
	t.Run("session load failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		loadErr := goutils.NewNotFoundError("no such session", nil, false)
		expectSessionLoad(m, models.Session{}, loadErr)

		params := m.constructParams(sessionName)

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		// The transaction error is surfaced as-is
		var unknownErr goutils.NotFoundError
		assert.True(errors.As(err, &unknownErr))
	})

	// Case 3: the driver factory fails
	t.Run("driver factory failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		expectSessionLoad(m, testSession, nil)

		params := m.constructParams(sessionName)
		params.DriverFactory = func(
			_ context.Context, _ models.Session, _ goutilsRedis.Client, _ func(),
		) (session.Driver, error) {
			return nil, fmt.Errorf("driver factory boom")
		}

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 4: the worker factory fails
	t.Run("worker factory failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		expectSessionLoad(m, testSession, nil)

		params := m.constructParams(sessionName)
		params.WorkerFactory = func(
			_ context.Context,
			_ string,
			_ int,
			_ log.Fields,
			_ goutils.TaskProcessorMetricHelper,
		) (goutils.TaskProcessor, error) {
			return nil, fmt.Errorf("worker factory boom")
		}

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 5: registering a task handler with the worker fails
	t.Run("handler registration failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		expectSessionLoad(m, testSession, nil)
		m.worker.EXPECT().
			AddToTaskExecutionMap(mock.Anything, mock.Anything).
			Return(fmt.Errorf("registration boom"))

		params := m.constructParams(sessionName)

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 6: happy path
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		expectSessionLoad(m, testSession, nil)
		// All three request handlers (start, stop, submit-commands) get registered
		m.worker.EXPECT().
			AddToTaskExecutionMap(mock.Anything, mock.Anything).
			Return(nil).
			Times(3)

		params := m.constructParams(sessionName)

		uut, err := session.NewSessionRunner(utCtx, params)
		assert.Nil(err)
		assert.NotNil(uut)
	})
}

// TestSessionRunnerHandleStartSession verifies the behavior of `HandleStartSession`.
func TestSessionRunnerHandleStartSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateIdle,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// Case 0: driver starts and the session transitions to READY
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().Start(mock.Anything).Return(nil)
		m.database.EXPECT().MarkSessionReady(mock.Anything, sessionName).Return(nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)
	})

	// Case 1: the driver fails to start (non-ConsistencyError) - no state transition is attempted
	t.Run("driver start failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().
			Start(mock.Anything).
			Return(goutils.NewRuntimeError("driver boom", nil, false))
		// MarkSessionReady is intentionally NOT expected: the handler must bail before it

		var gotErr error
		called := false
		err := uut.HandleStartSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionRunnerStartUpError
		assert.True(errors.As(err, &startErr))
	})

	// Case 2: the driver is already running - a ConsistencyError is treated as idempotent success
	t.Run("driver already running is idempotent", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().
			Start(mock.Anything).
			Return(goutils.NewConsistencyError("already running", nil, false))
		m.database.EXPECT().MarkSessionReady(mock.Anything, sessionName).Return(nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)
	})

	// Case 3: the driver starts but transitioning the session to READY fails
	t.Run("state transition failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().Start(mock.Anything).Return(nil)
		m.database.EXPECT().
			MarkSessionReady(mock.Anything, sessionName).
			Return(models.NewPersistenceError("db boom", nil, false))

		var gotErr error
		called := false
		err := uut.HandleStartSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionRunnerStartUpError
		assert.True(errors.As(err, &startErr))
	})
}

// TestSessionRunnerHandleStopSession verifies the behavior of `HandleStopSession`.
func TestSessionRunnerHandleStopSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateReady,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// Case 0: driver stops and the session transitions back to IDLE, notifying higher layers
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().Stop(mock.Anything).Return(nil)
		m.database.EXPECT().MarkSessionIdle(mock.Anything, sessionName).Return(nil)
		// The idle notification fires from a background goroutine; signal a channel so the test
		// can deterministically wait for it.
		notified := make(chan struct{})
		m.dummyManager.EXPECT().SessionIdleNotify().Run(func() { close(notified) }).Return()

		var gotErr error
		called := false
		err := uut.HandleStopSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)

		select {
		case <-notified:
		case <-time.After(time.Second * 2):
			assert.Fail("SessionIdleNotify was not called")
		}
	})

	// Case 1: the driver fails to stop (non-ConsistencyError) - no state transition is attempted
	t.Run("driver stop failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().
			Stop(mock.Anything).
			Return(goutils.NewRuntimeError("driver boom", nil, false))
		// Neither MarkSessionIdle nor SessionIdleNotify are expected: the handler must bail first

		var gotErr error
		called := false
		err := uut.HandleStopSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var stopErr models.SessionRunnerShutdownError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 2: the driver is already stopped - a ConsistencyError is treated as idempotent success
	t.Run("driver already stopped is idempotent", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().
			Stop(mock.Anything).
			Return(goutils.NewConsistencyError("already stopped", nil, false))
		m.database.EXPECT().MarkSessionIdle(mock.Anything, sessionName).Return(nil)
		notified := make(chan struct{})
		m.dummyManager.EXPECT().SessionIdleNotify().Run(func() { close(notified) }).Return()

		var gotErr error
		called := false
		err := uut.HandleStopSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)

		select {
		case <-notified:
		case <-time.After(time.Second * 2):
			assert.Fail("SessionIdleNotify was not called")
		}
	})

	// Case 3: the driver stops but transitioning the session to IDLE fails - no idle notification
	t.Run("state transition failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.driver.EXPECT().Stop(mock.Anything).Return(nil)
		m.database.EXPECT().
			MarkSessionIdle(mock.Anything, sessionName).
			Return(models.NewPersistenceError("db boom", nil, false))
		// SessionIdleNotify is intentionally NOT expected: it only fires after a successful transition

		var gotErr error
		called := false
		err := uut.HandleStopSession(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var stopErr models.SessionRunnerShutdownError
		assert.True(errors.As(err, &stopErr))
	})
}

// TestSessionRunnerHandleSubmitCommands verifies the behavior of `HandleSubmitCommands`.
func TestSessionRunnerHandleSubmitCommands(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"
	const sessionID = "01HSESSIONIDABCDEF0123456"

	// buildSession assemble a session with the runner mode and state under test
	buildSession := func(
		mode models.SessionRunnerModeTypeENUMType, state models.SessionStateENUMType,
	) models.Session {
		return models.Session{
			ID:                   sessionID,
			Name:                 sessionName,
			Command:              models.SessionCommand{Command: "/bin/bash"},
			State:                state,
			DriverType:           models.SessionDriverTypePTY,
			OutputBufferCapacity: 16384,
			RunnerMode:           mode,
		}
	}

	textContent := "ls"
	validCommands := []models.SessionInputCommand{
		{Type: models.SessionInputCommandTypeText, Content: &textContent},
		{Type: models.SessionInputCommandTypeCR},
	}
	serialized, serErr := models.BuildStdinInputFromCommands(validCommands)
	assert.New(t).Nil(serErr)

	// Case 0: the session is not running in COMMAND mode
	t.Run("not in command mode", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(
			utCtx, t, buildSession(models.SessionRunnerModeTypeByPassed, models.SessionStateReady),
		)

		// No driver/redis interaction is expected: the handler must reject up front

		var gotErr error
		called := false
		err := uut.HandleSubmitCommands(validCommands, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 1: the session is in COMMAND mode but not yet READY
	t.Run("not ready", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(
			utCtx, t, buildSession(models.SessionRunnerModeTypeCommanded, models.SessionStateIdle),
		)

		var gotErr error
		called := false
		err := uut.HandleSubmitCommands(validCommands, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 2: the commands fail to serialize
	t.Run("serialization failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(
			utCtx, t, buildSession(models.SessionRunnerModeTypeCommanded, models.SessionStateReady),
		)

		// A CTRL command with no content is invalid and fails serialization
		badCommands := []models.SessionInputCommand{{Type: models.SessionInputCommandTypeCTRL}}

		var gotErr error
		called := false
		err := uut.HandleSubmitCommands(badCommands, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 3: grabbing the input ring buffer fails
	t.Run("input buffer failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(
			utCtx, t, buildSession(models.SessionRunnerModeTypeCommanded, models.SessionStateReady),
		)

		m.redisClient.EXPECT().
			GetRingBuffer(mock.Anything, session.BuildSessionInputBufferName(sessionID), int64(16384)).
			Return(nil, goutils.NewRedisError("buffer boom", nil, false))

		var gotErr error
		called := false
		err := uut.HandleSubmitCommands(validCommands, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 4: writing the serialized commands into the buffer fails
	t.Run("buffer write failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(
			utCtx, t, buildSession(models.SessionRunnerModeTypeCommanded, models.SessionStateReady),
		)

		ringBuf := mockRedis.NewRingBuffer(t)
		m.redisClient.EXPECT().
			GetRingBuffer(mock.Anything, session.BuildSessionInputBufferName(sessionID), int64(16384)).
			Return(ringBuf, nil)
		ringBuf.EXPECT().
			Write(mock.Anything, serialized).
			Return(0, goutils.NewRedisError("write boom", nil, false))

		var gotErr error
		called := false
		err := uut.HandleSubmitCommands(validCommands, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 5: happy path - serialized commands are written into the input buffer
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(
			utCtx, t, buildSession(models.SessionRunnerModeTypeCommanded, models.SessionStateReady),
		)

		ringBuf := mockRedis.NewRingBuffer(t)
		m.redisClient.EXPECT().
			GetRingBuffer(mock.Anything, session.BuildSessionInputBufferName(sessionID), int64(16384)).
			Return(ringBuf, nil)
		ringBuf.EXPECT().
			Write(mock.Anything, serialized).
			Return(int64(len(serialized)), nil)

		var gotErr error
		called := false
		err := uut.HandleSubmitCommands(validCommands, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)
	})
}

/*
invokeOnComplete reach into a worker task-request param (an unexported struct whose exported
`OnComplete` field carries the completion callback) and invoke that callback. It lets a mocked
worker.Submit stand in for the real worker eventually running the request and reporting its result.
*/
func invokeOnComplete(taskParam interface{}, cbErr error) {
	fn := reflect.ValueOf(taskParam).FieldByName("OnComplete")
	errArg := reflect.Zero(reflect.TypeOf((*error)(nil)).Elem())
	if cbErr != nil {
		errArg = reflect.ValueOf(cbErr)
	}
	fn.Call([]reflect.Value{reflect.ValueOf(context.Background()), errArg})
}

// stubIPCEnvelope a minimal `models.IPCMessageEnvelope` used to feed crafted payloads into
// `HandleIPCRequestMessage`.
type stubIPCEnvelope struct {
	payload string
	err     error
}

// StringPayload return the configured payload/error
func (s stubIPCEnvelope) StringPayload() (string, error) {
	return s.payload, s.err
}

// TestSessionRunnerStartSession verifies the behavior of `StartSession`.
func TestSessionRunnerStartSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateIdle,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// Case 0: non-blocking submission succeeds
	t.Run("non-blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		assert.Nil(uut.StartSession(utCtx, false))
	})

	// Case 1: non-blocking submission fails to enqueue
	t.Run("non-blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StartSession(utCtx, false)
		assert.NotNil(err)
		var startErr models.SessionRunnerStartUpError
		assert.True(errors.As(err, &startErr))
	})

	// Case 2: blocking submission succeeds and the worker reports success
	t.Run("blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// Simulate the worker running the request and reporting success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		assert.Nil(uut.StartSession(utCtx, true))
	})

	// Case 3: blocking submission succeeds but the worker reports a failure
	t.Run("blocking handler failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		handlerErr := models.NewSessionRunnerStartUpError("handler boom", nil, false)
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, handlerErr)
			}).
			Return(nil)

		err := uut.StartSession(utCtx, true)
		assert.NotNil(err)
		// The handler error is surfaced verbatim to the caller
		var startErr models.SessionRunnerStartUpError
		assert.True(errors.As(err, &startErr))
	})

	// Case 4: blocking submission fails to enqueue
	t.Run("blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StartSession(utCtx, true)
		assert.NotNil(err)
		var startErr models.SessionRunnerStartUpError
		assert.True(errors.As(err, &startErr))
	})

	// Case 5: blocking submission succeeds but the caller context ends before a response
	t.Run("blocking context ended", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// The worker accepts the request but never reports back
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		err := uut.StartSession(reqCtx, true)
		assert.NotNil(err)
		var startErr models.SessionRunnerStartUpError
		assert.True(errors.As(err, &startErr))
	})
}

// TestSessionRunnerStopSession verifies the behavior of `StopSession`.
func TestSessionRunnerStopSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateReady,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// Case 0: non-blocking submission succeeds
	t.Run("non-blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		assert.Nil(uut.StopSession(utCtx, false))
	})

	// Case 1: non-blocking submission fails to enqueue
	t.Run("non-blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StopSession(utCtx, false)
		assert.NotNil(err)
		var stopErr models.SessionRunnerShutdownError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 2: blocking submission succeeds and the worker reports success
	t.Run("blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// Simulate the worker running the request and reporting success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		assert.Nil(uut.StopSession(utCtx, true))
	})

	// Case 3: blocking submission succeeds but the worker reports a failure
	t.Run("blocking handler failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		handlerErr := models.NewSessionRunnerShutdownError("handler boom", nil, false)
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, handlerErr)
			}).
			Return(nil)

		err := uut.StopSession(utCtx, true)
		assert.NotNil(err)
		// The handler error is surfaced verbatim to the caller
		var stopErr models.SessionRunnerShutdownError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 4: blocking submission fails to enqueue
	t.Run("blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StopSession(utCtx, true)
		assert.NotNil(err)
		var stopErr models.SessionRunnerShutdownError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 5: blocking submission succeeds but the caller context ends before a response
	t.Run("blocking context ended", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// The worker accepts the request but never reports back
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		err := uut.StopSession(reqCtx, true)
		assert.NotNil(err)
		var stopErr models.SessionRunnerShutdownError
		assert.True(errors.As(err, &stopErr))
	})
}

// TestSessionRunnerSubmitCommands verifies the behavior of `SubmitCommands`.
func TestSessionRunnerSubmitCommands(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateReady,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	textContent := "ls"
	commands := []models.SessionInputCommand{
		{Type: models.SessionInputCommandTypeText, Content: &textContent},
		{Type: models.SessionInputCommandTypeCR},
	}

	// Case 0: submission succeeds and the worker reports success
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// Simulate the worker running the request and reporting success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		assert.Nil(uut.SubmitCommands(utCtx, commands))
	})

	// Case 1: submission succeeds but the worker reports a failure
	t.Run("handler failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		handlerErr := models.NewSessionRunnerSubmitCommandError("handler boom", nil, false)
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, handlerErr)
			}).
			Return(nil)

		err := uut.SubmitCommands(utCtx, commands)
		assert.NotNil(err)
		// The handler error is surfaced verbatim to the caller
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 2: submission fails to enqueue
	t.Run("submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.SubmitCommands(utCtx, commands)
		assert.NotNil(err)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})

	// Case 3: submission succeeds but the caller context ends before a response
	t.Run("context ended", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// The worker accepts the request but never reports back
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		err := uut.SubmitCommands(reqCtx, commands)
		assert.NotNil(err)
		var submitErr models.SessionRunnerSubmitCommandError
		assert.True(errors.As(err, &submitErr))
	})
}

// TestSessionRunnerHandleIPCRequestGeneralFailures verifies the behavior of
// `HandleIPCRequestMessage` for general (non message-specific) failures.
func TestSessionRunnerHandleIPCRequestGeneralFailures(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateReady,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// Case 0: the envelope fails to yield a string payload
	t.Run("string payload failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		err := uut.HandleIPCRequestMessage(stubIPCEnvelope{err: fmt.Errorf("not a string")})
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})

	// Case 1: the payload is not a parsable IPC message
	t.Run("unparsable message", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		err := uut.HandleIPCRequestMessage(stubIPCEnvelope{payload: "not-a-json-message"})
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})

	// Case 2: the payload is a valid IPC message but of an unsupported type (a response message)
	t.Run("unsupported message type", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// A well-formed response message - it parses, but the runner only handles requests
		resp := models.IPCMessageRespUniversal{
			BaseIPCMessage: models.BaseIPCMessage{
				RequestID: "req-1",
				Type:      models.IPCMsgTypeRespRunCommands,
				Sender:    "tester",
				Timestamp: time.Now().UTC(),
			},
			Success: true,
		}
		payload, marshalErr := resp.StringPayload()
		assert.Nil(marshalErr)

		err := uut.HandleIPCRequestMessage(stubIPCEnvelope{payload: payload})
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})
}

// TestSessionRunnerHandleIPCRequestStopSession verifies `HandleIPCRequestMessage` processing an
// IPC request to stop a session.
func TestSessionRunnerHandleIPCRequestStopSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"
	const requestID = "req-stop-1"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateReady,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	// buildStopReq build an envelope carrying an IPC stop-session request
	buildStopReq := func(blocking bool) stubIPCEnvelope {
		req := models.IPCMessageReqStopSession{
			BaseIPCMessage: models.BaseIPCMessage{
				RequestID: requestID,
				Type:      models.IPCMsgTypeReqStopSession,
				Sender:    "tester",
				Timestamp: time.Now().UTC(),
			},
			Blocking: blocking,
		}
		payload, err := req.StringPayload()
		assert.New(t).Nil(err)
		return stubIPCEnvelope{payload: payload}
	}

	// Case 0: blocking stop request succeeds; a success reply is pushed back to the requester
	t.Run("blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// StopSession (blocking) submits to the worker, which reports success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)

		var captured models.IPCMessageEnvelope
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, message goutilsRedis.QueueMessageEnvelope, _ *time.Duration) {
				captured = message
			}).
			Return(uint64(1), nil)

		err := uut.HandleIPCRequestMessage(buildStopReq(true))
		assert.Nil(err)

		// The reply reports the stop succeeded
		resp, ok := captured.(models.IPCMessageRespUniversal)
		assert.True(ok)
		assert.Equal(requestID, resp.RequestID)
		assert.Equal(models.IPCMsgTypeRespStopSession, resp.Type)
		assert.True(resp.Success)
		assert.Nil(resp.ErrorMsg)
	})

	// Case 1: non-blocking stop request succeeds; a success reply is still pushed back
	t.Run("non-blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// Non-blocking StopSession only enqueues; the caller does not await the result
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Return(uint64(1), nil)

		assert.Nil(uut.HandleIPCRequestMessage(buildStopReq(false)))
	})

	// Case 2: the stop request itself fails; a failure reply is reported and the handler does
	// NOT surface an error (the command error travels back inside the reply)
	t.Run("stop failure is reported in reply", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// The worker rejects the request, so StopSession returns an error
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)

		var captured models.IPCMessageEnvelope
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, message goutilsRedis.QueueMessageEnvelope, _ *time.Duration) {
				captured = message
			}).
			Return(uint64(1), nil)

		err := uut.HandleIPCRequestMessage(buildStopReq(true))
		assert.Nil(err)

		// The reply carries the failure back to the requester
		resp, ok := captured.(models.IPCMessageRespUniversal)
		assert.True(ok)
		assert.False(resp.Success)
		assert.NotNil(resp.ErrorMsg)
	})

	// Case 3: obtaining the response queue handle fails
	t.Run("response queue handle failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(nil, goutils.NewRedisError("queue boom", nil, false))

		err := uut.HandleIPCRequestMessage(buildStopReq(true))
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})

	// Case 4: pushing the reply onto the response queue fails
	t.Run("response push failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Return(uint64(0), goutils.NewRedisError("push boom", nil, false))

		err := uut.HandleIPCRequestMessage(buildStopReq(true))
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})
}

// TestSessionRunnerHandleIPCRequestSubmitCommand verifies `HandleIPCRequestMessage` processing an
// IPC request to submit commands to a session.
func TestSessionRunnerHandleIPCRequestSubmitCommand(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const sessionName = "test-session-01"
	const requestID = "req-run-1"

	testSession := models.Session{
		ID:                   "01HSESSIONIDABCDEF0123456",
		Name:                 sessionName,
		Command:              models.SessionCommand{Command: "/bin/bash"},
		State:                models.SessionStateReady,
		DriverType:           models.SessionDriverTypePTY,
		OutputBufferCapacity: 16384,
		RunnerMode:           models.SessionRunnerModeTypeCommanded,
	}

	textContent := "ls"
	commands := []models.SessionInputCommand{
		{Type: models.SessionInputCommandTypeText, Content: &textContent},
		{Type: models.SessionInputCommandTypeCR},
	}

	// buildRunReq build an envelope carrying an IPC run-commands request
	buildRunReq := func() stubIPCEnvelope {
		req := models.IPCMessageReqRunCommands{
			BaseIPCMessage: models.BaseIPCMessage{
				RequestID: requestID,
				Type:      models.IPCMsgTypeReqRunCommands,
				Sender:    "tester",
				Timestamp: time.Now().UTC(),
			},
			Commands: commands,
		}
		payload, err := req.StringPayload()
		assert.New(t).Nil(err)
		return stubIPCEnvelope{payload: payload}
	}

	// Case 0: the run-commands request succeeds; a success reply is pushed back to the requester
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// SubmitCommands (always blocking) submits to the worker, which reports success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)

		var captured models.IPCMessageEnvelope
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, message goutilsRedis.QueueMessageEnvelope, _ *time.Duration) {
				captured = message
			}).
			Return(uint64(1), nil)

		err := uut.HandleIPCRequestMessage(buildRunReq())
		assert.Nil(err)

		// The reply reports the command submission succeeded
		resp, ok := captured.(models.IPCMessageRespUniversal)
		assert.True(ok)
		assert.Equal(requestID, resp.RequestID)
		assert.Equal(models.IPCMsgTypeRespRunCommands, resp.Type)
		assert.True(resp.Success)
		assert.Nil(resp.ErrorMsg)
	})

	// Case 1: the command submission itself fails; a failure reply is reported and the handler
	// does NOT surface an error (the command error travels back inside the reply)
	t.Run("submit failure is reported in reply", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		// The worker rejects the request, so SubmitCommands returns an error
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)

		var captured models.IPCMessageEnvelope
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, message goutilsRedis.QueueMessageEnvelope, _ *time.Duration) {
				captured = message
			}).
			Return(uint64(1), nil)

		err := uut.HandleIPCRequestMessage(buildRunReq())
		assert.Nil(err)

		// The reply carries the failure back to the requester
		resp, ok := captured.(models.IPCMessageRespUniversal)
		assert.True(ok)
		assert.False(resp.Success)
		assert.NotNil(resp.ErrorMsg)
	})

	// Case 2: obtaining the response queue handle fails
	t.Run("response queue handle failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(nil, goutils.NewRedisError("queue boom", nil, false))

		err := uut.HandleIPCRequestMessage(buildRunReq())
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})

	// Case 3: pushing the reply onto the response queue fails
	t.Run("response push failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newRunnerTestMocks(t)

		uut := m.buildRunner(utCtx, t, testSession)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		respQueue := mockRedis.NewQueue(t)
		m.redisClient.EXPECT().
			GetQueueHandle(mock.Anything, session.BuildSessionIPCRespQueueName(requestID)).
			Return(respQueue, nil)
		respQueue.EXPECT().
			PushRight(mock.Anything, mock.Anything, mock.Anything).
			Return(uint64(0), goutils.NewRedisError("push boom", nil, false))

		err := uut.HandleIPCRequestMessage(buildRunReq())
		assert.NotNil(err)
		var ipcErr models.SessionRunnerIPCProcessError
		assert.True(errors.As(err, &ipcErr))
	})
}
