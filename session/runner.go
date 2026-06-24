package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/common"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/oklog/ulid/v2"
)

// Runner entity responsible for operating a PTY session
type Runner interface {
	// Start the session runner
	Start(ctx context.Context) error

	// Stop the session runner
	Stop(ctx context.Context) error

	/*
		StartSession bring the session to `READY` state

			@param ctx context.Context - execution context
			@param blocking bool - if true, block until the request completes; if false,
			    submit the request and return immediately
	*/
	StartSession(ctx context.Context, blocking bool) error

	/*
		StopSession bring the session back to `IDLE` state

			@param ctx context.Context - execution context
			@param blocking bool - if true, block until the request completes; if false,
			    submit the request and return immediately
	*/
	StopSession(ctx context.Context, blocking bool) error

	/*
		SubmitCommands submit commands to the session driver

			@param ctx context.Context - execution context
			@param commands []models.SessionInputCommand - commands to submit to driver
	*/
	SubmitCommands(ctx context.Context, commands []models.SessionInputCommand) error

	// ------------------------------------------------------------------------------------
	// Exposed for testing purposes

	/*
		HandleStartSession handler function called by worker task engine to process `StartSession`.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleStartSession(onComplete func(ctx context.Context, err error)) error

	/*
		HandleStopSession handler function called by worker task engine to process `StopSession`.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleStopSession(onComplete func(ctx context.Context, err error)) error

	/*
		HandleSubmitCommands handler function called by worker task engine to process `SubmitCommands`.

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param commands []models.SessionInputCommand - commands to submit to the driver
			@param onComplete func(ctx context.Context, err error) - callback function triggered
			    upon completion
	*/
	HandleSubmitCommands(
		commands []models.SessionInputCommand, onComplete func(ctx context.Context, err error),
	) error

	/*
		HandleIPCRequestMessage handler function called by IPC receive loop to process
		individual messages

		Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

			@param newMessage models.IPCMessageEnvelope - message to process
	*/
	HandleIPCRequestMessage(newMessage models.IPCMessageEnvelope) error
}

// runnerImpl implements Runner
type runnerImpl struct {
	goutils.Component

	sessionName string
	sessionID   string

	// workingCtx the context the runner operates in
	workingCtx       context.Context
	workingCtxCancel context.CancelFunc

	// wg for threads spawn directly by the runner
	wg sync.WaitGroup

	// persistence DB persistence layer handle
	persistence db.Client

	// driver for this session runner
	driver Driver

	// redisClient client to REDIS
	redisClient goutilsRedis.Client

	// session this runner is responsible for
	session models.Session

	validator *validator.Validate

	// sessionIdleNotify callback function to trigger when the session went back to the IDLE state
	sessionIdleNotify func()

	// ipcProcessErrorNotify callback function to trigger when the IPC read process
	// encounters errors
	ipcProcessErrorNotify func(err error)

	// worker support task processing inbound requests
	worker goutils.TaskProcessor
}

// NewSessionRunnerParams parameters for defining a new session runner
type NewSessionRunnerParams struct {
	// SessionName name of session to operate
	SessionName string `validate:"required,session_name_type"`

	// PersistenceFactory factory function to generate prepare new persistence clients
	PersistenceFactory func() (db.Client, error) `validate:"required"`

	// RedisClient the REDIS client
	RedisClient goutilsRedis.Client `validate:"required"`

	// DriverFactory session driver factory function
	DriverFactory driverFactoryFunc `validate:"required"`

	// WorkerFactory worker task processor factory function
	WorkerFactory taskProcessorFactoryFunc `validate:"required"`

	// SessionIdleNotify callback function to trigger when the session went back to the IDLE state
	SessionIdleNotify func() `validate:"required"`

	// IPCProcessErrorNotify callback function to trigger when the IPC read process
	// encounters errors
	IPCProcessErrorNotify func(err error) `validate:"required"`
}

/*
NewSessionRunner define a new runner to operate a particular PTY session

	@param parentCtx context.Context - parent context for the runner
	@param params NewSessionRunnerParams - initialization parameters
	@returns new runner
	@returns `goutils.RuntimeError` sub-component initialization failed
	@returns `models.UnknownSessionError` referenced session is unknown
	@returns `models.PersistenceError` persistence layer failure
*/
func NewSessionRunner(parentCtx context.Context, params NewSessionRunnerParams) (Runner, error) {
	validate := validator.New()
	if err := models.RegisterWithValidator(validate); err != nil {
		return nil, goutils.NewRuntimeError("failed to install custom validation macros", err, true)
	}

	// Validate initialization parameters
	if err := validate.Struct(&params); err != nil {
		return nil, goutils.NewValidationError("session runner init param invalid", err, true)
	}

	persistence, err := params.PersistenceFactory()
	if err != nil {
		return nil, goutils.NewRuntimeError(
			"failed to prepare scoped persistence for PTY runner", err, true,
		)
	}

	var session models.Session
	if dbErr := persistence.UseDatabaseInTransaction(
		parentCtx, func(dbCtx context.Context, dbClient db.Database) error {
			var err error
			session, err = dbClient.GetSessionByName(dbCtx, params.SessionName)
			return err
		},
	); dbErr != nil {
		return nil, goutils.NewRuntimeError("unable to load session "+params.SessionName, dbErr, true)
	}

	logTags := log.Fields{
		"module":       "session",
		"component":    "session-runner",
		"session":      session.ID,
		"session-name": session.Name,
		"instance":     ulid.Make().String(),
	}

	instance := &runnerImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		sessionName:           session.Name,
		sessionID:             session.ID,
		persistence:           persistence,
		driver:                nil,
		redisClient:           params.RedisClient,
		session:               session,
		validator:             validate,
		wg:                    sync.WaitGroup{},
		sessionIdleNotify:     params.SessionIdleNotify,
		ipcProcessErrorNotify: params.IPCProcessErrorNotify,
	}
	instance.workingCtx, instance.workingCtxCancel = context.WithCancel(parentCtx)

	// ------------------------------------------------------------------------------------
	// Prepare the driver

	instance.driver, err = params.DriverFactory(
		parentCtx, session, params.RedisClient, func() {
			lclCtx, lclCtxCancel := context.WithTimeout(context.Background(), time.Second*5)
			defer lclCtxCancel()
			logTags := instance.GetLogTagsForContext(lclCtx)
			log.WithFields(logTags).Warn("Session command stopped prematurely")
			if err := instance.StopSession(lclCtx, false); err != nil {
				log.WithError(err).WithFields(logTags).Error("Failed to request stop session")
			}
		},
	)
	if err != nil {
		return nil, goutils.NewRuntimeError("failed to define session driver", err, true)
	}

	// ------------------------------------------------------------------------------------
	// Prepare worker task engine

	instance.worker, err = params.WorkerFactory(
		instance.workingCtx,
		instance.session.Name+".runner",
		10,
		log.Fields{
			"module":        "session",
			"component":     "session-runner",
			"sub-component": "task-engine",
			"session":       session.ID,
			"session-name":  session.Name,
			"instance":      ulid.Make().String(),
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
		reflect.TypeOf(runnerReqStartSession{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(runnerReqStartSession)
			if ok {
				return instance.HandleStartSession(newRequest.OnComplete)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(runnerReqStartSession{}),
		), err, true)
	}

	// Pending request to stop the session
	if err := instance.worker.AddToTaskExecutionMap(
		reflect.TypeOf(runnerReqStopSession{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(runnerReqStopSession)
			if ok {
				return instance.HandleStopSession(newRequest.OnComplete)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(runnerReqStopSession{}),
		), err, true)
	}

	// Pending request to submit commands to a session
	if err := instance.worker.AddToTaskExecutionMap(
		reflect.TypeOf(runnerReqSubmitCommands{}),
		func(taskParam interface{}) error {
			newRequest, ok := taskParam.(runnerReqSubmitCommands)
			if ok {
				return instance.HandleSubmitCommands(newRequest.Commands, newRequest.OnComplete)
			}
			return fmt.Errorf("received unexpected call parameters: %s", reflect.TypeOf(taskParam))
		},
	); err != nil {
		return nil, goutils.NewRuntimeError(fmt.Sprintf(
			"failed to register '%s' handler with worker", reflect.TypeOf(runnerReqSubmitCommands{}),
		), err, true)
	}

	return instance, nil
}

// Start the session runner
func (r *runnerImpl) Start(_ context.Context) error {
	// ------------------------------------------------------------------------------------
	// Start REDIS queue reader task

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.processIPCRequests()
	}()

	// ------------------------------------------------------------------------------------
	// Start the task engine

	if err := r.worker.StartEventLoop(&r.wg); err != nil {
		return goutils.NewRuntimeError("failed to start session support worker", err, true)
	}

	return nil
}

// Stop the session runner
func (r *runnerImpl) Stop(ctx context.Context) error {
	logTags := r.GetLogTagsForContext(ctx)

	// Stop runner operating context
	r.workingCtxCancel()

	var stopErr error

	if err := r.driver.Stop(ctx); err != nil {
		log.WithError(err).WithFields(logTags).Error("Failed to stop session driver")
	}

	if err := r.worker.StopEventLoop(); err != nil {
		stopErr = goutils.NewRuntimeError(
			"failed to stop session "+r.sessionName+" worker tasks", err, true,
		)
	}

	if err := goutils.TimeBoundedWaitGroupWait(ctx, &r.wg, time.Second*10); err != nil {
		stopErr = goutils.NewRuntimeError(
			"session "+r.sessionName+" support threads did not stop in time", err, true,
		)
	}

	return stopErr
}

// logOnlyCompletion builds an OnComplete callback that only logs failures. It is used for
// non-blocking (fire-and-forget) request submission, where no caller awaits the result.
func (r *runnerImpl) logOnlyCompletion(action string) func(context.Context, error) {
	return func(ctx context.Context, err error) {
		if err != nil {
			log.
				WithError(err).
				WithFields(r.GetLogTagsForContext(ctx)).
				Errorf("Async '%s' request for session %s failed", action, r.sessionName)
		}
	}
}

// ======================================================================================
// Start Session Runner

// runnerReqStartSession runner request 'StartSession'
type runnerReqStartSession struct {
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
}

/*
StartSession bring the session to `READY` state

	@param ctx context.Context - execution context
*/
func (r *runnerImpl) StartSession(ctx context.Context, blocking bool) error {
	// Non-blocking: submit the request with a log-only completion callback and return.
	if !blocking {
		if err := r.worker.Submit(
			ctx, runnerReqStartSession{OnComplete: r.logOnlyCompletion("start-session")},
		); err != nil {
			return models.NewSessionRunnerStartUpError(
				"failed to submit start request to session "+r.sessionName+" driver", err, true,
			)
		}
		return nil
	}

	type completion struct {
		err error
	}

	// NOTE: respChan is buffered and intentionally NOT closed. On the ctx.Done()
	// path this function returns while the worker task is still in-flight; the task
	// will later invoke respCapture. Closing here would make that a send on a closed
	// channel (panic). Leaving it open lets the buffered send succeed harmlessly and
	// the channel is reclaimed by GC.
	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(ctx, runnerReqStartSession{OnComplete: respCapture}); err != nil {
		return models.NewSessionRunnerStartUpError(
			"failed to submit start request to session "+r.sessionName+" driver", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionRunnerStartUpError(
				"session "+r.sessionName+" start request context ended", nil, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionRunnerStartUpError(
				"session "+r.sessionName+" request-start response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleStartSession handler function called by worker task engine to process `StartSession`.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *runnerImpl) HandleStartSession(onComplete func(ctx context.Context, err error)) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	var updatedSession models.Session

	// Start the session driver. The session is only transitioned to `READY` once the driver is up.
	// A ConsistencyError means the driver is already running; treat that as idempotent success and
	// proceed to (re)assert the session state.
	if err := r.driver.Start(r.workingCtx); err != nil {
		var consistencyErr goutils.ConsistencyError
		if !errors.As(err, &consistencyErr) {
			exitErr := models.NewSessionRunnerStartUpError(
				"failed to start session "+r.sessionName+" driver", err, true,
			)
			log.WithError(exitErr).WithFields(logTags).Error("Session driver failed to start")
			onComplete(r.workingCtx, exitErr)
			return exitErr
		}
		log.WithFields(logTags).Info("Session driver already running; treating start as idempotent")
	}

	// Move a session to `READY`
	if dbErr := r.persistence.UseDatabaseInTransaction(
		r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
			if err := dbClient.MarkSessionReady(ctx, r.sessionName); err != nil {
				return err
			}
			var err error
			updatedSession, err = dbClient.GetSessionByName(ctx, r.sessionName)
			return err
		},
	); dbErr != nil {
		exitErr := models.NewSessionRunnerStartUpError(
			"failed to transition session "+r.sessionName+" to READY", dbErr, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session transition to READY failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}
	r.session = updatedSession

	onComplete(r.workingCtx, nil)
	return nil
}

// ======================================================================================
// Stop Session Runner

// runnerReqStopSession runner request 'StopSession'
type runnerReqStopSession struct {
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
}

/*
StopSession bring the session back to `IDLE` state

	@param ctx context.Context - execution context
*/
func (r *runnerImpl) StopSession(ctx context.Context, blocking bool) error {
	// Non-blocking: submit the request with a log-only completion callback and return.
	if !blocking {
		if err := r.worker.Submit(
			ctx, runnerReqStopSession{OnComplete: r.logOnlyCompletion("stop-session")},
		); err != nil {
			return models.NewSessionRunnerShutdownError(
				"failed to submit stop request to session "+r.sessionName+" driver", err, true,
			)
		}
		return nil
	}

	type completion struct {
		err error
	}

	// NOTE: respChan is buffered and intentionally NOT closed. On the ctx.Done()
	// path this function returns while the worker task is still in-flight; the task
	// will later invoke respCapture. Closing here would make that a send on a closed
	// channel (panic). Leaving it open lets the buffered send succeed harmlessly and
	// the channel is reclaimed by GC.
	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(ctx, runnerReqStopSession{OnComplete: respCapture}); err != nil {
		return models.NewSessionRunnerShutdownError(
			"failed to submit stop request to session "+r.sessionName+" driver", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionRunnerShutdownError(
				"session "+r.sessionName+" stop request context ended", nil, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionRunnerShutdownError(
				"session "+r.sessionName+" request-stop response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleStopSession handler function called by worker task engine to process `StopSession`.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *runnerImpl) HandleStopSession(onComplete func(ctx context.Context, err error)) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	var updatedSession models.Session

	// Stop the session driver. The session is only transitioned back to `IDLE` once the
	// driver has been torn down. A ConsistencyError means the driver is already stopped; treat
	// that as idempotent success and proceed to (re)assert the session state.
	if err := r.driver.Stop(r.workingCtx); err != nil {
		var consistencyErr goutils.ConsistencyError
		if !errors.As(err, &consistencyErr) {
			exitErr := models.NewSessionRunnerShutdownError(
				"failed to stop session "+r.sessionName+" driver", err, true,
			)
			log.WithError(exitErr).WithFields(logTags).Error("Session driver failed to stop")
			onComplete(r.workingCtx, exitErr)
			return exitErr
		}
		log.WithFields(logTags).Info("Session driver already stopped; treating stop as idempotent")
	}

	// Move a session to `IDLE`
	if dbErr := r.persistence.UseDatabaseInTransaction(
		r.workingCtx, func(ctx context.Context, dbClient db.Database) error {
			if err := dbClient.MarkSessionIdle(ctx, r.sessionName); err != nil {
				return err
			}
			var err error
			updatedSession, err = dbClient.GetSessionByName(ctx, r.sessionName)
			return err
		},
	); dbErr != nil {
		exitErr := models.NewSessionRunnerShutdownError(
			"failed to transition session "+r.sessionName+" to IDLE", dbErr, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session transition to IDLE failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}
	r.session = updatedSession

	// Notify higher layers that the session is back to `IDLE`
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if r.sessionIdleNotify != nil {
			r.sessionIdleNotify()
		}
	}()

	onComplete(r.workingCtx, nil)
	return nil
}

// ======================================================================================
// Process User Submitted Commands

// runnerReqSubmitCommands runner request `submitCommands`
type runnerReqSubmitCommands struct {
	// OnComplete callback function triggered upon completion
	OnComplete func(ctx context.Context, err error)
	// Commands the list of commands to send to the session
	Commands []models.SessionInputCommand
}

/*
SubmitCommands submit commands to the session driver

	@param ctx context.Context - execution context
	@param commands []models.SessionInputCommand - commands to submit to driver
*/
func (r *runnerImpl) SubmitCommands(
	ctx context.Context, commands []models.SessionInputCommand,
) error {
	type completion struct {
		err error
	}

	// NOTE: respChan is buffered and intentionally NOT closed. On the ctx.Done()
	// path this function returns while the worker task is still in-flight; the task
	// will later invoke respCapture. Closing here would make that a send on a closed
	// channel (panic). Leaving it open lets the buffered send succeed harmlessly and
	// the channel is reclaimed by GC.
	respChan := make(chan completion, 1)
	respCapture := func(_ context.Context, err error) {
		respChan <- completion{err: err}
	}

	if err := r.worker.Submit(
		ctx, runnerReqSubmitCommands{OnComplete: respCapture, Commands: commands},
	); err != nil {
		return models.NewSessionRunnerSubmitCommandError(
			"failed to submit run-command request to session "+r.sessionName+" driver", err, true,
		)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return models.NewSessionRunnerSubmitCommandError(
				"session "+r.sessionName+" run-command context ended", nil, true,
			)
		}

	case resp, ok := <-respChan:
		if !ok {
			return models.NewSessionRunnerSubmitCommandError(
				"session "+r.sessionName+" run-command response channel closed", nil, true,
			)
		}
		if resp.err != nil {
			return resp.err
		}
	}

	return nil
}

/*
HandleSubmitCommands handler function called by worker task engine to process `SubmitCommands`.

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param commands []models.SessionInputCommand - commands to submit to the driver
	@param onComplete func(ctx context.Context, err error) - callback function triggered
	    upon completion
*/
func (r *runnerImpl) HandleSubmitCommands(
	commands []models.SessionInputCommand, onComplete func(ctx context.Context, err error),
) error {
	logTags := r.GetLogTagsForContext(r.workingCtx)

	if r.session.RunnerMode != models.SessionRunnerModeTypeCommanded {
		exitErr := models.NewSessionRunnerSubmitCommandError(
			"session "+r.sessionName+" not running in COMMAND mode", nil, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session can't process commands")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	if r.session.State != models.SessionStateReady {
		exitErr := models.NewSessionRunnerSubmitCommandError(
			"session "+r.sessionName+" not ready to process commands yet", nil, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session can't process commands")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	serialized, err := models.BuildStdinInputFromCommands(commands)
	if err != nil {
		exitErr := models.NewSessionRunnerSubmitCommandError(
			"failed to build command for session "+r.sessionName, err, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session commands serialization error")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	inputBuf, err := r.redisClient.GetRingBuffer(
		r.workingCtx, BuildSessionInputBufferName(r.sessionID), r.session.OutputBufferCapacity,
	)
	if err != nil {
		exitErr := models.NewSessionRunnerSubmitCommandError(
			"failed to grab input buffer for session "+r.sessionName, err, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session commands serialization error")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	if _, err := inputBuf.Write(r.workingCtx, serialized); err != nil {
		exitErr := models.NewSessionRunnerSubmitCommandError(
			"failed to write commands into input buffer for session "+r.sessionName, err, true,
		)
		log.WithError(exitErr).WithFields(logTags).Error("Session commands write failed")
		onComplete(r.workingCtx, exitErr)
		return exitErr
	}

	onComplete(r.workingCtx, nil)
	return nil
}

// ======================================================================================
// Read IPC Request From REDIS

/*
HandleIPCRequestMessage handler function called by IPC receive loop to process individual messages

Only exposed for testing purposes. DO NOT DIRECTLY USE IN PRODUCTION.

	@param newMessage models.IPCMessageEnvelope - message to process
*/
func (r *runnerImpl) HandleIPCRequestMessage(newMessage models.IPCMessageEnvelope) error {
	// Parse the message
	msgAsStr, err := newMessage.StringPayload()
	if err != nil {
		return models.NewSessionRunnerIPCProcessError(
			"session "+r.sessionName+" received non-string IPC message", err, true,
		)
	}
	parsed, err := models.ParseIPCMessage(r.validator, []byte(msgAsStr))
	if err != nil {
		return models.NewSessionRunnerIPCProcessError(
			"session "+r.sessionName+" received unknown parsable IPC message", err, true,
		)
	}

	// Helper function to build the standard response message
	buildResp := func(
		reqID string, respIPCType models.IPCMessageTypeEnumType, cmdErr error,
	) models.IPCMessageEnvelope {
		resp := models.IPCMessageRespUniversal{
			BaseIPCMessage: models.BaseIPCMessage{
				RequestID: reqID,
				Type:      respIPCType,
				Sender:    "session-" + r.sessionName + ".runner",
				Timestamp: time.Now().UTC(),
			},
			Success: true,
		}
		if cmdErr != nil {
			resp.Success = false
			resp.ErrorMsg = common.GetTypedPtr(cmdErr.Error())
		}
		return resp
	}

	// Helper function to return a response back to the original requester
	returnResp := func(reqID string, resp models.IPCMessageEnvelope) error {
		respQueue, err := r.redisClient.GetQueueHandle(
			r.workingCtx, BuildSessionIPCRespQueueName(reqID),
		)
		if err != nil {
			return err
		}
		// Return the response
		ttl := time.Second * 60
		_, err = respQueue.PushRight(r.workingCtx, resp, &ttl)
		return err
	}

	switch typedIPC := parsed.(type) {
	case models.IPCMessageReqRunCommands:
		// Submit user commands to the session
		cmdErr := r.SubmitCommands(r.workingCtx, typedIPC.Commands)
		// Build reply
		reply := buildResp(typedIPC.RequestID, models.IPCMsgTypeRespRunCommands, cmdErr)
		// Send feedback to original requester
		if err := returnResp(typedIPC.RequestID, reply); err != nil {
			return models.NewSessionRunnerIPCProcessError(
				"failed to send response for session "+r.sessionName+" command submission", err, true,
			)
		}

	case models.IPCMessageReqStopSession:
		// Stop the session driver
		cmdErr := r.StopSession(r.workingCtx, typedIPC.Blocking)
		// Build reply
		reply := buildResp(typedIPC.RequestID, models.IPCMsgTypeRespStopSession, cmdErr)
		// Send feedback to original requester
		if err := returnResp(typedIPC.RequestID, reply); err != nil {
			return models.NewSessionRunnerIPCProcessError(
				"failed to send response for session "+r.sessionName+" stop request", err, true,
			)
		}

	default:
		return models.NewSessionRunnerIPCProcessError(fmt.Sprintf(
			"session %s received unsupported IPC message %s",
			r.sessionName,
			reflect.TypeOf(parsed).String(),
		), nil, true)
	}
	return nil
}

// processIPCRequests support task to process IPC requests send to the session runner
func (r *runnerImpl) processIPCRequests() {
	// Prepare IPC REDIS queue
	queue, err := r.redisClient.GetQueueHandle(r.workingCtx, BuildSessionIPCQueueName(r.sessionID))
	if err != nil {
		exitErr := models.NewSessionRunnerIPCProcessError(
			"failed to get session "+r.sessionName+" IPC queue handle", err, true,
		)
		r.ipcProcessErrorNotify(exitErr)
		return
	}

	logTags := r.GetLogTagsForContext(r.workingCtx)

	log.WithFields(logTags).Info("Starting session " + r.sessionName + " IPC receive loop")
	defer func() {
		log.WithFields(logTags).Info("Session " + r.sessionName + " IPC receive loop ended")
	}()

	for {
		// Check whether context expired
		if err := r.workingCtx.Err(); err != nil {
			break
		}

		newMessage, err := queue.PopLeft(r.workingCtx, true, nil)
		if err != nil {
			exitErr := models.NewSessionRunnerIPCProcessError(
				"IPC message read failure for session "+r.sessionName, err, true,
			)
			log.WithError(exitErr).WithFields(logTags).Error("Session IPC read failure")
			r.ipcProcessErrorNotify(exitErr)
			return
		}

		// Process the message
		//
		// Message-level errors (unparsable payload, unsupported message type, a rejected
		// command submission, etc.) are recoverable and MUST NOT tear down the receive
		// loop: a single malformed message from any client would otherwise permanently
		// kill IPC for this session. Log and continue to the next message. Only transport
		// failures (the PopLeft above) warrant tearing the loop down.
		if newMessage != nil {
			if err := r.HandleIPCRequestMessage(newMessage); err != nil {
				log.WithError(err).WithFields(logTags).Error("Session IPC process failure")
			}
		}
	}
}
