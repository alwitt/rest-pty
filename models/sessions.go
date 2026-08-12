package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alwitt/goutils"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	"github.com/go-playground/validator/v10"
	"gorm.io/datatypes"
)

// SessionCommand the command being executed by the session
type SessionCommand struct {
	// Command the command being run
	Command string `json:"cmd" validate:"required" jsonschema:"the command being run"`
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

// SessionDriverDockerParams parameters for docker-container session drivers.
//
// The container is run interactively with a TTY (mirroring the PTY driver), hardened by
// default (read-only rootfs, all capabilities dropped, no-new-privileges) and isolated from
// the network. Sessions that need to accept inbound connections must select a routable
// NetworkMode and declare PublishPorts.
type SessionDriverDockerParams goutilsRuntime.DockerRuntimeParams

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
