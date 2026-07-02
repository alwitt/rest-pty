package api

import (
	"context"
	"encoding/json"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/go-playground/validator/v10"
)

// SessionManagerCore transport-agnostic session management business logic. It holds the
// dependencies shared by the REST handlers and the MCP tools so the actual
// persistence / session-manager operations live in one place independent of transport.
type SessionManagerCore struct {
	// validate request/parameter validator
	validate *validator.Validate

	// persistence layer client
	persistence db.Client

	// manager of sessions
	manager session.Manager
}

/*
Ready run the persistence-layer readiness check.

	@param ctx context.Context - execution context
	@returns error if the persistence layer is not ready
*/
func (c SessionManagerCore) Ready(ctx context.Context) error {
	return c.persistence.UseDatabaseInTransaction(
		ctx, func(_ context.Context, dbClient db.Database) error {
			return dbClient.Ready()
		},
	)
}

/*
GetSession fetch one session by its name.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to fetch
	@returns the session, or a goutils.NotFoundError if no such session exists
*/
func (c SessionManagerCore) GetSession(
	ctx context.Context, sessionName string,
) (models.Session, error) {
	var session models.Session
	err := c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			session, err = dbClient.GetSessionByName(ctx, sessionName)
			return err
		},
	)
	return session, err
}

/*
ListSessions list the known sessions matching the given filter.

The filter is validated before the query is run; an invalid filter is reported as a
goutils.ValidationError so callers can distinguish bad input from a query failure.

	@param ctx context.Context - execution context
	@param filters db.SessionQueryFilter - filtering and ordering to apply
	@returns the matching sessions
*/
func (c SessionManagerCore) ListSessions(
	ctx context.Context, filters db.SessionQueryFilter,
) ([]models.Session, error) {
	// Validate the filter
	if err := c.validate.Struct(&filters); err != nil {
		return nil, goutils.NewValidationError("session list query filters not valid", err, true)
	}

	// Fetch the sessions
	var sessions []models.Session
	err := c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			sessions, err = dbClient.ListSessions(ctx, filters)
			return err
		},
	)
	return sessions, err
}

/*
UpdateSessionOutputBufCapacity change the output buffer capacity of a session, applied
through the session manager. Only permitted on IDLE sessions.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to update
	@param newCapacity int64 - the new output buffer capacity
	@param blocking bool - whether to wait for the change to complete
	@returns goutils.NotFoundError if the session is unknown, goutils.ConsistencyError if
		the session is not in a state allowing the change
*/
func (c SessionManagerCore) UpdateSessionOutputBufCapacity(
	ctx context.Context, sessionName string, newCapacity int64, blocking bool,
) error {
	return c.manager.ChangeOutputBufferCapacity(ctx, sessionName, newCapacity, blocking)
}

/*
UpdateSessionRunMode change the runner mode of a session. Only permitted on IDLE sessions.

The mode is validated before the change is applied; an invalid mode is reported as a
goutils.ValidationError.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to update
	@param newMode models.SessionRunnerModeTypeENUMType - the new runner mode
	@returns goutils.ValidationError if the mode is invalid, goutils.NotFoundError if the
		session is unknown, goutils.ConsistencyError if the session is not in a state
		allowing the change
*/
func (c SessionManagerCore) UpdateSessionRunMode(
	ctx context.Context, sessionName string, newMode models.SessionRunnerModeTypeENUMType,
) error {
	// Validate the mode
	if err := c.validate.Var(string(newMode), "session_runner_mode_type"); err != nil {
		return goutils.NewValidationError("session runner mode is not valid", err, true)
	}

	return c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionRunMode(ctx, sessionName, newMode)
		},
	)
}

/*
UpdateSessionName change the name of a session.

The new name is validated before the change is applied; an invalid name is reported as a
goutils.ValidationError.

	@param ctx context.Context - execution context
	@param sessionName string - the current name of the session to update
	@param newName string - the new session name
	@returns goutils.ValidationError if the name is invalid, goutils.NotFoundError if the
		session is unknown
*/
func (c SessionManagerCore) UpdateSessionName(
	ctx context.Context, sessionName string, newName string,
) error {
	// Validate the new name
	if err := c.validate.Var(newName, "session_name_type"); err != nil {
		return goutils.NewValidationError("session name is not valid", err, true)
	}

	return c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionName(ctx, sessionName, newName)
		},
	)
}

/*
DefineNewSession define a new session.

The parameters and the driver metadata are validated before the session is created; parse and
validation failures are reported as a goutils.ValidationError.

	@param ctx context.Context - execution context
	@param params NewSessionRequest - the new session parameters
	@returns the newly defined session, or a goutils.ValidationError if the parameters are
		invalid
*/
func (c SessionManagerCore) DefineNewSession(
	ctx context.Context, params NewSessionRequest,
) (models.Session, error) {
	// Validate the parameters
	if err := c.validate.Struct(&params); err != nil {
		return models.Session{}, goutils.NewValidationError(
			"new session parameters not valid", err, true,
		)
	}

	// Parse and validate the driver metadata
	driverMetadata, err := c.resolveDriverMetadata(params.DriverType, params.DriverMetadata)
	if err != nil {
		return models.Session{}, err
	}

	// Define the session
	var newSession models.Session
	dbErr := c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			var err error
			newSession, err = dbClient.DefineNewSession(
				ctx,
				params.Name,
				params.Description,
				params.Command,
				params.OutputBufferCapacity,
				driverMetadata,
			)
			return err
		},
	)
	return newSession, dbErr
}

/*
UpdateSessionCommand change the command a session runs. Only permitted on IDLE sessions.

The command is validated before the change is applied; an invalid command is reported as a
goutils.ValidationError.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to update
	@param newCommand models.SessionCommand - the new command
	@returns goutils.ValidationError if the command is invalid, goutils.NotFoundError if the
		session is unknown, goutils.ConsistencyError if the session is not in a state
		allowing the change
*/
func (c SessionManagerCore) UpdateSessionCommand(
	ctx context.Context, sessionName string, newCommand models.SessionCommand,
) error {
	// Validate the command
	if err := c.validate.Struct(&newCommand); err != nil {
		return goutils.NewValidationError("session command is not valid", err, true)
	}

	return c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionCommand(ctx, sessionName, newCommand)
		},
	)
}

/*
resolveDriverMetadata parse and validate the raw driver metadata for the given driver type,
returning the concrete, validated driver parameter value.

The raw metadata is JSON (as carried by both REST bodies and MCP tool arguments), so this is
the single transport-agnostic entry point for turning a (driver type, raw metadata) pair into
a validated driver parameter object. Parse and validation failures are reported as a
goutils.ValidationError; an unsupported driver type is likewise a validation failure.

	@param driverType models.SessionDriverTypeENUMType - the driver type the metadata describes
	@param rawMetadata json.RawMessage - the raw driver metadata JSON
	@returns the validated driver parameter value
*/
func (c SessionManagerCore) resolveDriverMetadata(
	driverType models.SessionDriverTypeENUMType, rawMetadata json.RawMessage,
) (interface{}, error) {
	switch driverType {
	case models.SessionDriverTypePTY:
		var ptyDriverMetadata models.SessionDriverPTYParams
		if err := json.Unmarshal(rawMetadata, &ptyDriverMetadata); err != nil {
			return nil, goutils.NewValidationError(
				"unable to parse PTY driver parameters", err, true,
			)
		}
		if err := c.validate.Struct(&ptyDriverMetadata); err != nil {
			return nil, goutils.NewValidationError("PTY driver parameters not valid", err, true)
		}
		return ptyDriverMetadata, nil

	case models.SessionDriverTypeDocker:
		var dockerDriverMetadata models.SessionDriverDockerParams
		if err := json.Unmarshal(rawMetadata, &dockerDriverMetadata); err != nil {
			return nil, goutils.NewValidationError(
				"unable to parse DOCKER driver parameters", err, true,
			)
		}
		if err := c.validate.Struct(&dockerDriverMetadata); err != nil {
			return nil, goutils.NewValidationError("DOCKER driver parameters not valid", err, true)
		}
		return dockerDriverMetadata, nil

	default:
		return nil, goutils.NewValidationError(
			"driver type '"+string(driverType)+"' not supported", nil, true,
		)
	}
}

/*
UpdateSessionDriver change the driver a session uses, along with its setup metadata. Only
permitted on IDLE sessions.

The driver metadata is parsed and validated for the given driver type before the change is
applied; parse/validation failures are reported as a goutils.ValidationError.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to update
	@param driverType models.SessionDriverTypeENUMType - the new driver type
	@param rawMetadata json.RawMessage - the raw driver metadata JSON
	@returns goutils.ValidationError if the driver parameters are invalid, goutils.NotFoundError
		if the session is unknown, goutils.ConsistencyError if the session is not in a state
		allowing the change
*/
func (c SessionManagerCore) UpdateSessionDriver(
	ctx context.Context,
	sessionName string,
	driverType models.SessionDriverTypeENUMType,
	rawMetadata json.RawMessage,
) error {
	driverMetadata, err := c.resolveDriverMetadata(driverType, rawMetadata)
	if err != nil {
		return err
	}

	return c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDriver(ctx, sessionName, driverMetadata)
		},
	)
}

/*
UpdateSessionDescription change the description of a session.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to update
	@param description *string - the new description, or nil to clear it
	@returns goutils.NotFoundError if the session is unknown
*/
func (c SessionManagerCore) UpdateSessionDescription(
	ctx context.Context, sessionName string, description *string,
) error {
	return c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.UpdateSessionDescription(ctx, sessionName, description)
		},
	)
}

/*
DeleteSession delete a session. Only permitted on IDLE sessions.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to delete
	@returns goutils.NotFoundError if the session is unknown, goutils.ConsistencyError if the
		session is not in a state allowing deletion
*/
func (c SessionManagerCore) DeleteSession(ctx context.Context, sessionName string) error {
	return c.persistence.UseDatabaseInTransaction(
		ctx, func(ctx context.Context, dbClient db.Database) error {
			return dbClient.DeleteSession(ctx, sessionName)
		},
	)
}

/*
StartSession bring up a session runner for an existing session and start it, applied through
the session manager.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to start
	@param blocking bool - whether to wait for the start to complete
	@returns goutils.NotFoundError if the session is unknown, goutils.ConsistencyError if the
		session is not in a state allowing start
*/
func (c SessionManagerCore) StartSession(
	ctx context.Context, sessionName string, blocking bool,
) error {
	return c.manager.StartSession(ctx, sessionName, blocking)
}

/*
StopSession bring a session back to IDLE and unload its runner, applied through the session
manager.

	@param ctx context.Context - execution context
	@param sessionName string - the name of the session to stop
	@param blocking bool - whether to wait for the stop to complete
	@returns goutils.NotFoundError if the session is unknown, goutils.ConsistencyError if the
		session is not in a state allowing stop
*/
func (c SessionManagerCore) StopSession(
	ctx context.Context, sessionName string, blocking bool,
) error {
	return c.manager.StopSession(ctx, sessionName, blocking)
}
