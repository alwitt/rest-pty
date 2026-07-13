package models

import (
	"reflect"
	"regexp"

	"github.com/alwitt/goutils"
	goutilsRuntime "github.com/alwitt/goutils/runtime"
	"github.com/go-playground/validator/v10"
)

/*
RegisterWithValidator register with the validator this custom validation support

	@param v *validator.Validate - the validator to register against
	@return whether successful
*/
func RegisterWithValidator(v *validator.Validate) error {
	if err := goutils.RegisterENUMInValidator(
		v, "session_state_type", goutils.ValidateStringENUM[SessionStateENUMType](),
	); err != nil {
		return err
	}

	if err := v.RegisterValidation(
		"session_name_type", validateSessionNameType,
	); err != nil {
		return err
	}

	if err := goutils.RegisterENUMInValidator(
		v, "session_driver_type", goutils.ValidateStringENUM[SessionDriverTypeENUMType](),
	); err != nil {
		return err
	}

	if err := goutils.RegisterENUMInValidator(
		v, "session_runner_mode_type", goutils.ValidateStringENUM[SessionRunnerModeTypeENUMType](),
	); err != nil {
		return err
	}

	if err := goutils.RegisterENUMInValidator(
		v, "session_input_cmd_type", goutils.ValidateStringENUM[SessionInputCommandTypeENUMType](),
	); err != nil {
		return err
	}

	if err := goutils.RegisterENUMInValidator(
		v, "ipc_msg_type", goutils.ValidateStringENUM[IPCMessageTypeEnumType](),
	); err != nil {
		return err
	}

	v.RegisterStructValidation(validateSessionInputCommand, SessionInputCommand{})
	v.RegisterStructValidation(validateSessionDriverDockerParams, SessionDriverDockerParams{})

	return goutils.RegisterWithValidator(v)
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
		networkMode = goutilsRuntime.DefaultDockerNetworkMode
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

var validSessionNameREGEX = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

func validateSessionNameType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	return validSessionNameREGEX.MatchString(fl.Field().String())
}
