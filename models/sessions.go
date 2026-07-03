package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alwitt/goutils"
	"github.com/go-playground/validator/v10"
	"gorm.io/datatypes"
)

// SessionCommand the command being executed by the session
type SessionCommand struct {
	// Command the command being ran
	Command string `json:"cmd" validate:"required" jsonschema:"the command being ran"`
	// Arguments the arguments passed to the command
	Arguments []string `json:"args" jsonschema:"the arguments passed to the command"`
}

// Scan scan value into Jsonb, implements sql.Scanner interface
func (c *SessionCommand) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("core value is not byte slice")
	}

	var parsed SessionCommand
	err := json.Unmarshal(bytes, &parsed)
	*c = parsed
	return err
}

// Value return json value, implement driver.Valuer interface
func (c SessionCommand) Value() (driver.Value, error) {
	return json.Marshal(&c)
}

// SessionStateENUMType session state ENUM value type
type SessionStateENUMType string

const (
	// SessionStateIdle session is idle
	SessionStateIdle SessionStateENUMType = "IDLE"

	// SessionStateReady session is running and ready to accept input
	SessionStateReady SessionStateENUMType = "READY"
)

// Values all valid SessionStateENUMType values
func (SessionStateENUMType) Values() []SessionStateENUMType {
	return []SessionStateENUMType{SessionStateIdle, SessionStateReady}
}

// SessionDriverTypeENUMType session driver type ENUM type
type SessionDriverTypeENUMType string

const (
	// SessionDriverTypePTY using PTY as session driver
	SessionDriverTypePTY SessionDriverTypeENUMType = "PTY"

	// SessionDriverTypeDocker using a docker container as session driver
	SessionDriverTypeDocker SessionDriverTypeENUMType = "DOCKER"
)

// Values all valid SessionDriverTypeENUMType values
func (SessionDriverTypeENUMType) Values() []SessionDriverTypeENUMType {
	return []SessionDriverTypeENUMType{SessionDriverTypePTY, SessionDriverTypeDocker}
}

// SessionRunnerModeTypeENUMType session runner operating mode type ENUM
type SessionRunnerModeTypeENUMType string

const (
	// SessionRunnerModeTypeCommanded session runner operates by taking commands from user
	// and feeding it to the session under control.
	SessionRunnerModeTypeCommanded SessionRunnerModeTypeENUMType = "COMMANDED"

	// SessionRunnerModeTypeByPassed session runner will ignore commands from user
	// and allow user to directly interact with the session under control
	SessionRunnerModeTypeByPassed SessionRunnerModeTypeENUMType = "BY_PASSED"
)

// Values all valid SessionRunnerModeTypeENUMType values
func (SessionRunnerModeTypeENUMType) Values() []SessionRunnerModeTypeENUMType {
	return []SessionRunnerModeTypeENUMType{
		SessionRunnerModeTypeCommanded, SessionRunnerModeTypeByPassed,
	}
}

// Session one PTY session running a command with arguments
type Session struct {
	// ID PTY session ID
	ID string `json:"id" gorm:"column:id;primaryKey;unique" validate:"required"`

	// Name for the session, can only contain alphanumeric characters and -
	Name string `json:"name" gorm:"column:name;not null;unique" validate:"required,session_name_type" jsonschema:"name for the session, can only contain alphanumeric characters and -"`

	// Description an optional description for the session
	Description *string `json:"description" gorm:"column:description;default:null" jsonschema:"an optional description for the session"`

	// Command being executed by the session
	Command SessionCommand `json:"command" gorm:"column:command;not null" validate:"required" jsonschema:"command being executed by the session"`

	// State of the session
	State SessionStateENUMType `json:"state" gorm:"column:state;not null" validate:"required,session_state_type" jsonschema:"state of the session [IDLE, READY]"`

	// DriverType indicate which driver the session uses
	DriverType SessionDriverTypeENUMType `json:"driver" gorm:"column:driver;not null" validate:"required,session_driver_type" jsonschema:"indicate which driver the session uses"`

	// DriverMetadata metadata relating to the session driver
	DriverMetadata datatypes.JSON `json:"driver_metadata,omitempty" gorm:"column:driver_metadata;default:null" swaggertype:"string"`

	// OutputBufferCapacity buffering capacity for holding command output history
	OutputBufferCapacity int64 `json:"io_buf_cap" gorm:"column:io_buf_cap;not null" validate:"required,gte=16384" jsonschema:"buffering capacity for holding command output history"`

	// SessionRunnerModeTypeENUMType session runner operating mode
	RunnerMode SessionRunnerModeTypeENUMType `json:"runner_mode" gorm:"column:runner_mode;not null" validate:"required,session_runner_mode_type" jsonschema:"session runner operating mode"`

	// CreatedAt entry creation timestamp
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt entry update timestamp
	UpdatedAt time.Time `json:"updated_at"`
}

// ParseDriverMetadata parse driver metadata based on driver type
func (s Session) ParseDriverMetadata(validator *validator.Validate) (interface{}, error) {
	switch s.DriverType {
	case SessionDriverTypePTY:
		var parsed SessionDriverPTYParams
		if err := json.Unmarshal(s.DriverMetadata, &parsed); err != nil {
			return nil, fmt.Errorf("session '%s' driver metadata parse failed [%w]", s.Name, err)
		}
		return parsed, validator.Struct(&parsed)

	case SessionDriverTypeDocker:
		var parsed SessionDriverDockerParams
		if err := json.Unmarshal(s.DriverMetadata, &parsed); err != nil {
			return nil, fmt.Errorf("session '%s' driver metadata parse failed [%w]", s.Name, err)
		}
		return parsed, validator.Struct(&parsed)

	default:
		return nil, fmt.Errorf("unsupported session driver type '%s'", s.DriverType)
	}
}

// SessionDriverPTYParams parameters for PTY session drivers
type SessionDriverPTYParams struct {
	// DisplayRows PTY number of rows (in cells).
	DisplayRows uint16 `json:"display_rows" validate:"gte=30" jsonschema:"PTY number of rows (in cells)"`

	// DisplayCol PTY number of columns (in cells).
	DisplayCols uint16 `json:"display_cols" validate:"gte=80" jsonschema:"PTY number of columns (in cells)"`
}

// ContainerStopSignalENUMType signal used to request the container process to stop
type ContainerStopSignalENUMType string

const (
	// ContainerStopSignalSIGINT stop the container process with SIGINT
	ContainerStopSignalSIGINT ContainerStopSignalENUMType = "SIGINT"
	// ContainerStopSignalSIGTERM stop the container process with SIGTERM
	ContainerStopSignalSIGTERM ContainerStopSignalENUMType = "SIGTERM"
	// ContainerStopSignalSIGQUIT stop the container process with SIGQUIT
	ContainerStopSignalSIGQUIT ContainerStopSignalENUMType = "SIGQUIT"
	// ContainerStopSignalSIGHUP stop the container process with SIGHUP
	ContainerStopSignalSIGHUP ContainerStopSignalENUMType = "SIGHUP"
	// ContainerStopSignalSIGKILL forcibly stop the container process with SIGKILL
	ContainerStopSignalSIGKILL ContainerStopSignalENUMType = "SIGKILL"
)

// Values all valid ContainerStopSignalENUMType values
func (ContainerStopSignalENUMType) Values() []ContainerStopSignalENUMType {
	return []ContainerStopSignalENUMType{
		ContainerStopSignalSIGINT,
		ContainerStopSignalSIGTERM,
		ContainerStopSignalSIGQUIT,
		ContainerStopSignalSIGHUP,
		ContainerStopSignalSIGKILL,
	}
}

// Default values applied for omitted docker driver parameters.
const (
	// DefaultContainerRunAsUser default container run-as user
	DefaultContainerRunAsUser = "nobody"
	// DefaultContainerRunAsGroup default container run-as group
	DefaultContainerRunAsGroup = "nogroup"
	// DefaultContainerWorkingDir default container working directory
	DefaultContainerWorkingDir = "/tmp"
	// DefaultContainerMemReservation default container memory reservation
	DefaultContainerMemReservation = "32m"
	// DefaultContainerMemLimit default container memory limit
	DefaultContainerMemLimit = "128m"
	// DefaultContainerTmpfsSize default writable tmpfs mount size in bytes (64 MiB)
	DefaultContainerTmpfsSize = int64(67108864)
	// DefaultContainerNetworkMode default container network mode (no networking)
	DefaultContainerNetworkMode = "none"
	// DefaultContainerStopSignal default signal used to stop the container process
	DefaultContainerStopSignal = ContainerStopSignalSIGINT
	// DefaultContainerPortProtocol default published port protocol
	DefaultContainerPortProtocol = "tcp"
	// DefaultContainerPublishHostIP default host interface a published port binds to
	DefaultContainerPublishHostIP = "127.0.0.1"
)

// ContainerHostMount a host path bind-mounted into the container
type ContainerHostMount struct {
	// Path host path to mount
	Path string `json:"path" validate:"required" jsonschema:"host path to mount"`
	// ReadOnly whether to mount the path read-only; defaults to true when nil
	ReadOnly *bool `json:"read_only,omitempty" jsonschema:"whether to mount the path read-only; defaults to true when nil"`
	// MountPath path within the container to mount the host path. The mount path will
	// mirror the host path if this is not specified.
	MountPath *string `json:"mount_path,omitempty" jsonschema:"path within the container to mount the host path. The mount path will mirror the host path if this is not specified."`
}

// IsReadOnly resolve ReadOnly, defaulting to true when unset
func (m ContainerHostMount) IsReadOnly() bool {
	if m.ReadOnly == nil {
		return true
	}
	return *m.ReadOnly
}

// GetMountPath get the in-container mount path
func (m ContainerHostMount) GetMountPath() string {
	if m.MountPath != nil {
		return *m.MountPath
	}
	return m.Path
}

// ContainerTmpfsMount a writable tmpfs mount within the (otherwise read-only) container
type ContainerTmpfsMount struct {
	// Path the directory within the container to back with a writable tmpfs
	Path string `json:"path" validate:"required" jsonschema:"the directory within the container to back with a writable tmpfs"`
	// TmpfsSize size of the tmpfs mount in bytes; defaults to DefaultContainerTmpfsSize when 0
	TmpfsSize int64 `json:"tmpfs_size,omitempty" validate:"omitempty,gt=0" jsonschema:"size of the tmpfs mount in bytes; defaults to 67108864 (64 MiB) when 0"`
}

// Size resolve TmpfsSize, defaulting when unset
func (m ContainerTmpfsMount) Size() int64 {
	if m.TmpfsSize <= 0 {
		return DefaultContainerTmpfsSize
	}
	return m.TmpfsSize
}

// ContainerPortPublish publishes a container port to a host interface so the session
// command can accept inbound connections
type ContainerPortPublish struct {
	// ContainerPort the port the session command listens on inside the container
	ContainerPort uint16 `json:"container_port" validate:"required" jsonschema:"the port the session command listens on inside the container"`
	// Protocol the port protocol; defaults to DefaultContainerPortProtocol when empty
	Protocol string `json:"protocol,omitempty" validate:"omitempty,oneof=tcp udp" jsonschema:"the port protocol; defaults to tcp when empty"`
	// HostPort the host port to bind; 0 requests an ephemeral host port
	HostPort uint16 `json:"host_port,omitempty" jsonschema:"the host port to bind; 0 requests an ephemeral host port"`
	// HostIP the host interface to bind to; defaults to DefaultContainerPublishHostIP when empty
	HostIP string `json:"host_ip,omitempty" validate:"omitempty,ip" jsonschema:"the host interface to bind to; defaults to 127.0.0.1 when empty"`
}

// ResolvedProtocol resolve Protocol, defaulting when empty
func (p ContainerPortPublish) ResolvedProtocol() string {
	if p.Protocol == "" {
		return DefaultContainerPortProtocol
	}
	return p.Protocol
}

// ResolvedHostIP resolve HostIP, defaulting when empty
func (p ContainerPortPublish) ResolvedHostIP() string {
	if p.HostIP == "" {
		return DefaultContainerPublishHostIP
	}
	return p.HostIP
}

// ContainerExtraHost an extra host-to-IP mapping injected into the container's /etc/hosts
type ContainerExtraHost struct {
	// Host the hostname to map
	Host string `json:"host" validate:"required" jsonschema:"the hostname to map"`
	// Address the IP address the hostname resolves to
	Address string `json:"address" validate:"required,ip" jsonschema:"the IP address the hostname resolves to"`
}

// ContainerEnvVar an environment variable set on the container process
type ContainerEnvVar struct {
	// Name the environment variable name
	Name string `json:"name" validate:"required" jsonschema:"the environment variable name"`
	// Value the environment variable value
	Value string `json:"value" jsonschema:"the environment variable value"`
}

// SessionDriverDockerParams parameters for docker-container session drivers.
//
// The container is run interactively with a TTY (mirroring the PTY driver), hardened by
// default (read-only rootfs, all capabilities dropped, no-new-privileges) and isolated from
// the network. Sessions that need to accept inbound connections must select a routable
// NetworkMode and declare PublishPorts.
type SessionDriverDockerParams struct {
	// Image container image reference to run
	Image string `json:"image" validate:"required" jsonschema:"container image reference to run"`

	// DisplayRows TTY number of rows (in cells).
	DisplayRows uint16 `json:"display_rows" validate:"gte=30" jsonschema:"TTY number of rows (in cells)"`
	// DisplayCols TTY number of columns (in cells).
	DisplayCols uint16 `json:"display_cols" validate:"gte=80" jsonschema:"TTY number of columns (in cells)"`

	// MemReservation soft memory reservation (e.g. "32m"); defaults when empty
	MemReservation string `json:"mem_reservation,omitempty" jsonschema:"soft memory reservation (e.g. \"32m\"); defaults when empty"`
	// MemLimit hard memory limit (e.g. "128m"); defaults when empty
	MemLimit string `json:"mem_limit,omitempty" jsonschema:"hard memory limit (e.g. \"128m\"); defaults when empty"`

	// RunAsUser user to run the container process as; defaults to DefaultContainerRunAsUser
	RunAsUser string `json:"run_as_user,omitempty" jsonschema:"user to run the container process as; defaults to 'nobody'"`
	// RunAsGroup group to run the container process as; defaults to DefaultContainerRunAsGroup
	RunAsGroup string `json:"run_as_group,omitempty" jsonschema:"group to run the container process as; defaults to 'nogroup'"`
	// WorkingDir working directory for the container process; defaults to DefaultContainerWorkingDir
	WorkingDir string `json:"working_dir,omitempty" jsonschema:"working directory for the container process; defaults to '/tmp'"`

	// WritableDirs tmpfs mounts providing writable directories within the read-only rootfs
	WritableDirs []ContainerTmpfsMount `json:"writable_dirs,omitempty" validate:"omitempty,dive" jsonschema:"tmpfs mounts providing writable directories within the read-only rootfs"`
	// HostMounts host paths bind-mounted into the container
	HostMounts []ContainerHostMount `json:"host_mounts,omitempty" validate:"omitempty,dive" jsonschema:"host paths bind-mounted into the container"`

	// AddCapabilities Linux capabilities to add back on top of the dropped-by-default set
	// (e.g. NET_BIND_SERVICE to bind ports below 1024)
	AddCapabilities []string `json:"add_caps,omitempty" jsonschema:"Linux capabilities to add back on top of the dropped-by-default set (e.g. NET_BIND_SERVICE to bind ports below 1024)"`

	// NetworkMode the container network mode (e.g. "none", "bridge"); defaults to
	// DefaultContainerNetworkMode. Must be routable when PublishPorts is set.
	NetworkMode string `json:"network_mode,omitempty" jsonschema:"the container network mode (e.g. \"none\", \"bridge\"); defaults to 'none'. Must be routable when PublishPorts is set."`
	// PublishPorts container ports published to the host for inbound connections
	PublishPorts []ContainerPortPublish `json:"publish_ports,omitempty" validate:"omitempty,dive" jsonschema:"container ports published to the host for inbound connections"`
	// ExtraHosts additional host-to-IP mappings for the container
	ExtraHosts []ContainerExtraHost `json:"extra_hosts,omitempty" validate:"omitempty,dive" jsonschema:"additional host-to-IP mappings for the container"`
	// Environment additional environment variables for the container process
	Environment []ContainerEnvVar `json:"environment,omitempty" validate:"omitempty,dive" jsonschema:"additional environment variables for the container process"`

	// StopSignal signal sent to request the container process stop during teardown;
	// defaults to DefaultContainerStopSignal when empty
	StopSignal ContainerStopSignalENUMType `json:"stop_signal,omitempty" validate:"omitempty,container_stop_signal" jsonschema:"signal sent to request the container process stop during teardown; defaults to 'SIGINT' when empty"`

	// ReadOnlyRootFS mount the container root filesystem read-only; defaults to true when nil
	ReadOnlyRootFS *bool `json:"read_only_rootfs,omitempty" jsonschema:"mount the container root filesystem read-only; defaults to true when nil"`
	// DropAllCapabilities drop all Linux capabilities; defaults to true when nil
	DropAllCapabilities *bool `json:"drop_all_caps,omitempty" jsonschema:"drop all Linux capabilities; defaults to true when nil"`
	// NoNewPrivileges set the no-new-privileges security option; defaults to true when nil
	NoNewPrivileges *bool `json:"no_new_privileges,omitempty" jsonschema:"set the no-new-privileges security option; defaults to true when nil"`
	// RemoveOnExit remove the container on teardown; defaults to true when nil
	RemoveOnExit *bool `json:"remove_on_exit,omitempty" jsonschema:"remove the container on teardown; defaults to true when nil"`
}

// IsReadOnlyRootFS resolve ReadOnlyRootFS, defaulting to true when unset
func (p SessionDriverDockerParams) IsReadOnlyRootFS() bool {
	return boolOrTrue(p.ReadOnlyRootFS)
}

// IsDropAllCapabilities resolve DropAllCapabilities, defaulting to true when unset
func (p SessionDriverDockerParams) IsDropAllCapabilities() bool {
	return boolOrTrue(p.DropAllCapabilities)
}

// IsNoNewPrivileges resolve NoNewPrivileges, defaulting to true when unset
func (p SessionDriverDockerParams) IsNoNewPrivileges() bool {
	return boolOrTrue(p.NoNewPrivileges)
}

// IsRemoveOnExit resolve RemoveOnExit, defaulting to true when unset
func (p SessionDriverDockerParams) IsRemoveOnExit() bool {
	return boolOrTrue(p.RemoveOnExit)
}

// ResolvedStopSignal resolve StopSignal, defaulting when empty
func (p SessionDriverDockerParams) ResolvedStopSignal() ContainerStopSignalENUMType {
	if p.StopSignal == "" {
		return DefaultContainerStopSignal
	}
	return p.StopSignal
}

// boolOrTrue resolve an optional bool, defaulting to true when nil
func boolOrTrue(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

// ValidNextState verify the session can transition to new state
func (s Session) ValidNextState(newState SessionStateENUMType) error {
	statesWithTransitions := map[SessionStateENUMType]map[SessionStateENUMType]bool{
		SessionStateIdle: {
			SessionStateIdle:  true,
			SessionStateReady: true,
		},
		SessionStateReady: {
			SessionStateReady: true,
			SessionStateIdle:  true,
		},
	}

	availableNextStates, ok := statesWithTransitions[s.State]
	if !ok {
		return goutils.NewConsistencyError(
			fmt.Sprintf("session can't transition out of state '%s'", s.State), nil, true,
		)
	}

	if _, ok := availableNextStates[newState]; !ok {
		return goutils.NewConsistencyError(
			fmt.Sprintf("session can't transition from '%s' to '%s'", s.State, newState), nil, true,
		)
	}

	return nil
}
