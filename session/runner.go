package session

import (
	"context"
	"sync"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/redis"
	"github.com/apex/log"
	"github.com/go-playground/validator/v10"
	"github.com/oklog/ulid/v2"
)

// Runner entity responsible for operating a PTY session
type Runner interface{}

// runnerImpl implements Runner
type runnerImpl struct {
	goutils.Component

	workingCtx       context.Context
	workingCtxCancel context.CancelFunc

	wg sync.WaitGroup

	persistence        db.Client
	persistenceFactory func() (db.Client, error)

	redisFactory func() (redis.Client, error)

	session models.Session

	validator *validator.Validate
}

/*
NewSessionRunner define a new runner to operate a particular PTY session

	@param parentCtx context.Context - parent context for the runner
	@param sessionName string - name of session to operate
	@param persistenceFactory func() (db.Client, error) - factory function to generate
	    prepare new persistence clients
	@param redisFactory func() (redis.Client, error) - factory function to generate
	    prepare new REDIS clients
	@returns new runner
	@returns `models.RuntimeError` sub-component initialization failed
	@returns `models.UnknownSessionError` referenced session is unknown
	@returns `models.PersistenceError` persistence layer failure
*/
func NewSessionRunner(
	parentCtx context.Context,
	sessionName string,
	persistenceFactory func() (db.Client, error),
	redisFactory func() (redis.Client, error),
) (Runner, error) {
	persistence, err := persistenceFactory()
	if err != nil {
		return nil, models.RuntimeError{
			Core: err, Message: "failed to prepare scoped persistence for PTY runner",
		}
	}

	var session models.Session
	if dbErr := persistence.UseDatabaseInTransaction(
		parentCtx, func(dbCtx context.Context, dbClient db.Database) error {
			var err error
			session, err = dbClient.GetSessionByName(dbCtx, sessionName)
			return err
		},
	); dbErr != nil {
		return nil, dbErr
	}

	logTags := log.Fields{
		"module":       "session",
		"component":    "session-runner",
		"session":      session.ID,
		"session-name": session.Name,
		"instance":     ulid.Make().String(),
	}

	instance := &runnerImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		persistence:        persistence,
		persistenceFactory: persistenceFactory,
		redisFactory:       redisFactory,
		session:            session,
		validator:          validator.New(),
		wg:                 sync.WaitGroup{},
	}
	instance.workingCtx, instance.workingCtxCancel = context.WithCancel(parentCtx)

	if err := models.RegisterWithValidator(instance.validator); err != nil {
		return nil, models.RuntimeError{
			Core: err, Message: "failed to install custom validation macros",
		}
	}

	return instance, nil
}

/*
Start operating the PTY session
*/
func (r *runnerImpl) Start(_ context.Context) error {
	return nil
}
