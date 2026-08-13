package session

import (
	"context"
	"fmt"
	"io"

	"github.com/alwitt/goutils"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/workspace"
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
//
// When the session carries a workspace, this resolves it against cairn and mounts the
// workspace's persistent volume - so the mount is re-resolved on every session start, which is
// what lets a workspace assigned while the session was IDLE take effect on the next run.
func newDockerCoreDriver(
	workingCtx context.Context,
	instanceID string,
	session models.Session,
	cairnClient workspace.CairnClient,
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

	// Mount the session's workspace, if it has one. Assigned back onto castMeta so it reaches
	// both the stored metadata and the core runtime built below.
	if session.WorkspaceName != nil {
		castMeta, err = resolveWorkspaceMount(
			workingCtx, cairnClient, session.Name, *session.WorkspaceName, castMeta,
		)
		if err != nil {
			return nil, err
		}
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
		workingCtx, instance.containerName(), goutilsRuntime.ContainerCommand{
			Entrypoint: []string{session.Command.Command}, Commands: session.Command.Arguments,
		}, goutilsRuntime.DockerRuntimeParams(castMeta), false,
	)
	if err != nil {
		return nil, goutils.NewRuntimeError("failed to define core runtime operator", err, true)
	}
	instance.core = core

	return instance, nil
}

/*
resolveWorkspaceMount resolve a session's cairn workspace and return a copy of the docker driver
params carrying the workspace's persistent volume mount.

rest-pty is a mount-only cairn client: cairn owns the workspace record, the volume backing it,
and that volume's name. This reads the name cairn persisted and mounts the volume cairn names,
at the mount path cairn fixed - it derives none of the three.

Every failure aborts the session start. A session that asked to run in a workspace and instead
got a container with nothing mounted would write into a scratch layer discarded on exit, with
no indication anything went wrong.

	@param ctx context.Context - execution context
	@param cairn workspace.CairnClient - the cairn client; nil when the deployment did not
	    configure the integration
	@param sessionName string - the session being started, for the error messages
	@param workspaceName string - the workspace the session was assigned
	@param params models.SessionDriverDockerParams - the parsed driver params (not mutated)
	@returns a copy of the driver params carrying the workspace volume mount
	@returns `goutils.RuntimeError` cairn is not configured, the lookup failed, or the
	    workspace has no mountable volume
*/
func resolveWorkspaceMount(
	ctx context.Context,
	cairn workspace.CairnClient,
	sessionName, workspaceName string,
	params models.SessionDriverDockerParams,
) (models.SessionDriverDockerParams, error) {
	if cairn == nil {
		return params, goutils.NewRuntimeError(
			"session '"+sessionName+"' is assigned workspace '"+workspaceName+"', but no cairn "+
				"service is configured on this deployment",
			nil,
			true,
		)
	}

	found, err := cairn.FetchWorkspace(ctx, workspaceName)
	if err != nil {
		return params, goutils.NewRuntimeError(
			"failed to resolve workspace '"+workspaceName+"' for session '"+sessionName+"'",
			err,
			true,
		)
	}

	// Only a READY volume can be mounted. NONE means no volume has been provisioned - which an
	// operator must do; rest-pty can not.
	if !found.IsVolumeReady() {
		return params, goutils.NewRuntimeError(
			"workspace '"+workspaceName+"' has no mountable persistent volume "+
				"(volume state '"+string(found.VolumeState)+"')",
			nil,
			true,
		)
	}

	return withVolumeMount(params, found.VolumeName, workspace.MountPath), nil
}

/*
withVolumeMount return a copy of the docker driver params with a named persistent volume
mounted.

Built on a fresh slice rather than appended into the caller's backing array. The params reach
here from a per-construction parse of the session's driver metadata, so nothing is shared
today - but the discipline is what keeps it that way if they ever are.

The mount is read-write (ReadOnly left unset): a workspace volume exists to be written to.

	@param params models.SessionDriverDockerParams - the driver params (taken by value; not mutated)
	@param volumeName string - the persistent volume to mount
	@param mountPath string - the absolute path within the container to mount it at
	@returns a copy of the driver params carrying the additional mount
*/
func withVolumeMount(
	params models.SessionDriverDockerParams, volumeName, mountPath string,
) models.SessionDriverDockerParams {
	merged := params
	configMounts := params.VolumeMounts
	mounts := make([]goutilsRuntime.ContainerVolumeMount, 0, len(configMounts)+1)
	mounts = append(mounts, configMounts...)
	mounts = append(mounts, goutilsRuntime.ContainerVolumeMount{
		Name: volumeName, MountPath: mountPath,
	})
	merged.VolumeMounts = mounts

	return merged
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
