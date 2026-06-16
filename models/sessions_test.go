package models_test

import (
	"testing"

	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
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
