// Package session - shell session operation package
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/redis"
	"github.com/apex/log"
	"github.com/oklog/ulid/v2"
)

// Driver entity for running the session command, and transferring data to and from it
type Driver interface {
	/*
		Start the driver daemon threads.

		These support threads will read INPUT from REDIS buffers and write output to REDIS buffers.

			@param parentCtx context.Context - driver execution's parent context
			@returns `models.ConsistencyError` driver is already running
			@returns `models.RuntimeError` any starting issues encountered
			@returns `models.PTYError` PTY starting issues
	*/
	Start(parentCtx context.Context) error

	/*
		Stop the daemon threads of the driver

			@param ctx context.Context - execution context
			@returns `models.ConsistencyError` driver is not running
			@returns `models.RuntimeError` any shutdown issues encountered
	*/
	Stop(ctx context.Context) error
}

// CoreDriver core session driver which directly wraps around the support session driver
// controllers (i.e. PTY, docker, etc.)
type coreDriver interface {
	// Setup the driver
	Setup() error

	// PipeInput pipe user input into the process
	PipeInput(input io.Reader) error

	// PipeOutput pipe process output into buffer
	PipeOutput(output io.Writer) error

	// Wait for the process to stop
	Wait() error

	// TearDown the driver
	TearDown() error
}

// prepareCoreDriver setup the core session driver
func prepareCoreDriver(
	workingCtx context.Context, instanceID string, session models.Session,
) (coreDriver, error) {
	switch session.DriverType {
	case models.SessionDriverTypePTY:
		return newPTYCoreDriver(workingCtx, instanceID, session)
	default:
		return nil,
			models.ConsistencyError{Message: "Unknown session driver type " + string(session.DriverType)}
	}
}

// driverImpl implements Driver
type driverImpl struct {
	goutils.Component

	workingCtx       context.Context
	workingCtxCancel context.CancelFunc

	wg sync.WaitGroup

	session models.Session

	redisClient redis.Client

	instanceID string

	lock *sync.Mutex
	core coreDriver

	commandStopNotify func()

	inputBufferName  string
	outputBufferName string
}

// driverFactoryFunc function signature for a driver factory function
type driverFactoryFunc func(
	ctx context.Context, session models.Session, redisClient redis.Client, commandStopNotify func(),
) (Driver, error)

/*
NewDriver define a new driver instance for a session

`commandStopNotify` callback is used by the driver to notify higher layers that the command
being executed has ended before `Stop` on this driver was called. Due to implementation,
this callback MUST NOT directly trigger call to driver `Stop`.

	@param ctx context.Context - execution context
	@param session models.Session - the session this driver is for
	@param redisClient redis.Client - the REDIS client
	@param commandStopNotify func() - callback function to trigger when session command stopped
	    before driver `Stop` is called.
	@returns the new driver
*/
func NewDriver(
	_ context.Context,
	session models.Session,
	redisClient redis.Client,
	commandStopNotify func(),
) (Driver, error) {
	instanceID := ulid.Make().String()

	logTags := log.Fields{
		"module":       "session",
		"component":    "driver",
		"session":      session.ID,
		"session-name": session.Name,
		"instance":     instanceID,
	}

	instance := &driverImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		wg:                sync.WaitGroup{},
		session:           session,
		redisClient:       redisClient,
		instanceID:        instanceID,
		core:              nil,
		lock:              &sync.Mutex{},
		commandStopNotify: commandStopNotify,
		workingCtx:        nil,
		workingCtxCancel:  nil,
		inputBufferName:   BuildSessionInputBufferName(session.ID),
		outputBufferName:  BuildSessionOutputBufferName(session.ID),
	}

	return instance, nil
}

/*
Start the driver daemon threads.

These support threads will read INPUT from REDIS buffers and write output to REDIS buffers.

	@param parentCtx context.Context - driver execution's parent context
	@returns `models.ConsistencyError` driver is already running
	@returns `models.RuntimeError` any starting issues encountered
	@returns `models.PTYError` PTY starting issues
*/
func (r *driverImpl) Start(parentCtx context.Context) error {
	r.lock.Lock()
	defer func() {
		r.lock.Unlock()
	}()

	if r.core != nil {
		return models.ConsistencyError{
			Message: "session " + r.session.Name + " driver already running",
		}
	}

	// ------------------------------------------------------------------------------------
	// Prepare new working context for this run

	r.workingCtx, r.workingCtxCancel = context.WithCancel(parentCtx)

	// ------------------------------------------------------------------------------------
	// Prepare INPUT REDIS buffer

	if err := r.redisClient.DeleteRingBuffer(r.workingCtx, r.inputBufferName); err != nil {
		r.workingCtxCancel()
		return models.RuntimeError{
			Core: err, Message: "failed to delete existing session " + r.session.Name + " input buffer",
		}
	}
	inputBuf, err := r.redisClient.GetRingBuffer(
		r.workingCtx, r.inputBufferName, r.session.OutputBufferCapacity,
	)
	if err != nil {
		r.workingCtxCancel()
		return models.RuntimeError{
			Core: err, Message: "failed to define session " + r.session.Name + " input buffer",
		}
	}

	// ------------------------------------------------------------------------------------
	// Prepare OUTPUT REDIS buffer

	outputBuf, err := r.redisClient.GetRingBuffer(
		r.workingCtx, r.outputBufferName, r.session.OutputBufferCapacity,
	)
	if err != nil {
		r.workingCtxCancel()
		return models.RuntimeError{
			Core: err, Message: "failed to define session " + r.session.Name + " output buffer",
		}
	}

	cmdDisplayStr := ""
	{
		t := []string{r.session.Command.Command}
		t = append(t, r.session.Command.Arguments...)
		cmdDisplayStr = strings.Join(t, " ")
	}

	// Write a preamble into the output buffer to indicate when the PTY started
	if _, err = outputBuf.Write(
		r.workingCtx,
		[]byte(
			fmt.Sprintf(
				"\n\r============= [%s] STARTING '%s' =============\n\r",
				time.Now().UTC().String(),
				cmdDisplayStr,
			),
		),
	); err != nil {
		r.workingCtxCancel()
		return models.RuntimeError{
			Core: err, Message: "failed to write session " + r.session.Name + " preamble",
		}
	}

	// ------------------------------------------------------------------------------------
	// Start the core driver

	core, err := prepareCoreDriver(r.workingCtx, r.instanceID, r.session)
	if err != nil {
		r.workingCtxCancel()
		return models.RuntimeError{
			Core: err, Message: "core driver construction for session " + r.session.Name + " failed",
		}
	}
	if err := core.Setup(); err != nil {
		r.workingCtxCancel()
		return models.RuntimeError{
			Core: err, Message: "core driver setup for session " + r.session.Name + " failed",
		}
	}

	// ------------------------------------------------------------------------------------
	// Start Support Task - Copy data stream from input buffer to PTY

	r.wg.Add(1)
	go func(cd coreDriver) {
		defer r.wg.Done()
		logTags := r.GetLogTagsForContext(r.workingCtx)

		asReader := inputBuf.AsReadWriteCloser(r.workingCtx, time.Millisecond*10)
		defer func() {
			_ = asReader.Close()
		}()

		log.WithFields(logTags).Info("Starting INPUT transfer loop")
		defer func() {
			log.WithFields(logTags).Info("INPUT transfer loop ended")
		}()
		err := cd.PipeInput(asReader)
		if err != nil && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
			log.WithError(err).WithFields(logTags).Error("Write STDIN from REDIS failed")
		}
	}(core)

	// ------------------------------------------------------------------------------------
	// Start Support Task - Copy data stream from PTY to output buffer

	r.wg.Add(1)
	go func(cd coreDriver) {
		defer r.wg.Done()
		logTags := r.GetLogTagsForContext(r.workingCtx)

		asWriter := outputBuf.AsReadWriteCloser(r.workingCtx, time.Millisecond*10)
		defer func() {
			_ = asWriter.Close()
		}()

		log.WithFields(logTags).Info("Starting OUTPUT transfer loop")
		defer func() {
			log.WithFields(logTags).Info("OUTPUT transfer loop ended")
		}()
		err := cd.PipeOutput(asWriter)
		if err != nil && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
			log.WithError(err).WithFields(logTags).Error("Write STDOUT/STDERR to REDIS failed")
		}

		lclCtx, lclCtxCancel := context.WithTimeout(context.Background(), time.Second)
		defer lclCtxCancel()
		if _, err = outputBuf.Write(
			lclCtx,
			[]byte(
				fmt.Sprintf(
					"\n\r============= [%s] '%s' Stopped =============\n\r",
					time.Now().UTC().String(),
					cmdDisplayStr,
				),
			),
		); err != nil {
			log.
				WithError(err).
				WithFields(logTags).
				Error("failed to write session postamble")
		}
	}(core)

	// ------------------------------------------------------------------------------------
	// Start Support Task - Wait on core command to finish

	r.wg.Add(1)
	go func(cd coreDriver) {
		defer r.wg.Done()
		logTags := r.GetLogTagsForContext(r.workingCtx)

		// We assume that the main PTY child process should be finishing or finished as well
		if err := cd.Wait(); err != nil {
			log.WithError(err).WithFields(logTags).Errorf("CMD '%s' non-zero-return", cmdDisplayStr)
		}
		log.WithFields(logTags).Infof("Core command '%s' ended", cmdDisplayStr)

		// Signal to the parent that the core command finished
		if err := r.workingCtx.Err(); err == nil && r.commandStopNotify != nil {
			r.commandStopNotify()
		}
	}(core)

	// Record critical structures
	r.core = core

	return nil
}

/*
Stop the daemon threads of the driver

	@param ctx context.Context - execution context
	@returns `models.ConsistencyError` driver is not running
	@returns `models.RuntimeError` any shutdown issues encountered
*/
func (r *driverImpl) Stop(ctx context.Context) error {
	logTags := r.GetLogTagsForContext(ctx)

	r.lock.Lock()
	defer func() {
		r.lock.Unlock()
	}()

	if r.core == nil {
		return models.ConsistencyError{
			Message: "session " + r.session.Name + " driver is not running",
		}
	}

	r.workingCtxCancel()

	if err := r.core.TearDown(); err != nil {
		return models.RuntimeError{
			Core: err, Message: "session " + r.session.Name + " driver tear down failed",
		}
	}

	log.WithFields(logTags).Info("Core Driver teared down")

	// Wait for all daemon threads to end
	if err := goutils.TimeBoundedWaitGroupWait(ctx, &r.wg, time.Second*5); err != nil {
		return models.RuntimeError{
			Core: err, Message: "session " + r.session.Name + " support daemons did not stop in time",
		}
	}

	log.WithFields(logTags).Info("All driver support daemon threads stopped")

	// Clear records
	r.core = nil
	return nil
}
