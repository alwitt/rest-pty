package session

import (
	"context"
	"fmt"
	"io"

	"github.com/alwitt/goutils"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
)

// dockerDriver docker-container based session driver which implements `coreDriver`.
//
// The session command runs as the container's entrypoint inside a sandboxed container that is
// hardened by default (read-only rootfs, all capabilities dropped, no-new-privileges) and
// isolated from the network. The container is run with a TTY, so its STDOUT/STDERR arrive as a
// single raw stream (no stdcopy de-multiplexing needed), mirroring the PTY driver. User input
// and process output are carried over the hijacked attach connection.
type dockerDriver struct {
	goutils.Component
	workingCtx context.Context
	session    models.Session
	// validator  *validator.Validate
	metadata models.SessionDriverDockerParams

	core goutilsRuntime.SystemCallRuntime
}

// newDockerCoreDriver define new docker-container core session driver
func newDockerCoreDriver(
	workingCtx context.Context, instanceID string, session models.Session,
) (coreDriver, error) {
	logTags := log.Fields{
		"module":       "session",
		"component":    "docker-driver",
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
			"failed to parse docker driver metadata for session "+session.Name, err, true,
		)
	}

	castMeta, ok := driverMeta.(models.SessionDriverDockerParams)
	if !ok {
		return nil, goutils.NewConsistencyError("metadata for docker driver is wrong type", nil, true)
	}

	instance := &dockerDriver{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		workingCtx: workingCtx,
		session:    session,
		metadata:   castMeta,
	}

	core, err := goutilsRuntime.NewDockerSystemCallRuntime(
		workingCtx, instance.containerName(), castMeta.DockerRuntimeParams, false,
	)
	if err != nil {
		return nil, goutils.NewRuntimeError("failed to define core runtime operator", err, true)
	}
	instance.core = core

	return instance, nil
}

// Setup the driver
func (d *dockerDriver) Setup() error {
	return d.core.Start(d.workingCtx)
}

// PipeInput pipe user input into the container's STDIN.
func (d *dockerDriver) PipeInput(input io.Reader) error {
	return d.core.PipeInput(input)
}

// PipeOutput pipe the container's STDOUT/STDERR into the buffer.
func (d *dockerDriver) PipeOutput(output io.Writer) error {
	return d.core.PipeOutput(output)
}

// Wait for the container process to stop
func (d *dockerDriver) Wait() error {
	_, err := d.core.Wait(d.workingCtx)
	return err
}

// TearDown the driver
func (d *dockerDriver) TearDown() error {
	return d.core.Cleanup(d.workingCtx)
}

// containerName build a docker-safe container name for this session run.
func (d *dockerDriver) containerName() string {
	return fmt.Sprintf("rest-pty.%s", d.session.Name)
}
