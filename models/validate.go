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

	v.RegisterStructValidation(validateSessionInputCommand, SessionInputCommand{})

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
	case SessionStateStarting:
		fallthrough
	case SessionStateReady:
		fallthrough
	case SessionStateClaimed:
		fallthrough
	case SessionStateStopping:
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
		return true
	}
	return false
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
	case IPCMsgTypeReqClaimSession:
		fallthrough
	case IPCMsgTypeReqReleaseSession:
		fallthrough
	case IPCMsgTypeReqVerifyClaim:
		fallthrough
	case IPCMsgTypeReqRunCommands:
		fallthrough
	case IPCMsgTypeRespClaimSession:
		fallthrough
	case IPCMsgTypeRespReleaseSession:
		fallthrough
	case IPCMsgTypeRespVerifyClaim:
		fallthrough
	case IPCMsgTypeRespRunCommands:
		return true
	}
	return false
}
