// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"fmt"
	"net/http"
	"time"

	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
	"github.com/alwitt/rest-pty/db"
	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/session"
	"github.com/gorilla/mux"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/cors"
)

/*
BuildMetricsCollectionServer create server to host metrics collection endpoint

	@param httpCfg common.HTTPServerConfig - HTTP server configuration
	@param metricsCollector goutils.MetricsCollector - metrics collector
	@param collectionEndpoint string - endpoint to expose the metrics on
	@param maxRESTRequests int - max number fo parallel requests to support
	@returns HTTP server instance
*/
func BuildMetricsCollectionServer(
	httpCfg models.HTTPServerConfig,
	metricsCollector goutils.MetricsCollector,
	collectionEndpoint string,
	maxRESTRequests int,
) *http.Server {
	router := mux.NewRouter()
	metricsCollector.ExposeCollectionEndpoint(router, collectionEndpoint, maxRESTRequests)

	serverListen := fmt.Sprintf(
		"%s:%d", httpCfg.ListenOn, httpCfg.Port,
	)
	httpSrv := &http.Server{
		Addr:         serverListen,
		WriteTimeout: time.Second * time.Duration(httpCfg.Timeouts.WriteTimeout),
		ReadTimeout:  time.Second * time.Duration(httpCfg.Timeouts.ReadTimeout),
		IdleTimeout:  time.Second * time.Duration(httpCfg.Timeouts.IdleTimeout),
		Handler:      router,
	}
	httpSrv.Protocols = new(http.Protocols)
	httpSrv.Protocols.SetHTTP1(true)
	httpSrv.Protocols.SetUnencryptedHTTP2(true)

	return httpSrv
}

/*
BuildHTTPServer create server to host session CRUD and IO endpoints

	@param httpCfg models.APIServerConfig - HTTP API server configuration
	@param persistence db.Client - DB persistence layer
	@param redisClient goutilsRedis.Client - redis client
	@param manager session.Manager - session manager
	@param metrics goutils.HTTPRequestMetricHelper - metric collection agent
	@returns HTTP server instance
*/
func BuildHTTPServer(
	httpCfg models.APIServerConfig,
	persistence db.Client,
	redisClient goutilsRedis.Client,
	manager session.Manager,
	metrics goutils.HTTPRequestMetricHelper,
) (*http.Server, error) {
	livenessAPI := NewLivenessHandler(persistence, httpCfg.APIs.RequestLogging, metrics)

	managerAPI, err := NewSessionManagerHandler(
		persistence, manager, httpCfg.APIs.RequestLogging, metrics,
	)
	if err != nil {
		return nil, goutils.NewRuntimeError("Failed to define management API handler", err, true)
	}

	ioAPI, err := NewSessionIOHandler(persistence, redisClient, httpCfg.APIs.RequestLogging, metrics)
	if err != nil {
		return nil, goutils.NewRuntimeError("Failed to define session IO API handler", err, true)
	}

	router := mux.NewRouter()
	mainRouter := registerPathPrefix(router, httpCfg.APIs.Endpoint.PathPrefix, nil)
	livenessRouter := registerPathPrefix(mainRouter, "/liveness", nil)
	v1Router := registerPathPrefix(mainRouter, "/v1", nil)

	// --------------------------------------------------------------------------------
	// Health check

	_ = registerPathPrefix(livenessRouter, "/alive", map[string]http.HandlerFunc{
		"get": livenessAPI.Alive,
	})
	_ = registerPathPrefix(livenessRouter, "/ready", map[string]http.HandlerFunc{
		"get": livenessAPI.Ready,
	})

	// --------------------------------------------------------------------------------
	// Session

	sessionsRouter := registerPathPrefix(v1Router, "/sessions", map[string]http.HandlerFunc{
		"post": managerAPI.DefineNewSession,
		"get":  managerAPI.ListSessions,
	})

	perSessionRouter := registerPathPrefix(
		sessionsRouter, "/{sessionName}", map[string]http.HandlerFunc{
			"get":    managerAPI.GetSession,
			"delete": managerAPI.DeleteSession,
		},
	)

	// Basic attribute update
	_ = registerPathPrefix(perSessionRouter, "/output-buf-cap", map[string]http.HandlerFunc{
		"put": managerAPI.UpdateSessionOutputBufCapacity,
	})
	_ = registerPathPrefix(perSessionRouter, "/run-mode", map[string]http.HandlerFunc{
		"put": managerAPI.UpdateSessionRunMode,
	})
	_ = registerPathPrefix(perSessionRouter, "/command", map[string]http.HandlerFunc{
		"put": managerAPI.UpdateSessionCommand,
	})
	_ = registerPathPrefix(perSessionRouter, "/driver", map[string]http.HandlerFunc{
		"put": managerAPI.UpdateSessionDriver,
	})
	_ = registerPathPrefix(perSessionRouter, "/name", map[string]http.HandlerFunc{
		"put": managerAPI.UpdateSessionName,
	})
	_ = registerPathPrefix(perSessionRouter, "/description", map[string]http.HandlerFunc{
		"put": managerAPI.UpdateSessionDescription,
	})

	// Life cycle management
	_ = registerPathPrefix(perSessionRouter, "/start", map[string]http.HandlerFunc{
		"post": managerAPI.StartSession,
	})
	_ = registerPathPrefix(perSessionRouter, "/stop", map[string]http.HandlerFunc{
		"post": managerAPI.StopSession,
	})

	perSessionIORouter := registerPathPrefix(perSessionRouter, "/io", nil)
	perSessionInputRouter := registerPathPrefix(perSessionIORouter, "/input", nil)
	perSessionOutputRouter := registerPathPrefix(perSessionIORouter, "/output", nil)

	// Session IO
	_ = registerPathPrefix(perSessionInputRouter, "/commands", map[string]http.HandlerFunc{
		"post": ioAPI.SubmitUserCommandToSession,
	})
	_ = registerPathPrefix(perSessionOutputRouter, "/chunk", map[string]http.HandlerFunc{
		"get": ioAPI.ReadSessionOutputChunk,
	})
	_ = registerPathPrefix(perSessionOutputRouter, "/newest", map[string]http.HandlerFunc{
		"get": ioAPI.ReadSessionOutputNewest,
	})
	_ = registerPathPrefix(perSessionOutputRouter, "/tail", map[string]http.HandlerFunc{
		"get": ioAPI.TailSessionOutput,
	})

	// --------------------------------------------------------------------------------
	// MCP Endpoint

	if httpCfg.APIs.EnableMCP {
		// Build up the MCP end point handler
		mcpAPI, err := NewSessionMCPHandler(
			persistence, manager, redisClient, httpCfg.APIs.RequestLogging,
		)
		if err != nil {
			return nil, goutils.NewRuntimeError("Failed to define MCP API handler", err, true)
		}

		// Build MCP server
		mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rest-pty", Version: "0.2.0"}, nil)
		if err := mcpAPI.RegisterTools(mcpServer); err != nil {
			return nil, goutils.NewRuntimeError("Failed to register MCP tools", err, true)
		}

		// Install logging middleware
		mcpServer.AddReceivingMiddleware(mcpAPI.LoggingMiddleware)

		handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
			return mcpServer
		}, &mcp.StreamableHTTPOptions{Stateless: true})

		// Install MCP server
		_ = v1Router.Path("/mcp").Handler(handler)
	}

	// --------------------------------------------------------------------------------
	// Middleware

	v1Router.Use(func(next http.Handler) http.Handler {
		return managerAPI.LoggingMiddleware(managerAPI.RequestPayloadDumpMiddleware(next.ServeHTTP))
	})
	livenessRouter.Use(func(next http.Handler) http.Handler {
		return livenessAPI.LoggingMiddleware(next.ServeHTTP)
	})

	// CORS middleware
	corsWrapper := cors.New(cors.Options{
		AllowedOrigins:      []string{"*"},
		AllowedMethods:      []string{"POST", "GET", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:      []string{"*"},
		ExposedHeaders:      []string{httpCfg.APIs.RequestLogging.RequestIDHeader},
		AllowPrivateNetwork: true,
	})

	// --------------------------------------------------------------------------------
	// HTTP Server

	serverListen := fmt.Sprintf(
		"%s:%d", httpCfg.Server.ListenOn, httpCfg.Server.Port,
	)
	httpSrv := &http.Server{
		Addr:         serverListen,
		WriteTimeout: time.Second * time.Duration(httpCfg.Server.Timeouts.WriteTimeout),
		ReadTimeout:  time.Second * time.Duration(httpCfg.Server.Timeouts.ReadTimeout),
		IdleTimeout:  time.Second * time.Duration(httpCfg.Server.Timeouts.IdleTimeout),
		Handler:      corsWrapper.Handler(router),
	}
	httpSrv.Protocols = new(http.Protocols)
	httpSrv.Protocols.SetHTTP1(true)
	httpSrv.Protocols.SetUnencryptedHTTP2(true)

	return httpSrv, nil
}
