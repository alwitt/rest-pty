package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-playground/validator/v10"
)

// IPCMessageEnvelope type erased IPC message object
type IPCMessageEnvelope interface {
	// StringPayload return its payload as a string
	StringPayload() (string, error)
}

// IPCMessageTypeEnumType IPC message type ENUM
type IPCMessageTypeEnumType string

const (
	/*
		Request - Session Runner
	*/

	// IPCMsgTypeReqRunCommands user request run commands
	IPCMsgTypeReqRunCommands IPCMessageTypeEnumType = "IPC_REQ_RUN_COMMANDS"

	/*
		Response - Session Runner
	*/

	// IPCMsgTypeRespRunCommands response to run user commands
	IPCMsgTypeRespRunCommands IPCMessageTypeEnumType = "IPC_RESP_RUN_COMMANDS"

/*
Request - Session Manager
*/

/*
Response - Session Manager
*/
)

// ParseIPCMessage parse the IPC message based on type
func ParseIPCMessage(validator *validator.Validate, msg []byte) (interface{}, error) {
	var asBaseMsg BaseIPCMessage
	if err := json.Unmarshal(msg, &asBaseMsg); err != nil {
		return nil, BadInputError{Core: err, Message: "failed to parse IPC message as base message"}
	}
	if err := validator.Struct(&asBaseMsg); err != nil {
		return nil, ValidationError{Core: err, Message: "base IPC message invalid"}
	}
	switch asBaseMsg.Type {
	/****************************************************************************************
	Request - Session Runner
	****************************************************************************************/

	case IPCMsgTypeReqRunCommands:
		var parsed IPCMessageReqRunCommands
		if err := json.Unmarshal(msg, &parsed); err != nil {
			return nil, fmt.Errorf("IPC message '%s' parse failed [%w]", asBaseMsg.Type, err)
		}
		return parsed, validator.Struct(&parsed)

	/****************************************************************************************
	Response - Session Runner
	****************************************************************************************/

	case IPCMsgTypeRespRunCommands:
		var parsed IPCMessageRespUniversal
		if err := json.Unmarshal(msg, &parsed); err != nil {
			return nil, fmt.Errorf("IPC message '%s' parse failed [%w]", asBaseMsg.Type, err)
		}
		return parsed, validator.Struct(&parsed)

	default:
		return nil, fmt.Errorf("unknown IPC message type %s", asBaseMsg.Type)
	}
}

// BaseIPCMessage base IPC message object
type BaseIPCMessage struct {
	// RequestID the ID of the request
	RequestID string `json:"req_id" validate:"required"`
	// Type message type
	Type IPCMessageTypeEnumType `json:"type" validate:"required,ipc_msg_type"`
	// Sender name of the sending entity
	Sender string `json:"sender" validate:"required"`
	// Timestamp message timestamp
	Timestamp time.Time `json:"timestamp"`
}

// StringPayload return its payload as a string
func (m BaseIPCMessage) StringPayload() (string, error) {
	t, err := json.Marshal(&m)
	return string(t), err
}

// IPCMessageRespUniversal universal response for requests which don't need special
// response structs.
type IPCMessageRespUniversal struct {
	BaseIPCMessage
	// Success whether the request is processed successfully
	Success bool `json:"success"`
	// ErrorMsg in case of failure, an accompanying error message
	ErrorMsg *string `json:"error,omitempty"`
}

/****************************************************************************************
Request - Session Runner
****************************************************************************************/

// IPCMessageReqRunCommands run user commands request
type IPCMessageReqRunCommands struct {
	BaseIPCMessage
	// Commands the list of commands to send to the session
	Commands []SessionInputCommand `json:"commands" validate:"required,gte=1,dive"`
}

/****************************************************************************************
Response - Session Runner
****************************************************************************************/
