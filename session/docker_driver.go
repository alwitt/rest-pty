package session

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	dockerUnits "github.com/docker/go-units"
	"github.com/go-playground/validator/v10"
	dockerContainer "github.com/moby/moby/api/types/container"
	dockerNetwork "github.com/moby/moby/api/types/network"
	dockerClient "github.com/moby/moby/client"
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
	validator  *validator.Validate
	metadata   models.SessionDriverDockerParams

	instanceID  string
	cli         *dockerClient.Client
	containerID string
	attach      dockerClient.HijackedResponse

	// tornDown guards TearDown so the container is stopped/removed at most once. After teardown
	// the container ID is gone and must not be acted on again.
	tornDown atomic.Bool
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
		workingCtx:  workingCtx,
		session:     session,
		validator:   validate,
		metadata:    castMeta,
		instanceID:  instanceID,
		cli:         nil,
		containerID: "",
	}

	return instance, nil
}

// Setup the driver: create the docker client, build the container, attach to its streams, and
// start it. Attaching BEFORE starting avoids racing against (and losing) early output produced
// before the attach lands.
func (d *dockerDriver) Setup() error {
	logTags := d.GetLogTagsForContext(d.workingCtx)

	cmdDisplayStr := ""
	{
		t := []string{d.session.Command.Command}
		t = append(t, d.session.Command.Arguments...)
		cmdDisplayStr = strings.Join(t, " ")
	}

	// ------------------------------------------------------------------------------------
	// Docker client

	cli, err := dockerClient.New(dockerClient.FromEnv)
	if err != nil {
		return models.NewDockerError("failed to build docker client", err, true)
	}
	d.cli = cli

	// ------------------------------------------------------------------------------------
	// Build container configuration

	exposedPorts, portBindings, err := d.buildPortMappings()
	if err != nil {
		return err
	}
	containerCfg := d.buildContainerConfig(exposedPorts)
	hostCfg, err := d.buildHostConfig(portBindings)
	if err != nil {
		return err
	}

	// ------------------------------------------------------------------------------------
	// Create the container

	created, err := d.cli.ContainerCreate(d.workingCtx, dockerClient.ContainerCreateOptions{
		Config:     containerCfg,
		HostConfig: hostCfg,
		Name:       d.containerName(),
	})
	if err != nil {
		return models.NewDockerError(
			fmt.Sprintf("failed to create container for '%s'", cmdDisplayStr), err, true,
		)
	}
	d.containerID = created.ID
	for _, warning := range created.Warnings {
		log.WithFields(logTags).Warnf("container create warning: %s", warning)
	}

	// ------------------------------------------------------------------------------------
	// Attach to the container streams (before start)

	attach, err := d.cli.ContainerAttach(
		d.workingCtx, d.containerID, dockerClient.ContainerAttachOptions{
			Stream: true,
			Stdin:  true,
			Stdout: true,
			Stderr: true,
		},
	)
	if err != nil {
		return models.NewDockerError("failed to attach to container", err, true)
	}
	d.attach = attach.HijackedResponse

	// ------------------------------------------------------------------------------------
	// Start the container

	if _, err := d.cli.ContainerStart(
		d.workingCtx, d.containerID, dockerClient.ContainerStartOptions{},
	); err != nil {
		return models.NewDockerError(
			fmt.Sprintf("failed to start container for '%s'", cmdDisplayStr), err, true,
		)
	}

	log.WithFields(logTags).Infof("Started container %s for %s", d.containerID, cmdDisplayStr)

	// ------------------------------------------------------------------------------------
	// Size the TTY to the requested geometry

	if _, err := d.cli.ContainerResize(
		d.workingCtx, d.containerID, dockerClient.ContainerResizeOptions{
			Height: uint(d.metadata.DisplayRows),
			Width:  uint(d.metadata.DisplayCols),
		},
	); err != nil {
		return models.NewDockerError("failed to set container TTY size", err, true)
	}

	return nil
}

// PipeInput pipe user input into the container's STDIN over the hijacked connection
func (d *dockerDriver) PipeInput(input io.Reader) error {
	_, err := io.Copy(d.attach.Conn, input)
	// Signal EOF to the container's STDIN once the input stream is drained.
	_ = d.attach.CloseWrite()
	if err != nil {
		return models.NewDockerError("docker INPUT pipe failure", err, true)
	}
	return nil
}

// PipeOutput pipe the container's STDOUT/STDERR into the buffer. As the container runs with a
// TTY, the attached reader is a single raw stream and needs no stdcopy de-multiplexing.
func (d *dockerDriver) PipeOutput(output io.Writer) error {
	_, err := io.Copy(output, d.attach.Reader)
	if err != nil {
		return models.NewDockerError("docker OUTPUT pipe failure", err, true)
	}
	return nil
}

// Wait for the container process to stop
func (d *dockerDriver) Wait() error {
	waitResult := d.cli.ContainerWait(
		d.workingCtx, d.containerID, dockerClient.ContainerWaitOptions{
			Condition: dockerContainer.WaitConditionNotRunning,
		},
	)

	select {
	case <-d.workingCtx.Done():
		return nil
	case err := <-waitResult.Error:
		if err != nil {
			return models.NewDockerError("container completion wait failure", err, true)
		}
		return nil
	case res := <-waitResult.Result:
		if res.Error != nil {
			return models.NewDockerError(
				fmt.Sprintf("container wait reported error: %s", res.Error.Message), nil, true,
			)
		}
		if res.StatusCode != 0 {
			return models.NewDockerError(
				fmt.Sprintf("container exited with non-zero status %d", res.StatusCode), nil, true,
			)
		}
		return nil
	}
}

// TearDown the driver: close the attach connection, stop the container, and (optionally) remove
// it. Closing the connection unblocks any in-flight PipeInput/PipeOutput copies.
func (d *dockerDriver) TearDown() error {
	if !d.tornDown.CompareAndSwap(false, true) {
		return nil
	}

	logTags := d.GetLogTagsForContext(d.workingCtx)

	// Close the hijacked connection so the input/output transfer loops unwind.
	d.attach.Close()

	if d.cli == nil || d.containerID == "" {
		return nil
	}
	defer func() {
		_ = d.cli.Close()
	}()

	// Request the container stop gracefully with the configured signal before docker forcibly
	// kills it. Teardown runs on a context that may already be cancelled, so use a fresh context
	// bounded by the stop timeout.
	stopCtx := context.WithoutCancel(d.workingCtx)
	stopSignal := string(d.metadata.ResolvedStopSignal())
	if _, err := d.cli.ContainerStop(stopCtx, d.containerID, dockerClient.ContainerStopOptions{
		Signal: stopSignal,
	}); err != nil {
		// A container that already exited on its own is not an error worth failing teardown over.
		log.WithError(err).WithFields(logTags).Warn("container stop returned an error")
	}

	if d.metadata.IsRemoveOnExit() {
		if _, err := d.cli.ContainerRemove(stopCtx, d.containerID, dockerClient.ContainerRemoveOptions{
			Force: true,
		}); err != nil {
			return models.NewDockerError(
				"session "+d.session.Name+" container removal failed", err, true,
			)
		}
	}

	return nil
}

// containerName build a docker-safe container name for this session run. The per-run instance
// ID keeps the name unique across restarts, so a container leaked by a crash (or left behind by
// remove_on_exit=false) never collides with a fresh run. The "rest-pty." prefix allows orphaned
// containers to be found and reaped by name filter.
func (d *dockerDriver) containerName() string {
	return fmt.Sprintf("rest-pty.%s.%s", d.session.Name, d.instanceID)
}

// buildContainerConfig assemble the container.Config from the driver metadata and session command
func (d *dockerDriver) buildContainerConfig(
	exposedPorts dockerNetwork.PortSet,
) *dockerContainer.Config {
	user := ""
	runAsUser := d.metadata.RunAsUser
	if runAsUser == "" {
		runAsUser = models.DefaultContainerRunAsUser
	}
	runAsGroup := d.metadata.RunAsGroup
	if runAsGroup == "" {
		runAsGroup = models.DefaultContainerRunAsGroup
	}
	user = runAsUser + ":" + runAsGroup

	workingDir := d.metadata.WorkingDir
	if workingDir == "" {
		workingDir = models.DefaultContainerWorkingDir
	}

	env := make([]string, 0, len(d.metadata.Environment))
	for _, entry := range d.metadata.Environment {
		env = append(env, entry.Name+"="+entry.Value)
	}

	cfg := &dockerContainer.Config{
		Image:        d.metadata.Image,
		Entrypoint:   []string{d.session.Command.Command},
		Cmd:          d.session.Command.Arguments,
		Env:          env,
		User:         user,
		WorkingDir:   workingDir,
		ExposedPorts: exposedPorts,
		StopSignal:   string(d.metadata.ResolvedStopSignal()),
		Tty:          true,
		OpenStdin:    true,
		StdinOnce:    false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}
	return cfg
}

// buildHostConfig assemble the container.HostConfig from the driver metadata, applying the
// sandbox hardening defaults
func (d *dockerDriver) buildHostConfig(
	portBindings dockerNetwork.PortMap,
) (*dockerContainer.HostConfig, error) {
	memReservation := d.metadata.MemReservation
	if memReservation == "" {
		memReservation = models.DefaultContainerMemReservation
	}
	memReservationBytes, err := dockerUnits.RAMInBytes(memReservation)
	if err != nil {
		return nil, models.NewDockerError(
			fmt.Sprintf("invalid memory reservation '%s'", memReservation), err, true,
		)
	}
	memLimit := d.metadata.MemLimit
	if memLimit == "" {
		memLimit = models.DefaultContainerMemLimit
	}
	memLimitBytes, err := dockerUnits.RAMInBytes(memLimit)
	if err != nil {
		return nil, models.NewDockerError(
			fmt.Sprintf("invalid memory limit '%s'", memLimit), err, true,
		)
	}

	networkMode := d.metadata.NetworkMode
	if networkMode == "" {
		networkMode = models.DefaultContainerNetworkMode
	}

	// Writable tmpfs mounts for the otherwise read-only rootfs.
	var tmpfs map[string]string
	if len(d.metadata.WritableDirs) > 0 {
		tmpfs = make(map[string]string, len(d.metadata.WritableDirs))
		for _, dir := range d.metadata.WritableDirs {
			tmpfs[dir.Path] = fmt.Sprintf("size=%d", dir.Size())
		}
	}

	// Host path bind mounts.
	var binds []string
	for _, mount := range d.metadata.HostMounts {
		bind := mount.Path + ":" + mount.GetMountPath()
		if mount.IsReadOnly() {
			bind += ":ro"
		}
		binds = append(binds, bind)
	}

	// Extra host-to-IP mappings in docker's "host:ip" form.
	var extraHosts []string
	for _, host := range d.metadata.ExtraHosts {
		extraHosts = append(extraHosts, host.Host+":"+host.Address)
	}

	var capDrop []string
	if d.metadata.IsDropAllCapabilities() {
		capDrop = []string{"ALL"}
	}
	var securityOpt []string
	if d.metadata.IsNoNewPrivileges() {
		securityOpt = []string{"no-new-privileges=true"}
	}

	hostCfg := &dockerContainer.HostConfig{
		NetworkMode:    dockerContainer.NetworkMode(networkMode),
		PortBindings:   portBindings,
		Binds:          binds,
		Tmpfs:          tmpfs,
		ExtraHosts:     extraHosts,
		CapDrop:        capDrop,
		CapAdd:         d.metadata.AddCapabilities,
		SecurityOpt:    securityOpt,
		ReadonlyRootfs: d.metadata.IsReadOnlyRootFS(),
		// The container lifecycle is managed explicitly via TearDown; AutoRemove would race the
		// ContainerWait/inspect performed by the wait daemon.
		AutoRemove: false,
		Resources: dockerContainer.Resources{
			Memory:            memLimitBytes,
			MemoryReservation: memReservationBytes,
		},
	}
	return hostCfg, nil
}

// buildPortMappings translate the metadata PublishPorts into the docker exposed-port set and
// host port bindings used to accept inbound connections
func (d *dockerDriver) buildPortMappings() (dockerNetwork.PortSet, dockerNetwork.PortMap, error) {
	if len(d.metadata.PublishPorts) == 0 {
		return nil, nil, nil
	}

	exposed := make(dockerNetwork.PortSet, len(d.metadata.PublishPorts))
	bindings := make(dockerNetwork.PortMap, len(d.metadata.PublishPorts))

	for _, publish := range d.metadata.PublishPorts {
		port, ok := dockerNetwork.PortFrom(
			publish.ContainerPort, dockerNetwork.IPProtocol(publish.ResolvedProtocol()),
		)
		if !ok {
			return nil, nil, models.NewDockerError(
				fmt.Sprintf(
					"invalid published port %d/%s",
					publish.ContainerPort, publish.ResolvedProtocol(),
				), nil, true,
			)
		}

		hostIP, err := netip.ParseAddr(publish.ResolvedHostIP())
		if err != nil {
			return nil, nil, models.NewDockerError(
				fmt.Sprintf("invalid published host IP '%s'", publish.ResolvedHostIP()), err, true,
			)
		}

		hostPort := ""
		if publish.HostPort != 0 {
			hostPort = strconv.Itoa(int(publish.HostPort))
		}

		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], dockerNetwork.PortBinding{
			HostIP:   hostIP,
			HostPort: hostPort,
		})
	}

	return exposed, bindings, nil
}
