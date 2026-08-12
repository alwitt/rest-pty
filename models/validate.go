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

	if err := v.RegisterValidation(
		"workspace_name_type", validateWorkspaceNameType,
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

	v.RegisterStructValidation(validateSession, Session{})
	v.RegisterStructValidation(validateSessionInputCommand, SessionInputCommand{})
	v.RegisterStructValidation(validateSessionDriverDockerParams, SessionDriverDockerParams{})

	return goutils.RegisterWithValidator(v)
}

// validateSession struct-level validation for Session, enforcing the relationship between the
// driver type and the workspace assignment.
//
// A workspace is a cairn concept pairing an object store with a Docker named volume, so it is
// only meaningful for a DOCKER driver session: a PTY session runs directly against the host
// filesystem and has no container to mount the volume into. Expressed as a struct-level rule
// rather than a conditional field tag, matching every other cross-field rule in this package.
func validateSession(sl validator.StructLevel) {
	session := sl.Current().Interface().(Session)
	if session.WorkspaceName != nil && session.DriverType != SessionDriverTypeDocker {
		sl.ReportError(
			session.WorkspaceName,
			"WorkspaceName",
			"WorkspaceName",
			"workspace_name is only valid for DOCKER driver sessions",
			"",
		)
	}
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

// validWorkspaceNameREGEX the charset a cairn workspace name may use. Deliberately identical
// to cairn's `valid_name` so the two services agree on what a workspace name is; rest-pty
// resolves this name against cairn, so a charset rest-pty accepts but cairn does not would
// only surface as a confusing lookup failure later.
//
// Note this is NOT validSessionNameREGEX: a session name forbids `_`, a workspace name allows
// it. The two namespaces are owned by different services and must not be conflated.
var validWorkspaceNameREGEX = regexp.MustCompile(`^[a-zA-Z0-9-_]+$`)

// validateWorkspaceNameType whether a field is a cairn workspace name of the permitted charset
func validateWorkspaceNameType(fl validator.FieldLevel) bool {
	if fl.Field().Kind() != reflect.String {
		return false
	}
	return validWorkspaceNameREGEX.MatchString(fl.Field().String())
}
