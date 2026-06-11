package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// SessionCommand the command being executed by the session
type SessionCommand struct {
	// Command the command being ran
	Command string `json:"cmd" validate:"required"`
	// Arguments the arguments passed to the command
	Arguments []string `json:"args"`
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

	// SessionStateStarting session is starting up
	SessionStateStarting SessionStateENUMType = "STARTING"

	// SessionStateReady session is running and ready to be claimed
	SessionStateReady SessionStateENUMType = "READY"

	// SessionStateClaimed session has been claimed
	SessionStateClaimed SessionStateENUMType = "CLAIMED"

	// SessionStateStopping session is stopping
	SessionStateStopping SessionStateENUMType = "STOPPING"
)

// Session one PTY session running a command with arguments
type Session struct {
	// ID PTY session ID
	ID string `json:"id" gorm:"column:id;primaryKey;unique" validate:"required"`

	// Name for the session, can only contain alphanumeric characters and -
	Name string `json:"name" gorm:"column:name;not null;unique" validate:"required,session_name_type"`

	// Description an optional description for the session
	Description *string `json:"description" gorm:"column:description;default:null"`

	// Command being executed by the session
	Command SessionCommand `json:"command" gorm:"column:command;not null" validate:"required"`

	// State of the session
	State SessionStateENUMType `json:"state" gorm:"column:state;not null" validate:"required,session_state_type"`

	// OutputBufferCapacity buffering capacity for holding command output history
	OutputBufferCapacity int64 `json:"io_buf_cap" gorm:"column:io_buf_cap;not null" validate:"required,gte=16384"`

	// CreatedAt entry creation timestamp
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt entry update timestamp
	UpdatedAt time.Time `json:"updated_at"`
}

// ValidNextState verify the session can transition to new state
func (s Session) ValidNextState(newState SessionStateENUMType) error {
	statesWithTransitions := map[SessionStateENUMType]map[SessionStateENUMType]bool{
		SessionStateIdle: {
			SessionStateIdle:     true,
			SessionStateStarting: true,
		},
		SessionStateStarting: {
			SessionStateIdle:     true,
			SessionStateStarting: true,
			SessionStateReady:    true,
		},
		SessionStateReady: {
			SessionStateIdle:     true,
			SessionStateReady:    true,
			SessionStateClaimed:  true,
			SessionStateStopping: true,
		},
		SessionStateClaimed: {
			SessionStateIdle:     true,
			SessionStateClaimed:  true,
			SessionStateReady:    true,
			SessionStateStopping: true,
		},
		SessionStateStopping: {
			SessionStateIdle:     true,
			SessionStateStopping: true,
		},
	}

	availableNextStates, ok := statesWithTransitions[s.State]
	if !ok {
		return fmt.Errorf("session can't transition out of state '%s'", s.State)
	}

	if _, ok := availableNextStates[newState]; !ok {
		return fmt.Errorf("session can't transition from '%s' to '%s'", s.State, newState)
	}

	return nil
}
