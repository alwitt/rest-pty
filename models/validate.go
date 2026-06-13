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

	return nil
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
