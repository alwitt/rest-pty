package db

import (
	"context"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"
)

// CommonListEntryQueryFilter common query filter when listing data entries
type CommonListEntryQueryFilter struct {
	Limit  *int `validate:"omitempty,gte=1"`
	Offset *int `validate:"omitempty,gte=0"`
}

// SessionQueryFilter list session query filter
type SessionQueryFilter struct {
	CommonListEntryQueryFilter
	// SimilarName filter for session whose name is similar to this, case insensitive.
	SimilarName *string
	// TargetDriverType fetch session using this driver type
	TargetDriverType []models.SessionDriverTypeENUMType `validate:"omitempty,dive,session_driver_type"`
	// TargetStates fetch session in this state
	TargetStates []models.SessionStateENUMType `validate:"omitempty,dive,session_state_type"`
	// OrderByName whether to order the returns by name
	OrderByName bool
	// OrderDirection ordering direction `asc` or `desc`
	OrderDirection *string `validate:"omitempty,oneof=asc desc ASC DESC"`
}

// Database the database handle to interacting with the data base
type Database interface {
	/*
		DefineNewSession define a new session

			@param ctx context.Context - execution context
			@param name string - session name, can only contain alphanumeric characters and -
			@param description *string - session description
			@param command models.SessionCommand - the command to execute
			@param outputBufferCapacity int64 - buffering capacity for holding command output history
			@param driverParams interface{} - session driver parameters, allowed types are:
			    * SessionDriverPTYParams
			@returns new session entry
			@returns `models.ValidationError` bad data
			@returns `models.PersistenceError` persistence layer failure
	*/
	DefineNewSession(
		ctx context.Context,
		name string,
		description *string,
		command models.SessionCommand,
		outputBufferCapacity int64,
		driverParams interface{},
	) (models.Session, error)

	/*
		GetSession fetch a session by name

			@param ctx context.Context - execution context
			@param name string - session name
			@returns session entry
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.PersistenceError` persistence layer failure
	*/
	GetSessionByName(ctx context.Context, name string) (models.Session, error)

	/*
		MarkSessionIdle mark a session is IDLE

			@param ctx context.Context - execution context
			@param name string - session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` state transition is not acceptable
			@returns `models.PersistenceError` persistence layer failure
	*/
	MarkSessionIdle(ctx context.Context, name string) error

	/*
		MarkSessionStarting mark a session is STARTING

			@param ctx context.Context - execution context
			@param name string - session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` state transition is not acceptable
			@returns `models.PersistenceError` persistence layer failure
	*/
	MarkSessionStarting(ctx context.Context, name string) error

	/*
		MarkSessionReady mark a session is Ready

			@param ctx context.Context - execution context
			@param name string - session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` state transition is not acceptable
			@returns `models.PersistenceError` persistence layer failure
	*/
	MarkSessionReady(ctx context.Context, name string) error

	/*
		MarkSessionClaimed mark a session is CLAIMED

			@param ctx context.Context - execution context
			@param name string - session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` state transition is not acceptable
			@returns `models.PersistenceError` persistence layer failure
	*/
	MarkSessionClaimed(ctx context.Context, name string) error

	/*
		MarkSessionStopping mark a session is STOPPING

			@param ctx context.Context - execution context
			@param name string - session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` state transition is not acceptable
			@returns `models.PersistenceError` persistence layer failure
	*/
	MarkSessionStopping(ctx context.Context, name string) error

	/*
		UpdateSessionOutputBufCapacity change the output buffer capacity of a session.

		This can only be performed on IDLE sessions.

			@param ctx context.Context - execution context
			@param name string - session name
			@param newCap int64 - new output buffer capacity
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` session in wrong state
			@returns `models.PersistenceError` persistence layer failure
	*/
	UpdateSessionOutputBufCapacity(ctx context.Context, name string, newCap int64) error

	/*
		UpdateSessionCommand change the session command

		This can only be performed on IDLE sessions.

			@param ctx context.Context - execution context
			@param name string - session name
			@param newCommand models.SessionCommand - new session command
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` session in wrong state
			@returns `models.PersistenceError` persistence layer failure
	*/
	UpdateSessionCommand(ctx context.Context, name string, newCommand models.SessionCommand) error

	/*
		UpdateSessionDriver change the session driver parameters

		This can only be performed on IDLE sessions.

			@param ctx context.Context - execution context
			@param name string - session name
			@param driverParams interface{} - new session driver parameters
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.ConsistencyError` session in wrong state
			@returns `models.ValidationError` new driver parameters are not valid
			@returns `models.PersistenceError` persistence layer failure
	*/
	UpdateSessionDriver(ctx context.Context, name string, driverParams interface{}) error

	/*
		UpdateSessionName change session name

			@param ctx context.Context - execution context
			@param name string - session name
			@param newName string - new session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.PersistenceError` persistence layer failure
	*/
	UpdateSessionName(ctx context.Context, name string, newName string) error

	/*
		UpdateSessionDescription change session description

			@param ctx context.Context - execution context
			@param name string - session name
			@param newDescription *string - new session description
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.PersistenceError` persistence layer failure
	*/
	UpdateSessionDescription(ctx context.Context, name string, newDescription *string) error

	/*
		DeleteSession delete a session

		This can only be performed on IDLE sessions.

			@param ctx context.Context - execution context
			@param name string - session name
			@returns `models.UnknownSessionError` if session is unknown
			@returns `models.PersistenceError` persistence layer failure
	*/
	DeleteSession(ctx context.Context, name string) error

	/*
		ListSessions list sessions

			@param ctx context.Context - execution context
			@param filter SessionQueryFilter - query filter
			@returns list of session according to the query filter
			@returns `models.PersistenceError` persistence layer failure
	*/
	ListSessions(ctx context.Context, filters SessionQueryFilter) ([]models.Session, error)
}

// databaseImpl implements Database
type databaseImpl struct {
	goutils.Component
	db        *gorm.DB
	validator *validator.Validate
}

// newDatabase define a new database client
func newDatabase(
	_ context.Context, sqlClient *gorm.DB,
) (Database, error) {
	logTags := log.Fields{"module": "db", "component": "db-client"}

	instance := &databaseImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		db:        sqlClient,
		validator: validator.New(),
	}

	if err := models.RegisterWithValidator(instance.validator); err != nil {
		return nil, models.RuntimeError{
			Core: err, Message: "failed to install custom validation macros",
		}
	}

	return instance, nil
}
