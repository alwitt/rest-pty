// Package app - application entry points
package app //revive:disable-line:var-naming

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/api"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/redis"
	"github.com/alwitt/rest-pty/session"
	"github.com/apex/log"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm/logger"
)

// Server REST PTY server
type Server interface {
	/*
		Start the server and its components

			@param ctx context.Context - execution context
			@param serverErrors chan error - channel used to broadcast a fatal runtime
				failure (e.g. one of the HTTP servers failing on ListenAndServe) back to
				the caller so it can trigger shutdown
	*/
	Start(ctx context.Context, serverErrors chan error) error

	/*
		Stop shutdown the server and its components

			@param ctx context.Context - execution context
	*/
	Stop(ctx context.Context) error
}

// serverImpl implement Server
type serverImpl struct {
	goutils.Component

	// parentCtx parent execution context for all running tasks
	parentCtx context.Context

	// mainServer primary API server
	mainServer *http.Server

	// metricsServer metrics server
	metricsServer *http.Server

	// manager session manager
	manager session.Manager

	wg sync.WaitGroup
}

/*
Start the server and its components

	@param ctx context.Context - execution context
	@param serverErrors chan error - channel used to broadcast a fatal runtime
		failure (e.g. one of the HTTP servers failing on ListenAndServe) back to
		the caller so it can trigger shutdown
*/
func (s *serverImpl) Start(ctx context.Context, serverErrors chan error) error {
	// Start the manager
	if err := s.manager.Start(ctx); err != nil {
		return models.BootStrapError{Core: err, Message: "Failed to start session manager"}
	}

	// Start API server
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		logTags := s.GetLogTagsForContext(s.parentCtx)
		if err := s.mainServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).WithFields(logTags).Error("REST API Server failure")
			serverErrors <- err
		}
	}()

	// Start Metrics server
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		logTags := s.GetLogTagsForContext(s.parentCtx)
		if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).WithFields(logTags).Error("Metrics API Server failure")
			serverErrors <- err
		}
	}()

	return nil
}

/*
Stop shutdown the server and its components

	@param ctx context.Context - execution context
*/
func (s *serverImpl) Stop(ctx context.Context) error {
	lclCtx, lclCtxCancel := context.WithTimeout(ctx, time.Second*15)
	defer lclCtxCancel()

	// Stop the session manager
	if err := s.manager.Stop(lclCtx); err != nil {
		return models.ShutdownError{Core: err, Message: "Failed to stop session manager"}
	}

	// Gracefully stop the HTTP servers so their ListenAndServe goroutines return. Attempt
	// both even if the first fails so neither server is left running.
	mainErr := s.mainServer.Shutdown(lclCtx)
	metricsErr := s.metricsServer.Shutdown(lclCtx)
	if mainErr != nil {
		return models.ShutdownError{Core: mainErr, Message: "Failed to stop REST API server"}
	}
	if metricsErr != nil {
		return models.ShutdownError{Core: metricsErr, Message: "Failed to stop metrics server"}
	}

	// Wait for all threads to stop
	if err := goutils.TimeBoundedWaitGroupWait(lclCtx, &s.wg, time.Second*5); err != nil {
		return models.ShutdownError{Core: err, Message: "Daemon tasks did not stop in time"}
	}

	return nil
}

/*
BuildNewServer build new REST PTY server

	@param parentCtx context.Context - parent execution context for all running tasks
	@param configs models.ApplicationConfig - server config
	@returns new server
*/
func BuildNewServer(
	parentCtx context.Context, configs models.ApplicationConfig,
) (Server, error) {
	// ------------------------------------------------------------------------------------
	// Build persistence client

	// Prepare redis client
	redisClient, err := redis.NewClient(parentCtx, configs.Redis)
	if err != nil {
		return nil, models.BootStrapError{Core: err, Message: "Failed to define REDIS client"}
	}

	// Prepare persistence
	persistence, err := db.NewConnection(db.GetSqliteDialector(configs.SQLite.DBFile), logger.Error)
	if err != nil {
		return nil, models.BootStrapError{Core: err, Message: "Failed to prepare DB persistence client"}
	}

	// Setup the SQLite DB file
	if err := persistence.RunSQLInTransaction(parentCtx, db.DefineTables); err != nil {
		return nil, models.BootStrapError{Core: err, Message: "DB migration failed"}
	}

	// ------------------------------------------------------------------------------------
	// Build core

	// Prepare session manager
	sessionManager, err := session.NewSessionManager(parentCtx, session.NewSessionManagerParams{
		InstanceName: ulid.Make().String(),
		PersistenceFactory: func() (db.Client, error) {
			return db.NewConnection(db.GetSqliteDialector(configs.SQLite.DBFile), logger.Error)
		},
		RedisClient:   redisClient,
		DriverFactory: session.NewDriver,
		WorkerFactory: goutils.GetNewTaskProcessorInstance,
		RunnerFactory: session.NewSessionRunner,
	})
	if err != nil {
		return nil, models.BootStrapError{Core: err, Message: "Failed to create session manager"}
	}

	// ------------------------------------------------------------------------------------
	// Build metrics server

	metricsCollector, err := goutils.GetNewMetricsCollector(
		log.Fields{"module": "utils", "component": "metrics-core"}, []goutils.LogMetadataModifier{},
	)
	if err != nil {
		return nil, models.BootStrapError{Core: err, Message: "Failed to create metrics collector"}
	}

	if configs.Metrics.Features.EnableAppMetrics {
		metricsCollector.InstallApplicationMetrics()
	}

	var httpMetricsAgent goutils.HTTPRequestMetricHelper
	if configs.Metrics.Features.EnableHTTPMetrics {
		httpMetricsAgent = metricsCollector.InstallHTTPMetrics()
	}

	// Build metrics hosting server
	metricsServer := api.BuildMetricsCollectionServer(
		configs.Metrics.Server,
		metricsCollector,
		configs.Metrics.MetricsEndpoint,
		configs.Metrics.MaxRequests,
	)

	// ------------------------------------------------------------------------------------
	// Build API server

	apiServer, err := api.BuildHTTPServer(
		configs.API, persistence, redisClient, sessionManager, httpMetricsAgent,
	)
	if err != nil {
		return nil, models.BootStrapError{Core: err, Message: "Failed to create API server"}
	}

	return &serverImpl{
		Component: goutils.Component{
			LogTags: log.Fields{"module": "main", "component": "server"},
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		parentCtx:     parentCtx,
		mainServer:    apiServer,
		metricsServer: metricsServer,
		manager:       sessionManager,
		wg:            sync.WaitGroup{},
	}, nil
}
