package session

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
)

// Manager entity responsible for managing a set of PTY session runner
type Manager interface {
	// Start the session manager
	Start(ctx context.Context) error

	// Stop the session manager
	Stop(ctx context.Context) error

	/*
		StartSession bring up a session runner for an existing session, and start the runner.

			@param ctx context.Context - execution context
			@param sessionName string - session to bring up
			@param blocking bool - whether this is a blocking call
	*/
	StartSession(ctx context.Context, sessionName string, blocking bool) error

	/*
		StopSession bring the session back to IDLE and unload its runner.

			@param ctx context.Context - execution context
			@param sessionName string - session to unload
			@param blocking bool - whether this is a blocking call
	*/
	StopSession(ctx context.Context, sessionName string, blocking bool) error

	// ------------------------------------------------------------------------------------
	// Exposed for testing purposes

	/*
		HandleStartSession handler function called by worker task engine to process `StartSession`

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param sessionName string - session to bring up
			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleStartSession(sessionName string, onComplete func(ctx context.Context, err error)) error

	/*
		HandleStopSession handler function called by worker task engine to process `StopSession`

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param sessionName string - session to unload
			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleStopSession(sessionName string, onComplete func(ctx context.Context, err error)) error

	/*
		ChangeOutputBufferCapacity change session output buffer capacity.

		This can only be performed on IDLE sessions.

			@param ctx context.Context - execution context
			@param sessionName string - session to change
			@param newCap int64 - new output buffer capacity
			@param blocking bool - whether this is a blocking call
	*/
	ChangeOutputBufferCapacity(
		ctx context.Context, sessionName string, newCap int64, blocking bool,
	) error

	/*
		HandleChangeOutputBufferCapacity handler function called by worker task engine to process
		`ChangeOutputBufferCapacity`

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param sessionName string - session to change
			@param newCap int64 - new output buffer capacity
			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleChangeOutputBufferCapacity(
		sessionName string, newCap int64, onComplete func(ctx context.Context, err error),
	) error

	/*
		HandleSessionIdleNotify callback function used by session runner to indicate the
		managed session went IDLE before any shutdown command was given.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param sessionName string - the session which stopped
	*/
	HandleSessionIdleNotify(sessionName string)

	/*
		HandleSessionIPCProcessError callback function used by session runner to indicate the
		managed session encountered issues operating the REDIS IPC queue.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param sessionName string - the session which stopped
	*/
	HandleSessionIPCProcessError(sessionName string, sessionErr error)

	/*
		StopAllSessions tear down every currently active session runner.

		This is a bulk shutdown intended only for use while the manager itself is stopping.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param ctx context.Context - execution context
			@param blocking bool - whether this is a blocking call
	*/
	StopAllSessions(ctx context.Context, blocking bool) error

	/*
		HandleStopAllSessions handler function called by worker task engine to process
		`StopAllSessions`.

		Each active runner is simply torn down via its `Stop`; the session is NOT transitioned back
		to IDLE here. On the next manager start, any session left in READY state is reset to IDLE.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleStopAllSessions(onComplete func(ctx context.Context, err error)) error
}

// managerImpl implements Manager
type managerImpl struct {
	goutils.Component

	// instanceName manager instance name
	instanceName string

	// activeRunners the currently loaded session runner
	activeRunners map[string]Runner

	validator *validator.Validate

	// workingCtx the context the manager operates in
	workingCtx       context.Context
	workingCtxCancel context.CancelFunc

	// wg for threads spawn directly by the manager
	wg sync.WaitGroup

	// persistence DB persistence layer handle
	persistence db.Client
	// persistenceFactory factory function to generate prepare new persistence clients
	persistenceFactory func() (db.Client, error)

	// redisClient client to REDIS
	redisClient goutilsRedis.Client

	// driverFactory session driver factory function
	driverFactory driverFactoryFunc

	// worker support task processing inbound requests
	worker goutils.TaskProcessor

	// workerFactory worker task processor factory function
	workerFactory taskProcessorFactoryFunc

	// runnerFactory session runner factory function
	runnerFactory func(parentCtx context.Context, params NewSessionRunnerParams) (Runner, error)

	// lifecycle tracks the manager lifecycle: NEW -> RUNNING -> STOPPED. STOPPED is terminal,
	// so once stopped the manager can never start again.
	lifecycle atomic.Int32

	// allowNewStarts indicate whether the manager should allow new session requests
	allowNewStarts atomic.Bool
}

// Manager lifecycle states tracked by managerImpl.lifecycle
const (
	// managerStateNew the manager has been defined but not yet started
	managerStateNew int32 = iota
	// managerStateRunning the manager has started and is operating
	managerStateRunning
	// managerStateStopped the manager has stopped; this is terminal
	managerStateStopped
)

// NewSessionManagerParams parameters for defining a new session manager
type NewSessionManagerParams struct {
	// InstanceName manager instance name
	InstanceName string `validate:"required"`

	// PersistenceFactory factory function to generate prepare new persistence clients
	PersistenceFactory func() (db.Client, error) `validate:"required"`

	// RedisClient the REDIS client
	RedisClient goutilsRedis.Client `validate:"required"`

	// DriverFactory session driver factory function
	DriverFactory driverFactoryFunc `validate:"required"`

	// WorkerFactory worker task processor factory function
	WorkerFactory taskProcessorFactoryFunc `validate:"required"`

	// RunnerFactory session runner factory function
	RunnerFactory func(parentCtx context.Context, params NewSessionRunnerParams) (Runner, error) `validate:"required"`
}

/*
NewSessionManager define a new manager to oversee session runners

	@param parentCtx context.Context - parent context for the manager
	@param params NewSessionManagerParams - initialization parameters
	@returns new manager
*/
func NewSessionManager(parentCtx context.Context, params NewSessionManagerParams) (Manager, error) {
	validate := validator.New()
	if err := models.RegisterWithValidator(validate); err != nil {
		return nil, goutils.NewRuntimeError("failed to install custom validation macros", err, true)
	}

	// Validate initialization parameters
	if err := validate.Struct(&params); err != nil {
		return nil, goutils.NewValidationError("session manager init param invalid", err, true)
	}

	persistence, err := params.PersistenceFactory()
	if err != nil {
		return nil, goutils.NewRuntimeError(
			"failed to prepare scoped persistence for manager", err, true,
		)
	}

	logTags := log.Fields{
		"module":    "session",
		"component": "session-manager",
		"instance":  params.InstanceName,
	}

	instance := &managerImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		instanceName:       params.InstanceName,
		activeRunners:      make(map[string]Runner),
		validator:          validate,
		wg:                 sync.WaitGroup{},
		persistence:        persistence,
		persistenceFactory: params.PersistenceFactory,
		redisClient:        params.RedisClient,
		driverFactory:      params.DriverFactory,
		runnerFactory:      params.RunnerFactory,
		workerFactory:      params.WorkerFactory,
	}
	instance.workingCtx, instance.workingCtxCancel = context.WithCancel(parentCtx)

	// ------------------------------------------------------------------------------------
	// Prepare worker task engine

	instance.worker, err = params.WorkerFactory(
		instance.workingCtx,
		instance.instanceName+".runner",
		10,
		log.Fields{
			"module":        "session",
			"component":     "session-manager",
			"instance":      params.InstanceName,
			"sub-component": "task-engine",
		},
		nil,
	)
	if err != nil {
		return nil, goutils.NewRuntimeError("failed to define worker task engine", err, true)
	}

	// ------------------------------------------------------------------------------------
	// Register task engine handler

	// Pending request to start the session
	if err := instance.worker.AddToTaskExecutionMap(
		reflect.TypeOf(managerReqStartSession{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(managerReqStartSession)
			if ok {
				return instance.HandleStartSession(newRequest.SessionName, newRequest.OnComplete)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(managerReqStartSession{}),
		), err, true)
	}

	// Pending request to stop the session
	if err := instance.worker.AddToTaskExecutionMap(
		reflect.TypeOf(managerReqStopSession{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(managerReqStopSession)
			if ok {
				return instance.HandleStopSession(newRequest.SessionName, newRequest.OnComplete)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(managerReqStopSession{}),
		), err, true)
	}

	// Pending request to change a session's output buffer capacity
	if err := instance.worker.AddToTaskExecutionMap(
		reflect.TypeOf(managerReqChangeOutBufferCap{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(managerReqChangeOutBufferCap)
			if ok {
				return instance.HandleChangeOutputBufferCapacity(
					newRequest.SessionName, newRequest.NewCapacity, newRequest.OnComplete,
				)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(managerReqChangeOutBufferCap{}),
		), err, true)
	}

	// Pending request to stop all sessions
	if err := instance.worker.AddToTaskExecutionMap(
		reflect.TypeOf(managerReqStopAllSessions{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(managerReqStopAllSessions)
			if ok {
				return instance.HandleStopAllSessions(newRequest.OnComplete)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(managerReqStopAllSessions{}),
		), err, true)
	}

	return instance, nil
}

// Start the session manager
func (r *managerImpl) Start(ctx context.Context) error {
	// Only a NEW manager can start. A failed CAS means the manager is either already RUNNING or
	// has been STOPPED (terminal); distinguish the two for the caller.
	if !r.lifecycle.CompareAndSwap(managerStateNew, managerStateRunning) {
		if r.lifecycle.Load() == managerStateStopped {
			return goutils.NewRuntimeError(
				"session manager already stopped and can't start again", nil, true,
			)
		}
		return goutils.NewConsistencyError("session manager already running", nil, true)
	}

	// ------------------------------------------------------------------------------------
	// For all session in READY state, move them back to IDLE

	// On any failure, roll RUNNING back to NEW so the start can be retried. CAS (not Store) so a
	// concurrent Stop that already moved us to STOPPED is not clobbered.
	success := false
	defer func() {
		if !success {
			r.lifecycle.CompareAndSwap(managerStateRunning, managerStateNew)
		}
	}()

	if dbErr := r.persistence.UseDatabaseInTransaction(
		ctx, func(dbCtx context.Context, dbClient db.Database) error {
			readySessions, err := dbClient.ListSessions(dbCtx, db.SessionQueryFilter{
				TargetStates: []models.SessionStateENUMType{models.SessionStateReady},
			})
			if err != nil {
				return goutils.NewRuntimeError("unable to list READY sessions", err, true)
			}

			for _, oneSession := range readySessions {
				if err := dbClient.MarkSessionIdle(dbCtx, oneSession.Name); err != nil {
					return goutils.NewRuntimeError(
						"unable to mark session "+oneSession.Name+" IDLE", err, true,
					)
				}
			}

			return nil
		},
	); dbErr != nil {
		return goutils.NewRuntimeError("failed to transition all running session to idle", dbErr, true)
	}

	// ------------------------------------------------------------------------------------
	// Start the task processor

	if err := r.worker.StartEventLoop(&r.wg); err != nil {
		return goutils.NewRuntimeError("failed to start manager support worker", err, true)
	}

	// ------------------------------------------------------------------------------------
	// Can start accepting session start requests
	r.allowNewStarts.Store(true)
	success = true
	return nil
}

// Stop the session manager
func (r *managerImpl) Stop(ctx context.Context) error {
	logTags := r.GetLogTagsForContext(ctx)

	// Only the call that transitions out of RUNNING performs the teardown below. Any other call
	// is a NOP, but still drive a NEW (never started) manager to STOPPED so that, per contract,
	// once Stop has been called no future Start can succeed.
	if !r.lifecycle.CompareAndSwap(managerStateRunning, managerStateStopped) {
		r.lifecycle.CompareAndSwap(managerStateNew, managerStateStopped)
		return nil
	}

	// ------------------------------------------------------------------------------------
	// Shutdown and stop all session runner

	var stopErr error

	if err := r.StopAllSessions(ctx, true); err != nil {
		stopErr = err
		log.WithError(err).WithFields(logTags).Error("failed to stop all active session runners")
	}

	// ------------------------------------------------------------------------------------
	// Close working context and wait for everything to stop

	r.workingCtxCancel()

	if err := r.worker.StopEventLoop(); err != nil {
		stopErr = goutils.NewRuntimeError("failed to stop manager worker tasks", err, true)
	}

	if err := goutils.TimeBoundedWaitGroupWait(ctx, &r.wg, time.Second*10); err != nil {
		stopErr = goutils.NewRuntimeError("manager support did not stop in time", err, true)
	}

	return stopErr
}

// logOnlyCompletion builds an OnComplete callback that only logs failures. It is used for
// non-blocking (fire-and-forget) request submission, where no caller awaits the result.
func (r *managerImpl) logOnlyCompletion(action string) func(context.Context, error) {
	return func(ctx context.Context, err error) {
		if err != nil {
			log.
				WithError(err).
				WithFields(r.GetLogTagsForContext(ctx)).
				Errorf("Async '%s' request failed", action)
		}
	}
}

// ======================================================================================
// Load And Start Session Runner

// managerReqStartSession manager request 'StartSession'
type managerReqStartSession struct {
	// SessionName the session to load
	SessionName string
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
}

/*
StartSession bring up a session runner for an existing session, and start the runner.

	@param ctx context.Context - execution context
	@param sessionName string - session to bring up
	@param blocking bool - whether this is a blocking call
*/
func (r *managerImpl) StartSession(ctx context.Context, sessionName string, blocking bool) error {
	if !blocking {
		if err := r.worker.Submit(ctx, managerReqStartSession{
			OnComplete: r.logOnlyCompletion("start-session"), SessionName: sessionName,
		}); err != nil {
			return models.NewSessionManagerStartSessionError(
				"failed to submit start session "+sessionName+" request", err, true,
			)
		}
		return nil
	}

	type completion struct {
		err error
	}

	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(ctx, managerReqStartSession{
		OnComplete: respCapture, SessionName: sessionName,
	}); err != nil {
		return models.NewSessionManagerStartSessionError(
			"failed to submit start session "+sessionName+" request", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionManagerStartSessionError(
				"session "+sessionName+" start request context ended", err, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionManagerStartSessionError(
				"session "+sessionName+" request-start response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleStartSession handler function called by worker task engine to process `StartSession`

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param sessionName string - session to bring up
	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *managerImpl) HandleStartSession(
	sessionName string, onComplete func(ctx context.Context, err error),
) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	// Is the manager still accepting start requests
	if !r.allowNewStarts.Load() {
		exitErr := models.NewSessionManagerStartSessionError(
			"manager not accepting session start requests", nil, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Start session " + sessionName + " failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Sanity check that the session exist
	var sessionEntry models.Session
	if dbErr := r.persistence.UseDatabaseInTransaction(
		r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); dbErr != nil {
		exitErr := models.NewSessionManagerStartSessionError(
			"failed to verify session "+sessionName+" is valid", dbErr, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Start session " + sessionName + " failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// There can't be an existing runner
	if _, foundExistingRunner := r.activeRunners[sessionEntry.Name]; foundExistingRunner {
		exitErr := models.NewSessionManagerStartSessionError(
			"session "+sessionEntry.Name+" runner already present", nil, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Start session " + sessionName + " failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	forceSessionToIdle := func() error {
		// Force the session back to IDLE
		dbErr := r.persistence.UseDatabaseInTransaction(
			r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionIdle(ctx, sessionEntry.Name)
			},
		)
		if dbErr != nil {
			exitErr := models.NewSessionManagerStartSessionError(
				"failed to move session "+sessionName+" back to IDLE", dbErr, true,
			)
			return exitErr
		}
		return nil
	}

	// Session needs to be in IDLE state
	if sessionEntry.State != models.SessionStateIdle {
		log.WithFields(logTags).Warnf("Session %s is READY with no associated runner", sessionName)
		// Force the session back to IDLE
		if err := forceSessionToIdle(); err != nil {
			log.
				WithError(err).
				WithFields(logTags).
				Error("Start session " + sessionName + " failed")
			onComplete(r.workingCtx, err)
			return err
		}
	}

	// Define a new runner
	newRunner, err := r.runnerFactory(r.workingCtx, NewSessionRunnerParams{
		SessionName:        sessionEntry.Name,
		PersistenceFactory: r.persistenceFactory,
		RedisClient:        r.redisClient,
		DriverFactory:      r.driverFactory,
		WorkerFactory:      r.workerFactory,
		SessionIdleNotify: func() {
			r.HandleSessionIdleNotify(sessionEntry.Name)
		},
		IPCProcessErrorNotify: func(err error) {
			r.HandleSessionIPCProcessError(sessionEntry.Name, err)
		},
	})
	if err != nil {
		exitErr := models.NewSessionManagerStartSessionError(
			"failed to define session "+sessionEntry.Name+" runner", err, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Start session " + sessionName + " failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	cleanUpOnFail := func() {
		err := newRunner.Stop(r.workingCtx)
		if err != nil {
			exitErr := models.NewSessionManagerStartSessionError(
				"failed to stop session "+sessionName+" runner", err, true,
			)
			log.
				WithError(exitErr).
				WithFields(logTags).
				Error("Failed session " + sessionName + " start clean up encountered issues")
		}
		// Force the session back to IDLE
		err = forceSessionToIdle()
		if err != nil {
			log.
				WithError(err).
				WithFields(logTags).
				Error("Failed session " + sessionName + " start clean up encountered issues")
		}
	}

	// Start the runner
	if err := newRunner.Start(r.workingCtx); err != nil {
		// Shut the runner down just in case it partially started.
		defer cleanUpOnFail()
		exitErr := models.NewSessionManagerStartSessionError(
			"failed to start session "+sessionEntry.Name+" runner", err, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Start session " + sessionName + " failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Start the driver
	if err := newRunner.StartSession(r.workingCtx, true); err != nil {
		// Shut the runner down just in case it partially started.
		defer cleanUpOnFail()
		exitErr := models.NewSessionManagerStartSessionError(
			"failed to start session "+sessionEntry.Name+" driver", err, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Start session " + sessionName + " failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Record the runner
	r.activeRunners[sessionEntry.Name] = newRunner

	// Notify original requester
	onComplete(r.workingCtx, nil)
	return nil
}

// ======================================================================================
// Stop And Unload Session Runner

// managerReqStopSession manager request 'StopSession'
type managerReqStopSession struct {
	// SessionName the session to unload
	SessionName string
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
}

/*
StopSession bring the session back to IDLE and unload its runner.

	@param ctx context.Context - execution context
	@param sessionName string - session to unload
	@param blocking bool - whether this is a blocking call
*/
func (r *managerImpl) StopSession(ctx context.Context, sessionName string, blocking bool) error {
	if !blocking {
		if err := r.worker.Submit(ctx, managerReqStopSession{
			OnComplete: r.logOnlyCompletion("stop-session"), SessionName: sessionName,
		}); err != nil {
			return models.NewSessionManagerStopSessionError(
				"failed to submit stop session "+sessionName+" request", err, true,
			)
		}
		return nil
	}

	type completion struct {
		err error
	}

	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(ctx, managerReqStopSession{
		OnComplete: respCapture, SessionName: sessionName,
	}); err != nil {
		return models.NewSessionManagerStopSessionError(
			"failed to submit stop session "+sessionName+" request", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionManagerStopSessionError(
				"session "+sessionName+" stop request context ended", err, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionManagerStopSessionError(
				"session "+sessionName+" request-stop response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleStopSession handler function called by worker task engine to process `StopSession`

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param sessionName string - session to unload
	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *managerImpl) HandleStopSession(
	sessionName string, onComplete func(ctx context.Context, err error),
) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	// There must be an existing runner. If there isn't, the session is already unloaded
	// and stopping is a NOP.
	runner, foundExistingRunner := r.activeRunners[sessionName]
	if !foundExistingRunner {
		log.WithFields(logTags).Warnf("Session %s is not running. Stop is NOP.", sessionName)
		onComplete(r.workingCtx, nil)
		return nil
	}

	// Bring the session driver back to IDLE
	if err := runner.StopSession(r.workingCtx, true); err != nil {
		exitErr := models.NewSessionManagerStopSessionError(
			"failed to stop session "+sessionName+" driver", err, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Errorf("Failed to stop session %s driver", sessionName)
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Tear the runner down.
	//
	// On failure the runner is intentionally KEPT in activeRunners and the error is surfaced to
	// the requester. A failed Stop means teardown could not be confirmed (e.g. threads did not
	// exit within the wait bound), so the runner's goroutines may still be alive and holding the
	// session's REDIS IPC queue / PTY. Retaining the reference keeps the `foundExistingRunner`
	// guard active, which blocks a future StartSession from spinning up a second runner that
	// would double-attach to the same session resources. This leaves the session wedged until
	// an operator inspects the logs and intervenes — a deliberate tradeoff over silent leakage.
	if err := runner.Stop(r.workingCtx); err != nil {
		exitErr := models.NewSessionManagerStopSessionError(
			"failed to unload session "+sessionName+" runner", err, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Errorf("Failed to unload session %s runner", sessionName)
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Forget the runner
	delete(r.activeRunners, sessionName)

	// Notify original requester
	onComplete(r.workingCtx, nil)
	return nil
}

// ======================================================================================
// Change Session Output Buffer Capacity

// managerReqChangeOutBufferCap manager request `ChangeOutputBufferCapacity`
type managerReqChangeOutBufferCap struct {
	// SessionName the session to change
	SessionName string
	// NewCapacity new session output buffer capacity
	NewCapacity int64
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
}

/*
ChangeOutputBufferCapacity change session output buffer capacity.

This can only be performed on IDLE sessions.

	@param ctx context.Context - execution context
	@param sessionName string - session to change
	@param newCap int64 - new output buffer capacity
	@param blocking bool - whether this is a blocking call
*/
func (r *managerImpl) ChangeOutputBufferCapacity(
	ctx context.Context, sessionName string, newCap int64, blocking bool,
) error {
	if !blocking {
		if err := r.worker.Submit(ctx, managerReqChangeOutBufferCap{
			SessionName: sessionName,
			NewCapacity: newCap,
			OnComplete:  r.logOnlyCompletion("change-out-buf-cap"),
		}); err != nil {
			return models.NewSessionManagerChangeOutputBufferCapError(
				"failed to submit session "+sessionName+" change output buffer cap request", err, true,
			)
		}
		return nil
	}

	type completion struct {
		err error
	}

	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(ctx, managerReqChangeOutBufferCap{
		OnComplete: respCapture, SessionName: sessionName, NewCapacity: newCap,
	}); err != nil {
		return models.NewSessionManagerChangeOutputBufferCapError(
			"failed to submit session "+sessionName+" change output buffer cap request", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionManagerChangeOutputBufferCapError(
				"session "+sessionName+" change output buffer cap request context ended", err, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionManagerChangeOutputBufferCapError(
				"session "+sessionName+" request-buf-cap-change response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleChangeOutputBufferCapacity handler function called by worker task engine to
process `ChangeOutputBufferCapacity`

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param sessionName string - session to change
	@param newCap int64 - new output buffer capacity
	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *managerImpl) HandleChangeOutputBufferCapacity(
	sessionName string, newCap int64, onComplete func(ctx context.Context, err error),
) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	// Sanity check that the session exist
	var sessionEntry models.Session
	if dbErr := r.persistence.UseDatabaseInTransaction(
		r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessionEntry, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	); dbErr != nil {
		exitErr := models.NewSessionManagerChangeOutputBufferCapError(
			"failed to verify session "+sessionName+" is valid", dbErr, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Change session " + sessionName + " output buffer capacity failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// There can't be an existing runner
	if _, foundExistingRunner := r.activeRunners[sessionEntry.Name]; foundExistingRunner {
		// Wrap a ConsistencyError so the API layer can surface this as a 409 Conflict: the change
		// is only permitted while the session is IDLE with no active runner.
		exitErr := models.NewSessionManagerChangeOutputBufferCapError(
			"session "+sessionEntry.Name+" currently active",
			goutils.NewConsistencyError(
				"session "+sessionEntry.Name+" output buffer capacity can only change while IDLE",
				nil, true,
			),
			true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Change session " + sessionName + " output buffer capacity failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	forceSessionToIdle := func() error {
		// Force the session back to IDLE
		dbErr := r.persistence.UseDatabaseInTransaction(
			r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionIdle(ctx, sessionEntry.Name)
			},
		)
		if dbErr != nil {
			exitErr := models.NewSessionManagerChangeOutputBufferCapError(
				"failed to move session "+sessionName+" back to IDLE", dbErr, true,
			)
			return exitErr
		}
		return nil
	}

	// Session needs to be in IDLE state
	if sessionEntry.State != models.SessionStateIdle {
		log.WithFields(logTags).Warnf("Session %s is READY with no associated runner", sessionName)
		// Force the session back to IDLE
		if err := forceSessionToIdle(); err != nil {
			log.
				WithError(err).
				WithFields(logTags).
				Error("Change session " + sessionName + " output buffer capacity failed")
			onComplete(r.workingCtx, err)
			return err
		}
	}

	// This updates two distinct storage systems (the REDIS buffer and the DB record), so some
	// sync skew is unavoidable. Delete the buffer FIRST, then update the record. If the delete
	// succeeds but the record update fails, the call can simply be retried: DeleteRingBuffer is a
	// REDIS DEL, which is idempotent (deleting an already-absent buffer is a no-op, not an error),
	// so the retry harmlessly re-runs the delete and re-attempts the record update. Do not flip
	// this ordering: updating the record first would leave the buffer reflecting a capacity the
	// record no longer advertises if the delete then failed.
	//
	// Until the record update commits, the record still shows the old capacity while the buffer is
	// gone. This only runs on IDLE sessions with no active runner, so nothing reads the buffer in
	// that window, and it is lazily recreated at the record's capacity on the next session start.

	// Delete the existing output buffer
	if err := r.redisClient.DeleteRingBuffer(
		r.workingCtx, BuildSessionOutputBufferName(sessionEntry.ID),
	); err != nil {
		exitErr := models.NewSessionManagerChangeOutputBufferCapError(
			"failed to delete session "+sessionName+" existing output buffer", err, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Change session " + sessionName + " output buffer capacity failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Change the session output buffer capacity
	if dbErr := r.persistence.UseDatabaseInTransaction(
		r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionOutputBufCapacity(ctx, sessionName, newCap)
		},
	); dbErr != nil {
		exitErr := models.NewSessionManagerChangeOutputBufferCapError(
			"failed to change session "+sessionName+" output buffer capacity", dbErr, true,
		)
		log.
			WithError(exitErr).
			WithFields(logTags).
			Error("Change session " + sessionName + " output buffer capacity failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	// Notify original requester
	onComplete(r.workingCtx, nil)
	return nil
}

// ======================================================================================
// Runner Support Callbacks

/*
HandleSessionIdleNotify callback function used by session runner to indicate the
managed session went IDLE before any shutdown command was given.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param sessionName string - the session which stopped
*/
func (r *managerImpl) HandleSessionIdleNotify(sessionName string) {
	logTags := r.GetLogTagsForContext(r.workingCtx)
	log.WithFields(logTags).Warn("Session " + sessionName + " stopped on its own")
	if err := r.StopSession(r.workingCtx, sessionName, false); err != nil {
		log.
			WithError(err).
			WithFields(logTags).
			Error("Failed to submit session " + sessionName + " stop request")
	}
}

/*
HandleSessionIPCProcessError callback function used by session runner to indicate the
managed session encountered issues operating the REDIS IPC queue.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param sessionName string - the session which stopped
*/
func (r *managerImpl) HandleSessionIPCProcessError(sessionName string, sessionErr error) {
	logTags := r.GetLogTagsForContext(r.workingCtx)
	log.
		WithError(sessionErr).
		WithFields(logTags).
		Error("Session " + sessionName + " encountered issues operating the IPC queue")
	if err := r.StopSession(r.workingCtx, sessionName, false); err != nil {
		log.
			WithError(err).
			WithFields(logTags).
			Error("Failed to submit session " + sessionName + " stop request")
	}
}

// ======================================================================================
// Stop All Session Runner

// managerReqStopAllSessions manager request 'StopAllSessions'
type managerReqStopAllSessions struct {
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
}

/*
StopAllSessions tear down every currently active session runner.

This is a bulk shutdown intended only for use while the manager itself is stopping.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param ctx context.Context - execution context
	@param blocking bool - whether this is a blocking call
*/
func (r *managerImpl) StopAllSessions(ctx context.Context, blocking bool) error {
	if !blocking {
		if err := r.worker.Submit(ctx, managerReqStopAllSessions{
			OnComplete: r.logOnlyCompletion("stop-all-sessions"),
		}); err != nil {
			return models.NewSessionManagerStopAllSessionsError(
				"failed to submit stop all sessions request", err, true,
			)
		}
		return nil
	}

	type completion struct {
		err error
	}

	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(ctx, managerReqStopAllSessions{
		OnComplete: respCapture,
	}); err != nil {
		return models.NewSessionManagerStopAllSessionsError(
			"failed to submit stop all sessions request", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionManagerStopAllSessionsError(
				"stop all sessions request context ended", err, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionManagerStopAllSessionsError(
				"stop all sessions response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleStopAllSessions handler function called by worker task engine to process
`StopAllSessions`.

Each active runner is simply torn down via its `Stop`; the session is NOT transitioned back
to IDLE here. On the next manager start, any session left in READY state is reset to IDLE.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *managerImpl) HandleStopAllSessions(onComplete func(ctx context.Context, err error)) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	// Stop accepting new start requests
	r.allowNewStarts.Store(false)

	var stopErr error

	// Tear down every active runner. The session is left in READY; the next manager start
	// will reset any READY session back to IDLE.
	for sessionName, runner := range r.activeRunners {
		if err := runner.Stop(r.workingCtx); err != nil {
			log.
				WithError(err).
				WithFields(logTags).
				Errorf("Failed to stop session %s runner during bulk shutdown", sessionName)
			if stopErr == nil {
				stopErr = models.NewSessionManagerStopAllSessionsError(
					"failed to stop session "+sessionName+" runner", err, true,
				)
			}
		}
		delete(r.activeRunners, sessionName)
	}

	onComplete(r.workingCtx, stopErr)
	return stopErr
}
