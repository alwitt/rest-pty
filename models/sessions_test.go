package models_test

import (
	"testing"

	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
)

func TestSessionStateTransitions(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	allStates := []models.SessionStateENUMType{
		models.SessionStateIdle,
		models.SessionStateReady,
	}

	// allowed defines the complete set of valid transitions for the state machine
	allowed := map[models.SessionStateENUMType]map[models.SessionStateENUMType]bool{
		models.SessionStateIdle: {
			models.SessionStateIdle:  true,
			models.SessionStateReady: true,
		},
		models.SessionStateReady: {
			models.SessionStateReady: true,
			models.SessionStateIdle:  true,
		},
	}

	// Exhaustively verify every (from, to) pair against the expected matrix
	for _, from := range allStates {
		for _, to := range allStates {
			session := models.Session{State: from}
			err := session.ValidNextState(to)
			if allowed[from][to] {
				assert.Nilf(err, "expected transition '%s' -> '%s' to be valid", from, to)
			} else {
				assert.NotNilf(err, "expected transition '%s' -> '%s' to be invalid", from, to)
			}
		}
	}

	// A session in an unknown state can't transition anywhere
	unknown := models.Session{State: models.SessionStateENUMType("UNKNOWN")}
	for _, to := range allStates {
		assert.NotNilf(
			unknown.ValidNextState(to),
			"expected transition from unknown state to '%s' to be invalid", to,
		)
	}
}

func TestSessionWorkspaceNameValidation(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	validate := validator.New()
	assert.Nil(models.RegisterWithValidator(validate))

	ptr := func(v string) *string { return &v }

	type testCase struct {
		name          string
		driverType    models.SessionDriverTypeENUMType
		workspaceName *string
		valid         bool
	}

	tests := []testCase{
		{name: "docker-no-workspace", driverType: models.SessionDriverTypeDocker, valid: true},
		{
			name:          "docker-hyphen",
			driverType:    models.SessionDriverTypeDocker,
			workspaceName: ptr("ws-alpha"),
			valid:         true,
		},
		// A workspace name permits '_' where a session name does not; the two namespaces are
		// owned by different services.
		{
			name:          "docker-underscore",
			driverType:    models.SessionDriverTypeDocker,
			workspaceName: ptr("ws_alpha"),
			valid:         true,
		},
		{
			name:          "docker-space",
			driverType:    models.SessionDriverTypeDocker,
			workspaceName: ptr("ws alpha"),
			valid:         false,
		},
		// cairn workspace names forbid '.'
		{
			name:          "docker-dot",
			driverType:    models.SessionDriverTypeDocker,
			workspaceName: ptr("ws.alpha"),
			valid:         false,
		},
		// Locks the `omitnil` tag in place: under `omitempty` the pointed-to "" would skip the
		// charset check entirely and persist an empty workspace name.
		{
			name:          "docker-empty",
			driverType:    models.SessionDriverTypeDocker,
			workspaceName: ptr(""),
			valid:         false,
		},
		{name: "pty-no-workspace", driverType: models.SessionDriverTypePTY, valid: true},
		// The DOCKER-only rule
		{
			name:          "pty-with-workspace",
			driverType:    models.SessionDriverTypePTY,
			workspaceName: ptr("ws-alpha"),
			valid:         false,
		},
	}

	for _, test := range tests {
		session := models.Session{
			ID:                   ulid.Make().String(),
			Name:                 "unit-tester",
			Command:              models.SessionCommand{Command: "/bin/sh"},
			State:                models.SessionStateIdle,
			DriverType:           test.driverType,
			WorkspaceName:        test.workspaceName,
			OutputBufferCapacity: 16384,
			RunnerMode:           models.SessionRunnerModeTypeCommanded,
		}

		err := validate.Struct(&session)
		if test.valid {
			assert.Nilf(err, "expected case '%s' to be valid", test.name)
		} else {
			assert.NotNilf(err, "expected case '%s' to be invalid", test.name)
		}
	}
}
