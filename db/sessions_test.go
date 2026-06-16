package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm/logger"
)

func TestDBSanityCheck(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))
}

func TestDBCreateSession(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	// Define the test sessions
	type testCase struct {
		name        string
		description *string
		command     models.SessionCommand
		bufCapacity int64
	}
	description0 := "test session 0 description"
	session0 := testCase{
		name:        fmt.Sprintf("test-session-0-%s", ulid.Make().String()),
		description: &description0,
		command:     models.SessionCommand{Command: "echo", Arguments: []string{"hello", "world"}},
		bufCapacity: 16384,
	}
	session1 := testCase{
		name:        fmt.Sprintf("test-session-1-%s", ulid.Make().String()),
		description: nil,
		command:     models.SessionCommand{Command: "cat"},
		bufCapacity: 32768,
	}

	// verify helper to read back a session and check its content
	verify := func(expected testCase) {
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				readBack, err := dbClient.GetSessionByName(ctx, expected.name)
				assert.Nil(err)
				assert.Equal(expected.name, readBack.Name)
				assert.Equal(expected.description, readBack.Description)
				assert.Equal(expected.command, readBack.Command)
				assert.Equal(expected.bufCapacity, readBack.OutputBufferCapacity)
				assert.Equal(models.SessionStateIdle, readBack.State)
				return nil
			},
		))
	}

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// Case 0: create test session 0
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			entry, err := dbClient.DefineNewSession(
				ctx,
				session0.name,
				session0.description,
				session0.command,
				session0.bufCapacity,
				testDriverParams,
			)
			assert.Nil(err)
			assert.Equal(session0.name, entry.Name)
			assert.Equal(models.SessionStateIdle, entry.State)
			return nil
		},
	))

	// Case 1: read back test session 0 and verify its content
	verify(session0)

	// Case 2: create another session with the same name as test session 0. This should fail.
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.DefineNewSession(
				ctx,
				session0.name,
				session1.description,
				session1.command,
				session1.bufCapacity,
				testDriverParams,
			)
			return err
		},
	))

	// Case 3: create test session 1
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			entry, err := dbClient.DefineNewSession(
				ctx,
				session1.name,
				session1.description,
				session1.command,
				session1.bufCapacity,
				testDriverParams,
			)
			assert.Nil(err)
			assert.Equal(session1.name, entry.Name)
			assert.Equal(models.SessionStateIdle, entry.State)
			return nil
		},
	))

	// Case 4: read back test session 1 and verify its content
	verify(session1)

	// Case 5: defining a session with invalid attributes is rejected with a ValidationError
	invalidCases := []struct {
		label   string
		name    string
		command models.SessionCommand
		bufCap  int64
	}{
		// Name contains characters disallowed by `session_name_type`
		{label: "bad name", name: "invalid name!", command: session1.command, bufCap: 16384},
		// Command is empty
		{label: "empty command", name: fmt.Sprintf("c-%s", ulid.Make().String()),
			command: models.SessionCommand{}, bufCap: 16384},
		// Output buffer capacity below the 16384 minimum
		{label: "tiny buffer", name: fmt.Sprintf("c-%s", ulid.Make().String()),
			command: session1.command, bufCap: 1024},
	}
	for _, testCase := range invalidCases {
		err := uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				_, err := dbClient.DefineNewSession(
					ctx,
					testCase.name,
					nil,
					testCase.command,
					testCase.bufCap,
					testDriverParams,
				)
				return err
			},
		)
		var validationErr models.ValidationError
		assert.Truef(errors.As(err, &validationErr),
			"expected ValidationError for case '%s', got %v", testCase.label, err)
	}
}

func TestDBUpdateSessionState(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// Define a session to drive through the state machine
	sessionName := fmt.Sprintf("test-session-%s", ulid.Make().String())
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			entry, err := dbClient.DefineNewSession(
				ctx,
				sessionName,
				nil,
				models.SessionCommand{Command: "echo", Arguments: []string{"hello"}},
				16384,
				testDriverParams,
			)
			assert.Nil(err)
			// A newly defined session starts in the IDLE state
			assert.Equal(models.SessionStateIdle, entry.State)
			return nil
		},
	))

	// verifyState reads the session back and checks its current state
	verifyState := func(expected models.SessionStateENUMType) {
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				readBack, err := dbClient.GetSessionByName(ctx, sessionName)
				assert.Nil(err)
				assert.Equal(expected, readBack.State)
				return nil
			},
		))
	}

	// Walk the basic state transition path, reading the session back after each Mark call
	// IDLE -> IDLE -> READY -> READY -> IDLE
	transitions := []struct {
		mark     func(ctx context.Context, dbClient db.Database) error
		expected models.SessionStateENUMType
	}{
		{
			mark: func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionIdle(ctx, sessionName)
			},
			expected: models.SessionStateIdle,
		},
		{
			mark: func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionReady(ctx, sessionName)
			},
			expected: models.SessionStateReady,
		},
		{
			mark: func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionReady(ctx, sessionName)
			},
			expected: models.SessionStateReady,
		},
		{
			mark: func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionIdle(ctx, sessionName)
			},
			expected: models.SessionStateIdle,
		},
		{
			mark: func(ctx context.Context, dbClient db.Database) error {
				return dbClient.MarkSessionIdle(ctx, sessionName)
			},
			expected: models.SessionStateIdle,
		},
	}

	for _, transition := range transitions {
		assert.Nil(uut.UseDatabaseInTransaction(utCtx, transition.mark))
		verifyState(transition.expected)
	}
}

func TestDBUpdateBasicSessionParams(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// Create a test session
	originalName := fmt.Sprintf("test-session-%s", ulid.Make().String())
	originalDescription := "original description"
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			entry, err := dbClient.DefineNewSession(
				ctx,
				originalName,
				&originalDescription,
				models.SessionCommand{Command: "echo", Arguments: []string{"hello"}},
				16384,
				testDriverParams,
			)
			assert.Nil(err)
			assert.Equal(originalName, entry.Name)
			assert.Equal(&originalDescription, entry.Description)
			return nil
		},
	))

	// verify reads the session back (by name) and checks its name and description
	verify := func(name string, description *string) {
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				readBack, err := dbClient.GetSessionByName(ctx, name)
				assert.Nil(err)
				assert.Equal(name, readBack.Name)
				assert.Equal(description, readBack.Description)
				return nil
			},
		))
	}

	// Change the session name
	newName := fmt.Sprintf("renamed-session-%s", ulid.Make().String())
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionName(ctx, originalName, newName)
		},
	))
	// Read back and verify the name change (description is unchanged)
	verify(newName, &originalDescription)

	// Change the session description
	newDescription := "updated description"
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDescription(ctx, newName, &newDescription)
		},
	))
	// Read back and verify the description change
	verify(newName, &newDescription)

	// Set the description to nil
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDescription(ctx, newName, nil)
		},
	))
	// Read back and verify the description was cleared
	verify(newName, nil)

	// Updating an unknown session is rejected with an UnknownSessionError
	unknownName := fmt.Sprintf("missing-%s", ulid.Make().String())
	nameErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionName(ctx, unknownName, "whatever")
		},
	)
	var unknownErr models.UnknownSessionError
	assert.True(errors.As(nameErr, &unknownErr))
	descErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDescription(ctx, unknownName, &newDescription)
		},
	)
	assert.True(errors.As(descErr, &unknownErr))

	// Renaming to an invalid name is rejected with a ValidationError
	invalidNameErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionName(ctx, newName, "invalid name!")
		},
	)
	var validationErr models.ValidationError
	assert.True(errors.As(invalidNameErr, &validationErr))
}

func TestDBUpdateCriticalSessionParams(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// Prepare a test session
	sessionName := fmt.Sprintf("test-session-%s", ulid.Make().String())
	originalCommand := models.SessionCommand{Command: "echo", Arguments: []string{"hello"}}
	originalCapacity := int64(16384)
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.DefineNewSession(
				ctx, sessionName, nil, originalCommand, originalCapacity, testDriverParams,
			)
			return err
		},
	))

	newCommand := models.SessionCommand{Command: "cat", Arguments: []string{"-n"}}
	newCapacity := int64(32768)
	// A newly defined session starts in the COMMANDED runner mode
	newRunMode := models.SessionRunnerModeTypeByPassed

	// Change the test session to READY state
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionReady(ctx, sessionName)
		},
	))

	// Updating the output buffer capacity, command and runner mode must fail outside of IDLE state
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionOutputBufCapacity(ctx, sessionName, newCapacity)
		},
	))
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionCommand(ctx, sessionName, newCommand)
		},
	))
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionRunMode(ctx, sessionName, newRunMode)
		},
	))

	// Change the test session back to IDLE state
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionIdle(ctx, sessionName)
		},
	))

	// Update the output buffer capacity, command and runner mode, which are now permitted
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionOutputBufCapacity(ctx, sessionName, newCapacity)
		},
	))
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionCommand(ctx, sessionName, newCommand)
		},
	))
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionRunMode(ctx, sessionName, newRunMode)
		},
	))

	// Read back the session and verify the changes occurred
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			readBack, err := dbClient.GetSessionByName(ctx, sessionName)
			assert.Nil(err)
			assert.Equal(newCapacity, readBack.OutputBufferCapacity)
			assert.Equal(newCommand, readBack.Command)
			assert.Equal(newRunMode, readBack.RunnerMode)
			return nil
		},
	))

	// Invalid values are rejected with a ValidationError (session is IDLE here)
	var validationErr models.ValidationError
	tinyCapErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionOutputBufCapacity(ctx, sessionName, 1024)
		},
	)
	assert.True(errors.As(tinyCapErr, &validationErr))
	emptyCmdErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionCommand(ctx, sessionName, models.SessionCommand{})
		},
	)
	assert.True(errors.As(emptyCmdErr, &validationErr))
	badRunModeErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionRunMode(ctx, sessionName, "not-a-run-mode")
		},
	)
	assert.True(errors.As(badRunModeErr, &validationErr))

	// Updating an unknown session is rejected with an UnknownSessionError
	var unknownErr models.UnknownSessionError
	unknownName := fmt.Sprintf("missing-%s", ulid.Make().String())
	unknownCapErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionOutputBufCapacity(ctx, unknownName, newCapacity)
		},
	)
	assert.True(errors.As(unknownCapErr, &unknownErr))
	unknownCmdErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionCommand(ctx, unknownName, newCommand)
		},
	)
	assert.True(errors.As(unknownCmdErr, &unknownErr))
	unknownRunModeErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionRunMode(ctx, unknownName, newRunMode)
		},
	)
	assert.True(errors.As(unknownRunModeErr, &unknownErr))
}

func TestDBUpdateSessionDriverParameters(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	originalDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// Prepare a test session (starts IDLE)
	sessionName := fmt.Sprintf("test-session-%s", ulid.Make().String())
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.DefineNewSession(
				ctx,
				sessionName,
				nil,
				models.SessionCommand{Command: "echo", Arguments: []string{"hello"}},
				16384,
				originalDriverParams,
			)
			return err
		},
	))

	newDriverParams := models.SessionDriverPTYParams{DisplayRows: 60, DisplayCols: 120}

	// Validator used to parse the persisted driver metadata back into typed parameters
	validate := validator.New()
	assert.Nil(models.RegisterWithValidator(validate))

	// Case 0: the update must fail outside of the IDLE state
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionReady(ctx, sessionName)
		},
	))
	nonIdleErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(ctx, sessionName, newDriverParams)
		},
	)
	var consistencyErr models.ConsistencyError
	assert.True(errors.As(nonIdleErr, &consistencyErr))

	// Return the session to IDLE so updates are permitted again
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionIdle(ctx, sessionName)
		},
	))

	// Case 1: an unsupported driver parameter data type is rejected with a ValidationError
	var validationErr models.ValidationError
	badTypeErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(ctx, sessionName, "not-a-driver-params")
		},
	)
	assert.True(errors.As(badTypeErr, &validationErr))

	// Case 2: driver parameters with invalid attributes are rejected with a ValidationError
	//   DisplayRows below the 30 minimum and DisplayCols below the 80 minimum
	invalidAttrErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(
				ctx, sessionName, models.SessionDriverPTYParams{DisplayRows: 10, DisplayCols: 20},
			)
		},
	)
	assert.True(errors.As(invalidAttrErr, &validationErr))

	// Case 3: a valid update on an IDLE session succeeds
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(ctx, sessionName, newDriverParams)
		},
	))

	// Read back the session and verify the driver parameters were recorded
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			readBack, err := dbClient.GetSessionByName(ctx, sessionName)
			assert.Nil(err)
			assert.Equal(models.SessionDriverTypePTY, readBack.DriverType)
			parsed, err := readBack.ParseDriverMetadata(validate)
			assert.Nil(err)
			assert.IsType(models.SessionDriverPTYParams{}, parsed)
			assert.Equal(newDriverParams, parsed)
			return nil
		},
	))

	// Case 4: updating an unknown session is rejected with an UnknownSessionError
	unknownErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(
				ctx, fmt.Sprintf("missing-%s", ulid.Make().String()), newDriverParams,
			)
		},
	)
	var unknownSessionErr models.UnknownSessionError
	assert.True(errors.As(unknownErr, &unknownSessionErr))
}

func TestDBDeleteSession(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// defineSession helper to create a fresh test session
	defineSession := func() string {
		name := fmt.Sprintf("test-session-%s", ulid.Make().String())
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				_, err := dbClient.DefineNewSession(
					ctx,
					name,
					nil,
					models.SessionCommand{Command: "echo", Arguments: []string{"hello"}},
					16384,
					testDriverParams,
				)
				return err
			},
		))
		return name
	}

	// Case 0: a session outside of IDLE state can't be deleted
	nonIdleSession := defineSession()
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionReady(ctx, nonIdleSession)
		},
	))
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.DeleteSession(ctx, nonIdleSession)
		},
	))
	// The session must still exist
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.GetSessionByName(ctx, nonIdleSession)
			return err
		},
	))

	// Returning the session to IDLE allows it to be deleted
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionIdle(ctx, nonIdleSession)
		},
	))
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.DeleteSession(ctx, nonIdleSession)
		},
	))
	// The session must now be unknown
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.GetSessionByName(ctx, nonIdleSession)
			return err
		},
	))

	// Case 1: an IDLE session can be deleted directly
	idleSession := defineSession()
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.DeleteSession(ctx, idleSession)
		},
	))
	assert.NotNil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.GetSessionByName(ctx, idleSession)
			return err
		},
	))

	// Case 2: deleting an unknown session is rejected with an UnknownSessionError
	unknownErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.DeleteSession(ctx, fmt.Sprintf("missing-%s", ulid.Make().String()))
		},
	)
	var unknownSessionErr models.UnknownSessionError
	assert.True(errors.As(unknownErr, &unknownSessionErr))
}

func TestDBListSessions(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	// Define three sessions. Sessions 2 and 3 share a common name prefix.
	runID := ulid.Make().String()
	sharedPrefix := fmt.Sprintf("shared-%s", runID)
	name1 := fmt.Sprintf("session1-%s", runID)
	name2 := fmt.Sprintf("%s-session2", sharedPrefix)
	name3 := fmt.Sprintf("%s-session3", sharedPrefix)

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	for _, name := range []string{name1, name2, name3} {
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				_, err := dbClient.DefineNewSession(
					ctx,
					name,
					nil,
					models.SessionCommand{Command: "echo", Arguments: []string{"hello"}},
					16384,
					testDriverParams,
				)
				return err
			},
		))
	}

	// Transition the sessions into distinct states
	//   session 1 -> IDLE (left as created), sessions 2 and 3 -> READY
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionReady(ctx, name2)
		},
	))
	assert.Nil(uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.MarkSessionReady(ctx, name3)
		},
	))

	// listNames helper to run a list query and return the resulting session names
	listNames := func(filter db.SessionQueryFilter) []string {
		var names []string
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				sessions, err := dbClient.ListSessions(ctx, filter)
				if err != nil {
					return err
				}
				for _, session := range sessions {
					names = append(names, session.Name)
				}
				return nil
			},
		))
		return names
	}

	// List all sessions: all three should return
	allNames := listNames(db.SessionQueryFilter{})
	assert.ElementsMatch([]string{name1, name2, name3}, allNames)

	// List sessions whose name contains the prefix shared by 2 and 3
	similarNames := listNames(db.SessionQueryFilter{SimilarName: &sharedPrefix})
	assert.ElementsMatch([]string{name2, name3}, similarNames)

	// List all READY sessions: sessions 2 and 3 should return
	stateFilteredNames := listNames(db.SessionQueryFilter{
		TargetStates: []models.SessionStateENUMType{
			models.SessionStateReady,
		},
	})
	assert.ElementsMatch([]string{name2, name3}, stateFilteredNames)
}

func TestDBListSessionsPagination(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	testDB := fmt.Sprintf("/tmp/rest_pty_ut_%s.db", ulid.Make().String())
	log.WithField("db", testDB).Debug("Test database")

	uut, err := db.NewConnection(db.GetSqliteDialector(testDB), logger.Error)
	assert.Nil(err)

	// Prepare the database tables
	assert.Nil(uut.RunSQLInTransaction(utCtx, db.DefineTables))

	testDriverParams := models.SessionDriverPTYParams{DisplayRows: 30, DisplayCols: 80}

	// Define sessions with deterministic, ordered names so ordering can be verified
	runID := ulid.Make().String()
	names := []string{
		fmt.Sprintf("%s-session-a", runID),
		fmt.Sprintf("%s-session-b", runID),
		fmt.Sprintf("%s-session-c", runID),
		fmt.Sprintf("%s-session-d", runID),
	}
	for _, name := range names {
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				_, err := dbClient.DefineNewSession(
					ctx,
					name,
					nil,
					models.SessionCommand{Command: "echo", Arguments: []string{"hello"}},
					16384,
					testDriverParams,
				)
				return err
			},
		))
	}

	// listNames helper to run a list query and return the resulting session names in order
	listNames := func(filter db.SessionQueryFilter) []string {
		var result []string
		assert.Nil(uut.UseDatabaseInTransaction(
			utCtx, func(ctx context.Context, dbClient db.Database) error {
				sessions, err := dbClient.ListSessions(ctx, filter)
				if err != nil {
					return err
				}
				for _, session := range sessions {
					result = append(result, session.Name)
				}
				return nil
			},
		))
		return result
	}

	asc := "asc"
	desc := "desc"

	// Order by name ascending returns all sessions in name order
	assert.Equal(names, listNames(db.SessionQueryFilter{OrderByName: true, OrderDirection: &asc}))

	// Order by name descending returns the reverse
	reversed := []string{names[3], names[2], names[1], names[0]}
	assert.Equal(reversed, listNames(db.SessionQueryFilter{OrderByName: true, OrderDirection: &desc}))

	// Limit caps the number of returned entries (first two by name)
	limit := 2
	assert.Equal(names[:2], listNames(db.SessionQueryFilter{
		OrderByName:                true,
		OrderDirection:             &asc,
		CommonListEntryQueryFilter: db.CommonListEntryQueryFilter{Limit: &limit},
	}))

	// Offset skips entries; combined with limit it pages through the results
	offset := 2
	assert.Equal(names[2:], listNames(db.SessionQueryFilter{
		OrderByName:                true,
		OrderDirection:             &asc,
		CommonListEntryQueryFilter: db.CommonListEntryQueryFilter{Limit: &limit, Offset: &offset},
	}))

	// An invalid filter is rejected with a ValidationError
	badLimit := 0
	invalidErr := uut.UseDatabaseInTransaction(
		utCtx, func(ctx context.Context, dbClient db.Database) error {
			_, err := dbClient.ListSessions(
				ctx, db.SessionQueryFilter{
					CommonListEntryQueryFilter: db.CommonListEntryQueryFilter{Limit: &badLimit},
				},
			)
			return err
		},
	)
	var validationErr models.ValidationError
	assert.True(errors.As(invalidErr, &validationErr))
}
