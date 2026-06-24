package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/oklog/ulid/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

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
	@returns `goutils.ValidationError` bad data
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) DefineNewSession(
	_ context.Context,
	name string,
	description *string,
	command models.SessionCommand,
	outputBufferCapacity int64,
	driverParams interface{},
) (models.Session, error) {
	var driverType models.SessionDriverTypeENUMType
	switch driverParams.(type) {
	case models.SessionDriverPTYParams:
		driverType = models.SessionDriverTypePTY
	case models.SessionDriverDockerParams:
		driverType = models.SessionDriverTypeDocker
	default:
		return models.Session{}, goutils.NewValidationError(
			"unsupported session driver metadata type "+reflect.TypeOf(driverParams).String(), nil, true,
		)
	}

	if err := d.validator.Struct(driverParams); err != nil {
		return models.Session{},
			goutils.NewValidationError("new session driver parameters are invalid", err, true)
	}

	driverMetadataStr, _ := json.Marshal(&driverParams)

	newEntry := sessionEntry{
		Session: models.Session{
			ID:                   ulid.Make().String(),
			Name:                 name,
			Description:          description,
			Command:              command,
			State:                models.SessionStateIdle,
			OutputBufferCapacity: outputBufferCapacity,
			DriverType:           driverType,
			DriverMetadata:       datatypes.JSON(driverMetadataStr),
			RunnerMode:           models.SessionRunnerModeTypeCommanded,
		},
	}

	if err := d.validator.Struct(&newEntry); err != nil {
		return models.Session{},
			goutils.NewValidationError("new session entry attributes are invalid", err, true)
	}

	if tmp := d.db.Create(&newEntry); tmp.Error != nil {
		return models.Session{},
			models.NewPersistenceError("failed to define new session entry", tmp.Error, true)
	}

	return newEntry.Session, nil
}

// getSessionEntryByName helper function to get a session by name
func (d *databaseImpl) getSessionEntryByName(name string) (sessionEntry, error) {
	var entry sessionEntry
	tmp := d.db.Model(&sessionEntry{}).Where("name = ?", name).First(&entry)

	if tmp.Error != nil {
		if errors.Is(tmp.Error, gorm.ErrRecordNotFound) {
			return sessionEntry{},
				goutils.NewNotFoundError("session '"+name+"' is unknown", tmp.Error, true)
		}
		return sessionEntry{},
			models.NewPersistenceError("failed to fetch session '"+name+"'", tmp.Error, true)
	}

	return entry, nil
}

/*
GetSession fetch a session by name

	@param ctx context.Context - execution context
	@param name string - session name
	@returns session entry
	@returns `models.UnknownSessionError` if session is unknown
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) GetSessionByName(_ context.Context, name string) (models.Session, error) {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return models.Session{}, err
	}
	return entry.Session, nil
}

// updateSessionState update session state
func (d *databaseImpl) updateSessionState(
	name string, newState models.SessionStateENUMType,
) error {
	session, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	if err := session.ValidNextState(newState); err != nil {
		return goutils.NewConsistencyError(
			"can't change session "+session.Name+" state", err, true,
		)
	}

	tmp := d.db.Model(&sessionEntry{}).Where("id = ?", session.ID).Update("state", newState)
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+session.Name+"'["+session.ID+"] new state", tmp.Error, true,
		)
	}

	return nil
}

/*
MarkSessionIdle mark a session is IDLE

	@param ctx context.Context - execution context
	@param name string - session name
	@returns `models.UnknownSessionError` if session is unknown
	@returns `goutils.ConsistencyError` state transition is not acceptable
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) MarkSessionIdle(_ context.Context, name string) error {
	return d.updateSessionState(name, models.SessionStateIdle)
}

/*
MarkSessionReady mark a session is Ready

	@param ctx context.Context - execution context
	@param name string - session name
	@returns `models.UnknownSessionError` if session is unknown
	@returns `goutils.ConsistencyError` state transition is not acceptable
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) MarkSessionReady(_ context.Context, name string) error {
	return d.updateSessionState(name, models.SessionStateReady)
}

/*
UpdateSessionOutputBufCapacity change the output buffer capacity of a session.

This can only be performed on IDLE sessions.

	@param ctx context.Context - execution context
	@param name string - session name
	@param newCap int64 - new output buffer capacity
	@returns `models.UnknownSessionError` if session is unknown
	@returns `goutils.ConsistencyError` session in wrong state
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) UpdateSessionOutputBufCapacity(
	_ context.Context, name string, newCap int64,
) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	if entry.State != models.SessionStateIdle {
		return goutils.NewConsistencyError(
			"can't change session '"+name+"' output buffer capacity outside of IDLE state", nil, true,
		)
	}

	entry.OutputBufferCapacity = newCap
	if err := d.validator.Struct(&entry); err != nil {
		return goutils.NewValidationError("new session "+name+" capacity is invalid", err, true)
	}

	tmp := d.db.Model(&sessionEntry{}).Where("id = ?", entry.ID).Update("io_buf_cap", newCap)
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+entry.Name+"'["+entry.ID+"] new IO capacity", tmp.Error, true,
		)
	}

	return nil
}

/*
UpdateSessionRunMode change the runner mode of a session

This can only be performed on IDLE sessions.

	@param ctx context.Context - execution context
	@param name string - session name
	@param newMode models.SessionRunnerModeTypeENUMType - new runner mode
	@returns `models.UnknownSessionError` if session is unknown
	@returns `goutils.ConsistencyError` session in wrong state
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) UpdateSessionRunMode(
	_ context.Context, name string, newMode models.SessionRunnerModeTypeENUMType,
) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	if entry.State != models.SessionStateIdle {
		return goutils.NewConsistencyError(
			"can't change session '"+name+"' runner mode outside of IDLE state", nil, true,
		)
	}

	entry.RunnerMode = newMode
	if err := d.validator.Struct(&entry); err != nil {
		return goutils.NewValidationError("new session "+name+" runner mode is invalid", err, true)
	}

	tmp := d.db.Model(&sessionEntry{}).Where("id = ?", entry.ID).Update("runner_mode", newMode)
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+entry.Name+"'["+entry.ID+"] new runner mode", tmp.Error, true,
		)
	}

	return nil
}

/*
UpdateSessionCommand change the session command

This can only be performed on IDLE sessions.

	@param ctx context.Context - execution context
	@param name string - session name
	@param newCommand models.SessionCommand - new session command
	@returns `models.UnknownSessionError` if session is unknown
	@returns `goutils.ConsistencyError` session in wrong state
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) UpdateSessionCommand(
	_ context.Context, name string, newCommand models.SessionCommand,
) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	if entry.State != models.SessionStateIdle {
		return goutils.NewConsistencyError(
			"can't change session '"+name+"' command outside of IDLE state", nil, true,
		)
	}

	entry.Command = newCommand
	if err := d.validator.Struct(&entry); err != nil {
		return goutils.NewValidationError(" new session "+name+" command is invalid", err, true)
	}

	tmp := d.db.Model(&sessionEntry{}).Where("id = ?", entry.ID).Update("command", newCommand)
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+entry.Name+"'["+entry.ID+"] new command", tmp.Error, true,
		)
	}

	return nil
}

/*
UpdateSessionDriver change the session driver parameters

This can only be performed on IDLE sessions.

	@param ctx context.Context - execution context
	@param name string - session name
	@param driverParams interface{} - new session driver parameters
	@returns `models.UnknownSessionError` if session is unknown
	@returns `goutils.ConsistencyError` session in wrong state
	@returns `goutils.ValidationError` new driver parameters are not valid
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) UpdateSessionDriver(
	_ context.Context, name string, driverParams interface{},
) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	if entry.State != models.SessionStateIdle {
		return goutils.NewConsistencyError(
			"can't change session '"+name+"' driver outside of IDLE state", nil, true,
		)
	}

	var driverType models.SessionDriverTypeENUMType
	switch driverParams.(type) {
	case models.SessionDriverPTYParams:
		driverType = models.SessionDriverTypePTY
	case models.SessionDriverDockerParams:
		driverType = models.SessionDriverTypeDocker
	default:
		return goutils.NewValidationError(
			"unsupported session driver metadata type "+reflect.TypeOf(driverParams).String(), nil, true,
		)
	}

	if err := d.validator.Struct(driverParams); err != nil {
		return goutils.NewValidationError("new session driver parameters are invalid", err, true)
	}

	driverMetadataStr, _ := json.Marshal(&driverParams)

	entry.DriverType = driverType
	entry.DriverMetadata = datatypes.JSON(driverMetadataStr)
	if err := d.validator.Struct(&entry); err != nil {
		return goutils.NewValidationError(" new session "+name+" driver params is invalid", err, true)
	}

	tmp := d.db.Model(&sessionEntry{}).
		Where("id = ?", entry.ID).
		Update("driver", driverType).
		Update("driver_metadata", datatypes.JSON(driverMetadataStr))
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+entry.Name+"'["+entry.ID+"] new driver params", tmp.Error, true,
		)
	}

	return nil
}

/*
UpdateSessionName change session name

	@param ctx context.Context - execution context
	@param name string - session name
	@param newName string - new session name
	@returns `models.UnknownSessionError` if session is unknown
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) UpdateSessionName(_ context.Context, name string, newName string) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	entry.Name = newName
	if err := d.validator.Struct(&entry); err != nil {
		return goutils.NewValidationError("new session name is invalid", err, true)
	}

	tmp := d.db.Model(&sessionEntry{}).Where("id = ?", entry.ID).Update("name", newName)
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+entry.Name+"'["+entry.ID+"] new name", tmp.Error, true,
		)
	}

	return nil
}

/*
UpdateSessionDescription change session description

	@param ctx context.Context - execution context
	@param name string - session name
	@param newDescription *string - new session description
	@returns `models.UnknownSessionError` if session is unknown
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) UpdateSessionDescription(
	_ context.Context, name string, newDescription *string,
) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	tmp := d.db.
		Model(&sessionEntry{}).
		Where("id = ?", entry.ID).
		Update("description", newDescription)
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to record session '"+entry.Name+"'["+entry.ID+"] new description", tmp.Error, true,
		)
	}

	return nil
}

/*
DeleteSession delete a session

	@param ctx context.Context - execution context
	@param name string - session name
	@returns `models.UnknownSessionError` if session is unknown
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) DeleteSession(_ context.Context, name string) error {
	entry, err := d.getSessionEntryByName(name)
	if err != nil {
		return err
	}

	if entry.State != models.SessionStateIdle {
		return goutils.NewConsistencyError(
			"can't delete session '"+name+"' outside of IDLE state", nil, true,
		)
	}

	tmp := d.db.Model(&sessionEntry{}).Where("id = ?", entry.ID).Delete(&sessionEntry{})
	if tmp.Error != nil {
		return models.NewPersistenceError(
			"failed to delete session '"+entry.Name+"'["+entry.ID+"]", tmp.Error, true,
		)
	}

	return nil
}

/*
ListSessions list sessions

	@param ctx context.Context - execution context
	@param filter SessionQueryFilter - query filter
	@returns list of session according to the query filter
	@returns `models.PersistenceError` persistence layer failure
*/
func (d *databaseImpl) ListSessions(
	_ context.Context, filters SessionQueryFilter,
) ([]models.Session, error) {
	if err := d.validator.Struct(&filters); err != nil {
		return nil, goutils.NewValidationError("session query filter is invalid", err, true)
	}

	query := d.db.Model(&sessionEntry{})

	if filters.SimilarName != nil {
		query = query.Where("name like ?", "%"+*filters.SimilarName+"%")
	}

	if len(filters.TargetStates) > 0 {
		query = query.Where("state in ?", filters.TargetStates)
	}

	if filters.Limit != nil {
		query = query.Limit(*filters.Limit)
	}
	if filters.Offset != nil {
		query = query.Offset(*filters.Offset)
	}

	orderDirection := "asc"
	if filters.OrderDirection != nil {
		orderDirection = *filters.OrderDirection
	}

	if filters.OrderByName {
		query = query.Order("name " + orderDirection)
	} else {
		query = query.Order("created_at " + orderDirection)
	}

	var entries []sessionEntry
	if tmp := query.Find(&entries); tmp.Error != nil {
		return nil, models.NewPersistenceError("failed to list sessions", tmp.Error, true)
	}

	results := make([]models.Session, 0, len(entries))
	for _, entry := range entries {
		results = append(results, entry.Session)
	}

	return results, nil
}
