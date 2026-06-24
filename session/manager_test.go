package session_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/alwitt/goutils"
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

// managerTestMocks bundles the mock collaborators a session manager is built on top of.
type managerTestMocks struct {
	persistence *mockdb.Client
	database    *mockdb.Database
	redisClient *mocktest.RedisClientForTest
	worker      *mocktest.TaskProcessorForTest
	runner      *mocksession.Runner

	// runnerFactoryErr when set, the runner factory fails with this error instead of returning
	// `runner`. Consulted dynamically so a test can flip it after the manager is built.
	runnerFactoryErr error
}

// newManagerTestMocks construct a fresh set of manager mock collaborators bound to `t`.
func newManagerTestMocks(t *testing.T) *managerTestMocks {
	return &managerTestMocks{
		persistence: mockdb.NewClient(t),
		database:    mockdb.NewDatabase(t),
		redisClient: mocktest.NewRedisClientForTest(t),
		worker:      mocktest.NewTaskProcessorForTest(t),
		runner:      mocksession.NewRunner(t),
	}
}

/*
constructParams build a happy-path `NewSessionManagerParams`, with each factory returning the
corresponding mock. Expectations on those mocks are set by each test case as needed. The driver
and worker factories are handed off (unused) to the runner the manager spins up; the manager
itself only ever calls the persistence, worker, and runner factories.
*/
func (m *managerTestMocks) constructParams(instanceName string) session.NewSessionManagerParams {
	return session.NewSessionManagerParams{
		InstanceName: instanceName,
		PersistenceFactory: func() (db.Client, error) {
			return m.persistence, nil
		},
		RedisClient: m.redisClient,
		DriverFactory: func(
			_ context.Context, _ models.Session, _ goutilsRedis.Client, _ func(),
		) (session.Driver, error) {
			return nil, fmt.Errorf("driver factory should not be called by the manager directly")
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
		RunnerFactory: func(
			_ context.Context, _ session.NewSessionRunnerParams,
		) (session.Runner, error) {
			if m.runnerFactoryErr != nil {
				return nil, m.runnerFactoryErr
			}
			return m.runner, nil
		},
	}
}

/*
passthroughTransaction wires the persistence client so every `UseDatabaseInTransaction` simply runs
the supplied core logic against the database mock. Left open (no `.Times`) so it satisfies however
many transactions a given flow performs.
*/
func (m *managerTestMocks) passthroughTransaction() {
	m.persistence.EXPECT().
		UseDatabaseInTransaction(mock.Anything, mock.Anything).
		RunAndReturn(func(
			ctx context.Context, coreLogic func(context.Context, db.Database) error,
		) error {
			return coreLogic(ctx, m.database)
		})
}

/*
buildManager construct a ready-to-use manager backed by the mocks, wiring the construction-time
expectations (worker handler registration). The three request handlers (start, stop, stop-all) get
registered at construction.
*/
func (m *managerTestMocks) buildManager(
	ctx context.Context, t *testing.T, instanceName string,
) session.Manager {
	assert := assert.New(t)

	m.worker.EXPECT().AddToTaskExecutionMap(mock.Anything, mock.Anything).Return(nil).Times(3)

	uut, err := session.NewSessionManager(ctx, m.constructParams(instanceName))
	assert.Nil(err)
	assert.NotNil(uut)
	return uut
}

/*
startManager construct a manager and drive it through a happy-path `Start` (no leftover READY
sessions to reset, worker loop comes up), returning a RUNNING manager ready to accept requests.
*/
func (m *managerTestMocks) startManager(
	ctx context.Context, t *testing.T, instanceName string,
) session.Manager {
	assert := assert.New(t)

	uut := m.buildManager(ctx, t, instanceName)

	m.passthroughTransaction()
	m.database.EXPECT().
		ListSessions(mock.Anything, mock.Anything).
		Return([]models.Session{}, nil).
		Once()
	m.worker.EXPECT().StartEventLoop(mock.Anything).Return(nil).Once()

	assert.Nil(uut.Start(ctx))
	return uut
}

/*
loadRunner drive a (RUNNING) manager through a successful `HandleStartSession` so that `runner` is
recorded as the active runner for `sessionName`. This is the only way to populate the manager's
unexported active-runner set for the stop/teardown tests.
*/
func (m *managerTestMocks) loadRunner(t *testing.T, uut session.Manager, sessionName string) {
	assert := assert.New(t)

	m.database.EXPECT().
		GetSessionByName(mock.Anything, sessionName).
		Return(models.Session{
			ID:                   "01HSESSIONIDABCDEF0123456",
			Name:                 sessionName,
			Command:              models.SessionCommand{Command: "/bin/bash"},
			State:                models.SessionStateIdle,
			DriverType:           models.SessionDriverTypePTY,
			OutputBufferCapacity: 16384,
			RunnerMode:           models.SessionRunnerModeTypeCommanded,
		}, nil).
		Once()
	m.runner.EXPECT().Start(mock.Anything).Return(nil).Once()
	m.runner.EXPECT().StartSession(mock.Anything, true).Return(nil).Once()

	assert.Nil(uut.HandleStartSession(sessionName, func(_ context.Context, _ error) {}))
}

// TestSessionManagerConstruct verifies the behavior of `NewSessionManager`.
func TestSessionManagerConstruct(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"

	// Case 0: initialization parameters fail validation
	t.Run("param validation failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		// An empty instance name fails the `required` constraint
		params := m.constructParams("")

		uut, err := session.NewSessionManager(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var validationErr goutils.ValidationError
		assert.True(errors.As(err, &validationErr))
	})

	// Case 1: the persistence factory fails
	t.Run("persistence factory failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		params := m.constructParams(instanceName)
		params.PersistenceFactory = func() (db.Client, error) {
			return nil, fmt.Errorf("persistence factory boom")
		}

		uut, err := session.NewSessionManager(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 2: the worker factory fails
	t.Run("worker factory failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		params := m.constructParams(instanceName)
		params.WorkerFactory = func(
			_ context.Context,
			_ string,
			_ int,
			_ log.Fields,
			_ goutils.TaskProcessorMetricHelper,
		) (goutils.TaskProcessor, error) {
			return nil, fmt.Errorf("worker factory boom")
		}

		uut, err := session.NewSessionManager(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 3: registering a task handler with the worker fails
	t.Run("handler registration failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		m.worker.EXPECT().
			AddToTaskExecutionMap(mock.Anything, mock.Anything).
			Return(fmt.Errorf("registration boom"))

		params := m.constructParams(instanceName)

		uut, err := session.NewSessionManager(utCtx, params)
		assert.Nil(uut)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 4: happy path
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		// All three request handlers (start, stop, stop-all) get registered
		m.worker.EXPECT().
			AddToTaskExecutionMap(mock.Anything, mock.Anything).
			Return(nil).
			Times(3)

		params := m.constructParams(instanceName)

		uut, err := session.NewSessionManager(utCtx, params)
		assert.Nil(err)
		assert.NotNil(uut)
	})
}

// TestSessionManagerStart verifies the behavior of `Start`.
func TestSessionManagerStart(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"

	// buildSession assemble a READY session left over from a previous run
	buildSession := func(name string) models.Session {
		return models.Session{
			ID:                   "01HSESSIONID" + name,
			Name:                 name,
			Command:              models.SessionCommand{Command: "/bin/bash"},
			State:                models.SessionStateReady,
			DriverType:           models.SessionDriverTypePTY,
			OutputBufferCapacity: 16384,
			RunnerMode:           models.SessionRunnerModeTypeCommanded,
		}
	}

	// Case 0: no READY sessions to reset; the worker loop starts cleanly
	t.Run("success no ready sessions", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return([]models.Session{}, nil)
		m.worker.EXPECT().StartEventLoop(mock.Anything).Return(nil)

		assert.Nil(uut.Start(utCtx))
	})

	// Case 1: sessions left in READY are reset back to IDLE before the worker loop starts
	t.Run("success resets ready sessions", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return([]models.Session{buildSession("sess-a"), buildSession("sess-b")}, nil)
		m.database.EXPECT().MarkSessionIdle(mock.Anything, "sess-a").Return(nil)
		m.database.EXPECT().MarkSessionIdle(mock.Anything, "sess-b").Return(nil)
		m.worker.EXPECT().StartEventLoop(mock.Anything).Return(nil)

		assert.Nil(uut.Start(utCtx))
	})

	// Case 2: listing the leftover READY sessions fails
	t.Run("list ready sessions failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return(nil, models.NewPersistenceError("list boom", nil, false))
		// StartEventLoop is intentionally NOT expected: the handler must bail before it

		err := uut.Start(utCtx)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 3: resetting one of the leftover READY sessions to IDLE fails
	t.Run("mark session idle failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return([]models.Session{buildSession("sess-a")}, nil)
		m.database.EXPECT().
			MarkSessionIdle(mock.Anything, "sess-a").
			Return(models.NewPersistenceError("mark boom", nil, false))

		err := uut.Start(utCtx)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 4: the housekeeping succeeds but the worker loop fails to start
	t.Run("worker start failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return([]models.Session{}, nil)
		m.worker.EXPECT().
			StartEventLoop(mock.Anything).
			Return(fmt.Errorf("loop boom"))

		err := uut.Start(utCtx)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 5: a failed start rolls RUNNING back to NEW, so a subsequent start can still succeed
	t.Run("retriable after failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return([]models.Session{}, nil)
		// First attempt: the worker loop fails to start
		m.worker.EXPECT().
			StartEventLoop(mock.Anything).
			Return(fmt.Errorf("loop boom")).
			Once()

		err := uut.Start(utCtx)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))

		// Second attempt: the worker loop now starts and the manager comes up
		m.worker.EXPECT().StartEventLoop(mock.Anything).Return(nil).Once()

		assert.Nil(uut.Start(utCtx))
	})

	// Case 6: starting an already-running manager is a consistency error
	t.Run("already running", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.passthroughTransaction()
		m.database.EXPECT().
			ListSessions(mock.Anything, mock.Anything).
			Return([]models.Session{}, nil)
		m.worker.EXPECT().StartEventLoop(mock.Anything).Return(nil)

		assert.Nil(uut.Start(utCtx))

		// A second start finds the manager already RUNNING; no mocks are touched
		err := uut.Start(utCtx)
		assert.NotNil(err)
		var consistencyErr goutils.ConsistencyError
		assert.True(errors.As(err, &consistencyErr))
	})

	// Case 7: a stopped manager is terminal and can never start again
	t.Run("already stopped", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// Stopping a NEW (never started) manager drives it straight to STOPPED with no teardown
		assert.Nil(uut.Stop(utCtx))

		err := uut.Start(utCtx)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})
}

// TestSessionManagerStop verifies the behavior of `Stop`.
func TestSessionManagerStop(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"

	// Case 0: a running manager tears down cleanly
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		// StopAllSessions (blocking) submits to the worker, which reports success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)
		m.worker.EXPECT().StopEventLoop().Return(nil)

		assert.Nil(uut.Stop(utCtx))
	})

	// Case 1: the bulk session shutdown fails; the error is surfaced but teardown still proceeds
	t.Run("stop all sessions failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		// The worker rejects the bulk stop request, so StopAllSessions returns an error
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))
		// Teardown continues regardless: the worker loop is still stopped
		m.worker.EXPECT().StopEventLoop().Return(nil)

		err := uut.Stop(utCtx)
		assert.NotNil(err)
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))
	})

	// Case 2: the bulk shutdown succeeds but stopping the worker loop fails
	t.Run("worker stop loop failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)
		m.worker.EXPECT().
			StopEventLoop().
			Return(fmt.Errorf("loop stop boom"))

		err := uut.Stop(utCtx)
		assert.NotNil(err)
		var runtimeErr goutils.RuntimeError
		assert.True(errors.As(err, &runtimeErr))
	})

	// Case 3: stopping a never-started manager is a NOP that still bars any future start
	t.Run("never started is NOP", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// No teardown happens: a NEW manager is simply driven to STOPPED, no collaborators touched
		assert.Nil(uut.Stop(utCtx))
	})

	// Case 4: a second stop on an already-stopped manager is an idempotent NOP
	t.Run("double stop is idempotent", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)
		m.worker.EXPECT().StopEventLoop().Return(nil)

		assert.Nil(uut.Stop(utCtx))
		// The second stop finds the manager already STOPPED; no collaborators are touched
		assert.Nil(uut.Stop(utCtx))
	})
}

// TestSessionManagerHandleStartSession verifies the behavior of `HandleStartSession`.
func TestSessionManagerHandleStartSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"
	const sessionName = "test-session-01"

	// buildSession assemble a session in the requested state
	buildSession := func(state models.SessionStateENUMType) models.Session {
		return models.Session{
			ID:                   "01HSESSIONIDABCDEF0123456",
			Name:                 sessionName,
			Command:              models.SessionCommand{Command: "/bin/bash"},
			State:                state,
			DriverType:           models.SessionDriverTypePTY,
			OutputBufferCapacity: 16384,
			RunnerMode:           models.SessionRunnerModeTypeCommanded,
		}
	}

	// Case 0: an IDLE session is loaded, its runner spun up and started
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateIdle), nil)
		m.runner.EXPECT().Start(mock.Anything).Return(nil)
		m.runner.EXPECT().StartSession(mock.Anything, true).Return(nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)
	})

	// Case 1: the manager is no longer accepting start requests
	t.Run("not accepting starts", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		// A never-started manager has not flipped `allowNewStarts`, so it rejects up front
		uut := m.buildManager(utCtx, t, instanceName)

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 2: loading the session from persistence fails
	t.Run("session load failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(models.Session{}, goutils.NewNotFoundError("no such session", nil, false))

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 3: a runner for the session is already loaded
	t.Run("runner already present", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateIdle), nil)

		// First start records a runner
		m.runner.EXPECT().Start(mock.Anything).Return(nil)
		m.runner.EXPECT().StartSession(mock.Anything, true).Return(nil)
		assert.Nil(uut.HandleStartSession(sessionName, func(_ context.Context, _ error) {}))

		// Second start finds the existing runner and bails before building another
		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 4: a non-IDLE session is forced back to IDLE before its runner is started
	t.Run("non-idle session forced to idle", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateReady), nil)
		// The READY session is reset to IDLE first
		m.database.EXPECT().MarkSessionIdle(mock.Anything, sessionName).Return(nil)
		m.runner.EXPECT().Start(mock.Anything).Return(nil)
		m.runner.EXPECT().StartSession(mock.Anything, true).Return(nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)
	})

	// Case 5: forcing a non-IDLE session back to IDLE fails
	t.Run("force to idle failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateReady), nil)
		m.database.EXPECT().
			MarkSessionIdle(mock.Anything, sessionName).
			Return(models.NewPersistenceError("mark boom", nil, false))
		// No runner is built: the handler must bail after the failed reset

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 6: building the session runner fails
	t.Run("runner factory failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		// The runner factory now fails
		m.runnerFactoryErr = fmt.Errorf("runner factory boom")

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateIdle), nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 7: the runner fails to start; the partial start is cleaned up and the session reset
	t.Run("runner start failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateIdle), nil)
		m.runner.EXPECT().
			Start(mock.Anything).
			Return(goutils.NewRuntimeError("runner boom", nil, false))
		// cleanUpOnFail tears the runner down and forces the session back to IDLE
		m.runner.EXPECT().Stop(mock.Anything).Return(nil)
		m.database.EXPECT().MarkSessionIdle(mock.Anything, sessionName).Return(nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 8: the runner starts but its driver fails to start; cleanup likewise runs
	t.Run("driver start failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		m.database.EXPECT().
			GetSessionByName(mock.Anything, sessionName).
			Return(buildSession(models.SessionStateIdle), nil)
		m.runner.EXPECT().Start(mock.Anything).Return(nil)
		m.runner.EXPECT().
			StartSession(mock.Anything, true).
			Return(goutils.NewRuntimeError("driver boom", nil, false))
		// cleanUpOnFail tears the runner down and forces the session back to IDLE
		m.runner.EXPECT().Stop(mock.Anything).Return(nil)
		m.database.EXPECT().MarkSessionIdle(mock.Anything, sessionName).Return(nil)

		var gotErr error
		called := false
		err := uut.HandleStartSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})
}

// TestSessionManagerHandleStopSession verifies the behavior of `HandleStopSession`.
func TestSessionManagerHandleStopSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"
	const sessionName = "test-session-01"

	// Case 0: there is no runner for the session; stopping is a NOP
	t.Run("no runner is NOP", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		// No runner is loaded, so the handler reports success without touching any collaborator
		var gotErr error
		called := false
		err := uut.HandleStopSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)
	})

	// Case 1: the runner's driver stops, the runner is torn down and forgotten
	t.Run("success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)
		m.loadRunner(t, uut, sessionName)

		m.runner.EXPECT().StopSession(mock.Anything, true).Return(nil).Once()
		m.runner.EXPECT().Stop(mock.Anything).Return(nil).Once()

		var gotErr error
		called := false
		err := uut.HandleStopSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)

		// The runner has been forgotten: a second stop is now a NOP
		assert.Nil(uut.HandleStopSession(sessionName, func(_ context.Context, _ error) {}))
	})

	// Case 2: stopping the driver fails; the runner is retained for a later retry
	t.Run("driver stop failure retains runner", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)
		m.loadRunner(t, uut, sessionName)

		// First stop: the driver fails to stop, so the handler bails before tearing down
		m.runner.EXPECT().
			StopSession(mock.Anything, true).
			Return(goutils.NewRuntimeError("driver boom", nil, false)).
			Once()

		var gotErr error
		called := false
		err := uut.HandleStopSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var stopErr models.SessionManagerStopSessionError
		assert.True(errors.As(err, &stopErr))

		// The runner is still loaded: a follow-up stop drives it again and succeeds
		m.runner.EXPECT().StopSession(mock.Anything, true).Return(nil).Once()
		m.runner.EXPECT().Stop(mock.Anything).Return(nil).Once()
		assert.Nil(uut.HandleStopSession(sessionName, func(_ context.Context, _ error) {}))
	})

	// Case 3: tearing the runner down fails; the runner is likewise retained
	t.Run("runner teardown failure retains runner", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)
		m.loadRunner(t, uut, sessionName)

		// First stop: the driver stops but the runner teardown fails
		m.runner.EXPECT().StopSession(mock.Anything, true).Return(nil).Once()
		m.runner.EXPECT().
			Stop(mock.Anything).
			Return(goutils.NewRuntimeError("teardown boom", nil, false)).
			Once()

		var gotErr error
		called := false
		err := uut.HandleStopSession(sessionName, func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var stopErr models.SessionManagerStopSessionError
		assert.True(errors.As(err, &stopErr))

		// The runner is still loaded: a follow-up stop drives it again and succeeds
		m.runner.EXPECT().StopSession(mock.Anything, true).Return(nil).Once()
		m.runner.EXPECT().Stop(mock.Anything).Return(nil).Once()
		assert.Nil(uut.HandleStopSession(sessionName, func(_ context.Context, _ error) {}))
	})
}

// TestSessionManagerHandleStopAllSession verifies the behavior of `HandleStopAllSessions`.
func TestSessionManagerHandleStopAllSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"
	const sessionName = "test-session-01"

	// Case 0: no active runners; the bulk stop is a NOP but still bars new starts
	t.Run("no active runners", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)

		var gotErr error
		called := false
		err := uut.HandleStopAllSessions(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)

		// New starts are now barred: HandleStartSession rejects before touching persistence
		startErr := uut.HandleStartSession(sessionName, func(_ context.Context, _ error) {})
		assert.NotNil(startErr)
		var managerStartErr models.SessionManagerStartSessionError
		assert.True(errors.As(startErr, &managerStartErr))
	})

	// Case 1: the single active runner is torn down and forgotten
	t.Run("single runner success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)
		m.loadRunner(t, uut, sessionName)

		m.runner.EXPECT().Stop(mock.Anything).Return(nil).Once()

		var gotErr error
		called := false
		err := uut.HandleStopAllSessions(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.Nil(err)
		assert.True(called)
		assert.Nil(gotErr)

		// The runner has been forgotten: a subsequent stop is a NOP
		assert.Nil(uut.HandleStopSession(sessionName, func(_ context.Context, _ error) {}))
	})

	// Case 2: the runner teardown fails, yet the runner is still forgotten (bulk shutdown does
	// NOT retain failed runners the way HandleStopSession does)
	t.Run("runner teardown failure still forgets runner", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)
		m.loadRunner(t, uut, sessionName)

		m.runner.EXPECT().
			Stop(mock.Anything).
			Return(goutils.NewRuntimeError("teardown boom", nil, false)).
			Once()

		var gotErr error
		called := false
		err := uut.HandleStopAllSessions(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))

		// Despite the failed teardown the runner was dropped: a subsequent stop is a NOP
		assert.Nil(uut.HandleStopSession(sessionName, func(_ context.Context, _ error) {}))
	})

	// Case 3: with several active runners, one teardown failure is surfaced while every runner is
	// still torn down and forgotten
	t.Run("multiple runners one teardown failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.startManager(utCtx, t, instanceName)
		// Both sessions resolve to the same runner mock; the manager keys them by name
		m.loadRunner(t, uut, "sess-a")
		m.loadRunner(t, uut, "sess-b")

		// Map iteration order is unspecified, so the only guarantee is two teardowns of which the
		// first to run fails. Ordered expectations make the first Stop fail and the second succeed.
		m.runner.EXPECT().
			Stop(mock.Anything).
			Return(goutils.NewRuntimeError("teardown boom", nil, false)).
			Once()
		m.runner.EXPECT().Stop(mock.Anything).Return(nil).Once()

		var gotErr error
		called := false
		err := uut.HandleStopAllSessions(func(_ context.Context, e error) {
			called = true
			gotErr = e
		})
		assert.NotNil(err)
		assert.True(called)
		assert.NotNil(gotErr)
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))

		// Both runners were forgotten regardless of the failure
		assert.Nil(uut.HandleStopSession("sess-a", func(_ context.Context, _ error) {}))
		assert.Nil(uut.HandleStopSession("sess-b", func(_ context.Context, _ error) {}))
	})
}

// TestSessionManagerStartSession verifies the behavior of `StartSession` (request submission).
func TestSessionManagerStartSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"
	const sessionName = "test-session-01"

	// Case 0: non-blocking submission succeeds
	t.Run("non-blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		assert.Nil(uut.StartSession(utCtx, sessionName, false))
	})

	// Case 1: non-blocking submission fails to enqueue
	t.Run("non-blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StartSession(utCtx, sessionName, false)
		assert.NotNil(err)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 2: blocking submission succeeds and the worker reports success
	t.Run("blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// Simulate the worker running the request and reporting success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		assert.Nil(uut.StartSession(utCtx, sessionName, true))
	})

	// Case 3: blocking submission succeeds but the worker reports a failure
	t.Run("blocking handler failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		handlerErr := models.NewSessionManagerStartSessionError("handler boom", nil, false)
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, handlerErr)
			}).
			Return(nil)

		err := uut.StartSession(utCtx, sessionName, true)
		assert.NotNil(err)
		// The handler error is surfaced verbatim to the caller
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 4: blocking submission fails to enqueue
	t.Run("blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StartSession(utCtx, sessionName, true)
		assert.NotNil(err)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})

	// Case 5: blocking submission succeeds but the caller context ends before a response
	t.Run("blocking context ended", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// The worker accepts the request but never reports back
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		err := uut.StartSession(reqCtx, sessionName, true)
		assert.NotNil(err)
		var startErr models.SessionManagerStartSessionError
		assert.True(errors.As(err, &startErr))
	})
}

// TestSessionManagerStopSession verifies the behavior of `StopSession` (request submission).
func TestSessionManagerStopSession(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"
	const sessionName = "test-session-01"

	// Case 0: non-blocking submission succeeds
	t.Run("non-blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		assert.Nil(uut.StopSession(utCtx, sessionName, false))
	})

	// Case 1: non-blocking submission fails to enqueue
	t.Run("non-blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StopSession(utCtx, sessionName, false)
		assert.NotNil(err)
		var stopErr models.SessionManagerStopSessionError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 2: blocking submission succeeds and the worker reports success
	t.Run("blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// Simulate the worker running the request and reporting success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		assert.Nil(uut.StopSession(utCtx, sessionName, true))
	})

	// Case 3: blocking submission succeeds but the worker reports a failure
	t.Run("blocking handler failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		handlerErr := models.NewSessionManagerStopSessionError("handler boom", nil, false)
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, handlerErr)
			}).
			Return(nil)

		err := uut.StopSession(utCtx, sessionName, true)
		assert.NotNil(err)
		// The handler error is surfaced verbatim to the caller
		var stopErr models.SessionManagerStopSessionError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 4: blocking submission fails to enqueue
	t.Run("blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StopSession(utCtx, sessionName, true)
		assert.NotNil(err)
		var stopErr models.SessionManagerStopSessionError
		assert.True(errors.As(err, &stopErr))
	})

	// Case 5: blocking submission succeeds but the caller context ends before a response
	t.Run("blocking context ended", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// The worker accepts the request but never reports back
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		err := uut.StopSession(reqCtx, sessionName, true)
		assert.NotNil(err)
		var stopErr models.SessionManagerStopSessionError
		assert.True(errors.As(err, &stopErr))
	})
}

// TestSessionManagerStopAllSessions verifies the behavior of `StopAllSessions` (request submission)
func TestSessionManagerStopAllSessions(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"

	// Case 0: non-blocking submission succeeds
	t.Run("non-blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		assert.Nil(uut.StopAllSessions(utCtx, false))
	})

	// Case 1: non-blocking submission fails to enqueue
	t.Run("non-blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StopAllSessions(utCtx, false)
		assert.NotNil(err)
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))
	})

	// Case 2: blocking submission succeeds and the worker reports success
	t.Run("blocking success", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// Simulate the worker running the request and reporting success
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, nil)
			}).
			Return(nil)

		assert.Nil(uut.StopAllSessions(utCtx, true))
	})

	// Case 3: blocking submission succeeds but the worker reports a failure
	t.Run("blocking handler failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		handlerErr := models.NewSessionManagerStopAllSessionsError("handler boom", nil, false)
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				invokeOnComplete(taskParam, handlerErr)
			}).
			Return(nil)

		err := uut.StopAllSessions(utCtx, true)
		assert.NotNil(err)
		// The handler error is surfaced verbatim to the caller
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))
	})

	// Case 4: blocking submission fails to enqueue
	t.Run("blocking submit failure", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		err := uut.StopAllSessions(utCtx, true)
		assert.NotNil(err)
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))
	})

	// Case 5: blocking submission succeeds but the caller context ends before a response
	t.Run("blocking context ended", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// The worker accepts the request but never reports back
		m.worker.EXPECT().Submit(mock.Anything, mock.Anything).Return(nil)

		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()

		err := uut.StopAllSessions(reqCtx, true)
		assert.NotNil(err)
		var stopAllErr models.SessionManagerStopAllSessionsError
		assert.True(errors.As(err, &stopAllErr))
	})
}

/*
submittedSessionName reach into a worker task-request param (an unexported struct whose exported
`SessionName` field names the targeted session) and read that name. It lets a test confirm which
session a fire-and-forget submission was aimed at.
*/
func submittedSessionName(taskParam interface{}) string {
	return reflect.ValueOf(taskParam).FieldByName("SessionName").String()
}

// TestSessionManagerRunnerCallbackTrigger verifies the runner-callback shims
// `HandleSessionIdleNotify` and `HandleSessionIPCProcessError`. Both simply fire a non-blocking
// `StopSession` for the affected session, so the key behavior is that the request gets submitted.
func TestSessionManagerRunnerCallbackTrigger(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	const instanceName = "test-manager-01"
	const sessionName = "test-session-01"

	// Case 0: an unexpected idle notification submits a non-blocking stop for the session
	t.Run("idle notify submits stop", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		captured := ""
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				captured = submittedSessionName(taskParam)
			}).
			Return(nil)

		uut.HandleSessionIdleNotify(sessionName)
		assert.Equal(sessionName, captured)
	})

	// Case 1: an IPC processing error likewise submits a non-blocking stop for the session
	t.Run("ipc error submits stop", func(t *testing.T) {
		assert := assert.New(t)
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		captured := ""
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Run(func(_ context.Context, taskParam interface{}) {
				captured = submittedSessionName(taskParam)
			}).
			Return(nil)

		uut.HandleSessionIPCProcessError(sessionName, fmt.Errorf("ipc boom"))
		assert.Equal(sessionName, captured)
	})

	// Case 2: a failed submission is swallowed (only logged); the shim must not surface or panic
	t.Run("submit failure is swallowed", func(t *testing.T) {
		utCtx := context.Background()
		m := newManagerTestMocks(t)

		uut := m.buildManager(utCtx, t, instanceName)

		// The submission fails, but the shim has no return value and must simply log and move on
		m.worker.EXPECT().
			Submit(mock.Anything, mock.Anything).
			Return(fmt.Errorf("submit boom"))

		uut.HandleSessionIdleNotify(sessionName)
	})
}
