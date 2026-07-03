// Package models - application data models
package models

import (
	"github.com/alwitt/goutils"
	"github.com/spf13/viper"
)

// ======================================================================================
// HTTP

// HTTPServerTimeoutConfig defines the timeout settings for HTTP server
type HTTPServerTimeoutConfig struct {
	// ReadTimeout is the maximum duration for reading the entire
	// request, including the body in seconds. A zero or negative
	// value means there will be no timeout.
	ReadTimeout int `mapstructure:"read" json:"read" validate:"gte=0"`
	// WriteTimeout is the maximum duration before timing out
	// writes of the response in seconds. A zero or negative value
	// means there will be no timeout.
	WriteTimeout int `mapstructure:"write" json:"write" validate:"gte=0"`
	// IdleTimeout is the maximum amount of time to wait for the
	// next request when keep-alives are enabled in seconds. If
	// IdleTimeout is zero, the value of ReadTimeout is used. If
	// both are zero, there is no timeout.
	IdleTimeout int `mapstructure:"idle" json:"idle" validate:"gte=0"`
}

// HTTPServerConfig defines the HTTP server parameters
type HTTPServerConfig struct {
	// ListenOn is the interface the HTTP server will listen on
	ListenOn string `mapstructure:"listenOn" json:"listenOn" validate:"required,ip"`
	// Port is the port the HTTP server will listen on
	Port uint16 `mapstructure:"appPort" json:"appPort" validate:"required,gt=0,lt=65536"`
	// Timeouts sets the HTTP timeout settings
	Timeouts HTTPServerTimeoutConfig `mapstructure:"timeoutSecs" json:"timeoutSecs" validate:"required"`
}

// HTTPRequestLogging defines HTTP request logging parameters
type HTTPRequestLogging struct {
	// LogLevel output request logs at this level
	LogLevel goutils.HTTPRequestLogLevel `mapstructure:"logLevel" json:"logLevel" validate:"oneof=warn info debug"`
	// HealthLogLevel output health check logs at this level
	HealthLogLevel goutils.HTTPRequestLogLevel `mapstructure:"healthLogLevel" json:"healthLogLevel" validate:"oneof=warn info debug"`
	// RequestIDHeader is the HTTP header containing the API request ID
	RequestIDHeader string `mapstructure:"requestIDHeader" json:"requestIDHeader"`
	// DoNotLogHeaders is the list of headers to not include in logging metadata
	DoNotLogHeaders []string `mapstructure:"skipHeaders" json:"skipHeaders"`
}

// EndpointConfig defines API endpoint config
type EndpointConfig struct {
	// PathPrefix is the end-point path prefix for the APIs
	PathPrefix string `mapstructure:"pathPrefix" json:"pathPrefix" validate:"required"`
}

// APIConfig defines API settings for a submodule
type APIConfig struct {
	// Endpoint sets API endpoint related parameters
	Endpoint EndpointConfig `mapstructure:"endPoint" json:"endPoint" validate:"required,dive"`
	// RequestLogging sets API request logging parameters
	RequestLogging HTTPRequestLogging `mapstructure:"requestLogging" json:"requestLogging" validate:"required"`
	// EnableMCP enable the MCP endpoint
	EnableMCP bool `mapstructure:"enableMCP" json:"enableMCP"`
}

// APIServerConfig defines HTTP API / server parameters
type APIServerConfig struct {
	// Server defines HTTP server parameters
	Server HTTPServerConfig `mapstructure:"service" json:"service" validate:"required_with=Enabled"`
	// APIs defines API settings for a submodule
	APIs APIConfig `mapstructure:"apis" json:"apis" validate:"required_with=Enabled"`
}

// ======================================================================================
// Metrics

// MetricsFeatureConfig metrics framework features config
type MetricsFeatureConfig struct {
	// EnableAppMetrics whether to enable Golang application metrics
	EnableAppMetrics bool `mapstructure:"enableAppMetrics" json:"enableAppMetrics"`
	// EnableHTTPMetrics whether to enable HTTP request tracking metrics
	EnableHTTPMetrics bool `mapstructure:"enableHTTPMetrics" json:"enableHTTPMetrics"`
}

// MetricsConfig application metrics config
type MetricsConfig struct {
	// Server defines HTTP server parameters
	Server HTTPServerConfig `mapstructure:"service" json:"service" validate:"required_with=Enabled"`
	// MetricsEndpoint path to host the Prometheus metrics endpoint
	MetricsEndpoint string `mapstructure:"metricsEndpoint" json:"metricsEndpoint" validate:"required"`
	// MaxRequests max number of metrics requests in parallel to support
	MaxRequests int `mapstructure:"maxRequests" json:"maxRequests" validate:"gte=1"`
	// Features metrics framework features to enable
	Features MetricsFeatureConfig `mapstructure:"features" json:"features" validate:"gte=1"`
}

// ======================================================================================
// REDIS

// RedisConnectionConfig connection parameter to Redis server
type RedisConnectionConfig struct {
	// Host of the server
	Host string `mapstructure:"host" json:"host" validate:"required,hostname"`
	// Port of the server
	Port uint16 `mapstructure:"port" json:"port"`
	// DBNumber number of the REDIS database
	DBNumber uint32 `mapstructure:"dbNumber" json:"dbNumber" validate:"lte=15"`
}

// ======================================================================================
// Persistence

// SQLiteConfig SQLite persistence config
type SQLiteConfig struct {
	// DBFile the SQLite DB file path
	DBFile string `mapstructure:"file" json:"file" validate:"required"`
}

// ======================================================================================
// Application Config

// ApplicationConfig application config
type ApplicationConfig struct {
	// Metrics metrics framework configuration
	Metrics MetricsConfig `mapstructure:"metrics" json:"metrics" validate:"required"`

	// API server config
	API APIServerConfig `mapstructure:"api" json:"api" validate:"required"`

	// SQLite persistence config
	SQLite SQLiteConfig `mapstructure:"sqlite" json:"sqlite" validate:"required"`

	// Redis connection parameter
	Redis RedisConnectionConfig `mapstructure:"redis" json:"redis" validate:"required"`
}

// ======================================================================================
// Default Configuration Setter

// InstallDefaultServerConfigValues setup default server configs
func InstallDefaultServerConfigValues() {
	// Default metrics config
	viper.SetDefault("metrics.metricsEndpoint", "/metrics")
	viper.SetDefault("metrics.maxRequests", 4)
	// Default metrics features config
	viper.SetDefault("metrics.features.enableAppMetrics", false)
	viper.SetDefault("metrics.features.enableHTTPMetrics", true)
	// Default metrics HTTP server config
	viper.SetDefault("metrics.service.listenOn", "0.0.0.0")
	viper.SetDefault("metrics.service.appPort", 3001)
	viper.SetDefault("metrics.service.timeoutSecs.read", 60)
	viper.SetDefault("metrics.service.timeoutSecs.write", 60)
	viper.SetDefault("metrics.service.timeoutSecs.idle", 60)

	// Default REST API config
	viper.SetDefault("api.service.listenOn", "0.0.0.0")
	viper.SetDefault("api.service.appPort", 38281)
	viper.SetDefault("api.service.timeoutSecs.read", 60)
	viper.SetDefault("api.service.timeoutSecs.write", 0)
	viper.SetDefault("api.service.timeoutSecs.idle", 0)
	viper.SetDefault("api.apis.endPoint.pathPrefix", "/")
	viper.SetDefault("api.apis.requestLogging.logLevel", "warn")
	viper.SetDefault("api.apis.requestLogging.healthLogLevel", "debug")
	viper.SetDefault("api.apis.requestLogging.requestIDHeader", "X-Request-ID")
	viper.SetDefault("api.apis.requestLogging.skipHeaders", []string{
		"WWW-Authenticate", "Authorization", "Proxy-Authenticate", "Proxy-Authorization",
	})
	viper.SetDefault("api.apis.enableMCP", false)

	// Default REDIS config
	viper.SetDefault("redis.host", "127.0.0.1")
	viper.SetDefault("redis.port", 6479)
	viper.SetDefault("redis.dbNumber", 0)
}
