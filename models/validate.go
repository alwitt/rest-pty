package models

import (
	"reflect"
	"regexp"

	"github.com/go-playground/validator/v10"
)

/*
RegisterWithValidator register with the validator this custom validation support

	@param v *validator.Validate - the validator to register against
	@return whether successful
*/
func RegisterWithValidator(v *validator.Validate) error {
	if err := v.RegisterValidation(
		"session_state_type", validateSessionStateType,
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"session_name_type", validateSessionNameType,
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"session_driver_type", validateSessionDriverType,
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"session_runner_mode_type", validateSessionRunnerModeType,
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"session_input_cmd_type", validateSessionInputCmdType,
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"ipc_msg_type", validateIPCMessageType,
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"container_stop_signal", validateContainerStopSignal,
	); err != nil {
		return err
	}

	v.RegisterStructValidation(validateSessionInputCommand, SessionInputCommand{})
	v.RegisterStructValidation(validateSessionDriverDockerParams, SessionDriverDockerParams{})

	return nil
}

// validateSessionInputCommand struct-level validation for SessionInputCommand,
// delegating the Type/Content relationship checks to (SessionInputCommand).IsValid.
//
// IsValid's message is passed as the reported tag so it surfaces in the default
// ValidationErrors output (which renders the tag, not the param).
func validateSessionInputCommand(sl validator.StructLevel) {
	cmd := sl.Current().Interface().(SessionInputCommand)
	if err := cmd.IsValid(); err != nil {
		sl.ReportError(cmd.Content, "Content", "Content", err.Error(), "")
	}
}

func validateSessionStateType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	switch SessionStateENUMType(fl.Field().String()) {
	case SessionStateIdle:
		fallthrough
	case SessionStateReady:
		return true
	}
	return false
}

func validateSessionRunnerModeType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	switch SessionRunnerModeTypeENUMType(fl.Field().String()) {
	case SessionRunnerModeTypeCommanded:
		fallthrough
	case SessionRunnerModeTypeByPassed:
		return true
	}
	return false
}

func validateSessionInputCmdType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	switch SessionInputCommandTypeENUMType(fl.Field().String()) {
	case SessionInputCommandTypeText:
		fallthrough
	case SessionInputCommandTypeCTRL:
		fallthrough
	case SessionInputCommandTypeCR:
		fallthrough
	case SessionInputCommandTypeRaw:
		return true
	}
	return false
}

func validateContainerStopSignal(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	switch ContainerStopSignalENUMType(fl.Field().String()) {
	case ContainerStopSignalSIGINT:
		fallthrough
	case ContainerStopSignalSIGTERM:
		fallthrough
	case ContainerStopSignalSIGQUIT:
		fallthrough
	case ContainerStopSignalSIGHUP:
		fallthrough
	case ContainerStopSignalSIGKILL:
		return true
	}
	return false
}

// validateSessionDriverDockerParams struct-level validation for SessionDriverDockerParams.
// Publishing ports for inbound connections requires a routable network mode; the default
// "none" network mode (also the empty-string default) cannot accept inbound connections.
func validateSessionDriverDockerParams(sl validator.StructLevel) {
	params := sl.Current().Interface().(SessionDriverDockerParams)
	if len(params.PublishPorts) == 0 {
		return
	}
	networkMode := params.NetworkMode
	if networkMode == "" {
		networkMode = DefaultContainerNetworkMode
	}
	if networkMode == "none" {
		sl.ReportError(
			params.PublishPorts,
			"PublishPorts",
			"PublishPorts",
			"publish_ports requires a routable network_mode (not 'none')",
			"",
		)
	}
}

func validateSessionDriverType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	switch SessionDriverTypeENUMType(fl.Field().String()) {
	case SessionDriverTypePTY:
		fallthrough
	case SessionDriverTypeDocker:
		return true
	}
	return false
}

var validSessionNameREGEX = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

func validateSessionNameType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	return validSessionNameREGEX.MatchString(fl.Field().String())
}

func validateIPCMessageType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	switch IPCMessageTypeEnumType(fl.Field().String()) {
	case IPCMsgTypeReqRunCommands:
		fallthrough
	case IPCMsgTypeReqStopSession:
		fallthrough
	case IPCMsgTypeRespRunCommands:
		fallthrough
	case IPCMsgTypeRespStopSession:
		return true
	}
	return false
}
