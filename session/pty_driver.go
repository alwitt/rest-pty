package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/creack/pty"
	"github.com/go-playground/validator/v10"
)

// ptyDriver PTY based session driver which implements `coreDriver`
type ptyDriver struct {
	goutils.Component
	workingCtx context.Context
	coreCmd    *exec.Cmd
	ptyHandle  *os.File
	session    models.Session
	validator  *validator.Validate
	metadata   models.SessionDriverPTYParams

	// reaped is set once `Wait` has reaped the child. After reaping, the PID is freed and may
	// be recycled, so `TearDown` must not signal the (negative) PID anymore.
	reaped atomic.Bool
}

// newPTYCoreDriver define new PTY core session driver
func newPTYCoreDriver(
	workingCtx context.Context, instanceID string, session models.Session,
) (coreDriver, error) {
	logTags := log.Fields{
		"module":       "session",
		"component":    "pty-driver",
		"session":      session.ID,
		"session-name": session.Name,
		"instance":     instanceID,
	}

	validate := validator.New()
	if err := models.RegisterWithValidator(validate); err != nil {
		return nil, goutils.NewRuntimeError("failed to install custom validation macros", err, true)
	}

	driverMeta, err := session.ParseDriverMetadata(validate)
	if err != nil {
		return nil, goutils.NewValidationError(
			"failed to parse PTY driver metadata for session "+session.Name, err, true,
		)
	}

	castMeta, ok := driverMeta.(models.SessionDriverPTYParams)
	if !ok {
		return nil, goutils.NewConsistencyError("metadata for PTY driver is wrong type", nil, true)
	}

	instance := &ptyDriver{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		workingCtx: workingCtx,
		coreCmd:    nil,
		ptyHandle:  nil,
		session:    session,
		validator:  validate,
		metadata:   castMeta,
	}

	return instance, nil
}

// Setup the driver
func (d *ptyDriver) Setup() error {
	cmdDisplayStr := ""
	{
		t := []string{d.session.Command.Command}
		t = append(t, d.session.Command.Arguments...)
		cmdDisplayStr = strings.Join(t, " ")
	}

	// Start the PTY
	coreCmd := exec.Command(d.session.Command.Command, d.session.Command.Arguments...)
	ptyFile, err := pty.Start(coreCmd)
	if err != nil {
		return models.NewPTYError(fmt.Sprintf("failed to start PTY for '%s'", cmdDisplayStr), err, true)
	}

	{
		logTags := d.GetLogTagsForContext(d.workingCtx)
		log.
			WithFields(goutils.UpdateCodePositionInTags(logTags)).
			Infof("Started PTY for %s", cmdDisplayStr)
	}

	// Setup the session screen size
	err = pty.Setsize(
		ptyFile, &pty.Winsize{Rows: d.metadata.DisplayRows, Cols: d.metadata.DisplayCols},
	)
	if err != nil {
		_ = ptyFile.Close()
		_ = coreCmd.Process.Kill()
		_ = coreCmd.Wait() // reap
		return models.NewPTYError("failed to set PTY screen size", err, true)
	}

	d.coreCmd = coreCmd
	d.ptyHandle = ptyFile

	return nil
}

// PipeInput pipe user input into the process
func (d *ptyDriver) PipeInput(input io.Reader) error {
	_, err := io.Copy(d.ptyHandle, input)
	if err != nil {
		return models.NewPTYError("PTY INPUT pipe failure", err, true)
	}
	return nil
}

// PipeOutput pipe process output into buffer
func (d *ptyDriver) PipeOutput(output io.Writer) error {
	_, err := io.Copy(output, d.ptyHandle)
	if err != nil {
		return models.NewPTYError("PTY OUTPUT pipe failure", err, true)
	}
	return nil
}

// Wait for the process to stop
func (d *ptyDriver) Wait() error {
	err := d.coreCmd.Wait()
	// The child has been reaped; its PID is now free to be recycled, so signal `TearDown` to
	// stop using it as a kill target.
	d.reaped.Store(true)
	if err != nil {
		return models.NewPTYError("PTY completion wait failure", err, true)
	}
	return err
}

// TearDown the driver
func (d *ptyDriver) TearDown() error {
	// Stop the main command being executed. As `pty.Start` launches the child with `Setsid`,
	// the child is a process-group leader whose PGID equals its PID. Signalling the negative
	// PID delivers SIGKILL to the entire process group, so foreground children spawned by the
	// command are torn down too, not just the direct child. ESRCH means the group is already
	// gone (e.g. the command exited on its own and was reaped by the wait daemon).
	//
	// Skip the kill entirely once the child has been reaped: at that point the PID is freed and
	// could have been recycled to an unrelated process, so signalling the (negative) PID risks
	// killing a stranger's process group. A reaped leader is already gone, and closing the PTY
	// master below HUPs any remaining foreground group members.
	if d.coreCmd != nil && !d.reaped.Load() {
		err := syscall.Kill(-d.coreCmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return models.NewPTYError(
				"session "+d.session.Name+" core command kill failed", err, true,
			)
		}
	}

	// Stop the PTY
	if d.ptyHandle != nil {
		if err := d.ptyHandle.Close(); err != nil {
			return models.NewPTYError(
				"session "+d.session.Name+" PTY failed on shutdown", err, true,
			)
		}
	}

	return nil
}
