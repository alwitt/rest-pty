// Package models - application data models
package models

import (
	"time"

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
	// LogRequestPayload whether to log request payload
	LogRequestPayload bool `mapstructure:"logRequestPayload" json:"logRequestPayload"`
}

// EndpointConfig defines API endpoint config
type EndpointConfig struct {
	// PathPrefix is the end-point path prefix for the APIs
	PathPrefix string `mapstructure:"pathPrefix" json:"pathPrefix" validate:"required"`
}

// APIConfig defines API settings for a submodule
type APIConfig struct {
	// Endpoint sets API endpoint related parameters
	Endpoint EndpointConfig `mapstructure:"endPoint" json:"endPoint" validate:"required"`
	// RequestLogging sets API request logging parameters
	RequestLogging HTTPRequestLogging `mapstructure:"requestLogging" json:"requestLogging" validate:"required"`
	// EnableMCP enable the MCP endpoint
	EnableMCP bool `mapstructure:"enableMCP" json:"enableMCP"`
}

// APIServerConfig defines HTTP API / server parameters
type APIServerConfig struct {
	// Server defines HTTP server parameters
	Server HTTPServerConfig `mapstructure:"service" json:"service" validate:"required"`
	// APIs defines API settings for a submodule
	APIs APIConfig `mapstructure:"apis" json:"apis" validate:"required"`
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
	Server HTTPServerConfig `mapstructure:"service" json:"service" validate:"required"`
	// MetricsEndpoint path to host the Prometheus metrics endpoint
	MetricsEndpoint string `mapstructure:"metricsEndpoint" json:"metricsEndpoint" validate:"required"`
	// MaxRequests max number of metrics requests in parallel to support
	MaxRequests int `mapstructure:"maxRequests" json:"maxRequests" validate:"gte=1"`
	// Features metrics framework features to enable
	Features MetricsFeatureConfig `mapstructure:"features" json:"features" validate:"required"`
}

// ======================================================================================
// HTTP Client

// HTTPClientAuthConfig HTTP client OAuth middleware configuration
//
// Currently only support client-credential OAuth flow configuration
type HTTPClientAuthConfig struct {
	// IssuerURL OpenID provider issuer URL
	IssuerURL string `mapstructure:"issuerURL" json:"issuerURL" validate:"required,url"`
	// ClientID OAuth client ID
	ClientID string `mapstructure:"clientID" json:"clientID" validate:"required"`
	// ClientSecret OAuth client secret
	ClientSecret string `mapstructure:"clientSecret" json:"clientSecret" validate:"required"`
	// TargetAudience target audience `aud` to acquire a token for
	TargetAudience string `mapstructure:"targetAudience" json:"targetAudience" validate:"required,url"`
}

// HTTPClientRetryConfig HTTP client config retry configuration
type HTTPClientRetryConfig struct {
	// MaxAttempts max number of retry attempts
	MaxAttempts int `mapstructure:"maxAttempts" json:"maxAttempts" validate:"gte=0"`
	// InitWaitTimeInSec wait time before the first wait retry
	InitWaitTimeInSec uint32 `mapstructure:"initialWaitTimeInSec" json:"initialWaitTimeInSec" validate:"gte=1"`
	// MaxWaitTimeInSec max wait time
	MaxWaitTimeInSec uint32 `mapstructure:"maxWaitTimeInSec" json:"maxWaitTimeInSec" validate:"gte=1"`
}

// InitWaitTime convert InitWaitTimeInSec to time.Duration
func (c HTTPClientRetryConfig) InitWaitTime() time.Duration {
	return time.Second * time.Duration(c.InitWaitTimeInSec)
}

// MaxWaitTime convert MaxWaitTimeInSec to time.Duration
func (c HTTPClientRetryConfig) MaxWaitTime() time.Duration {
	return time.Second * time.Duration(c.MaxWaitTimeInSec)
}

// HTTPClientConfig HTTP client config targeting `go-resty`
//
// NOTE: neither field carries `dive`. `dive` is for slices and maps; on any other kind the
// validator panics outright ("dive error! can't dive on a non slice or map"). It is also
// unnecessary here — a nested struct, and a non-nil pointer to one, are descended into on
// their own.
type HTTPClientConfig struct {
	// OAuth OAuth middleware integration configuration
	OAuth *HTTPClientAuthConfig `mapstructure:"oauth,omitempty" json:"oauth,omitempty" validate:"omitempty"`
	// Retry client retry configuration. See https://github.com/go-resty/resty#retries for details
	Retry HTTPClientRetryConfig `mapstructure:"retry" json:"retry" validate:"required"`
}

// ======================================================================================
// cairn

// CairnConfig cairn service integration config.
//
// Optional: with Enable false rest-pty serves exactly as it did before cairn existed, and a
// session naming a workspace fails to start rather than silently running unmounted.
//
// BaseURL and Client are pointers so their absence is meaningful, and both are
// `required_with=Enable` rather than `required`. That is also why nothing may be registered
// under `cairn.*` in InstallDefaultServerConfigValues: a viper default materializes the key,
// mapstructure then allocates the pointer, and `required_with` could never fire again.
type CairnConfig struct {
	// Enable whether to enable the cairn integration
	Enable bool `mapstructure:"enable" json:"enable"`

	// BaseURL cairn API server base URL. rest-pty builds its endpoint paths from this.
	//
	// `omitempty` is load bearing, not decoration: `required_with` is one of the tags that
	// runs even against a nil pointer, so when Enable is false it passes and hands off to the
	// next tag — and `url` panics on any kind that is not a string. `omitempty` is what stops
	// the chain there.
	BaseURL *string `mapstructure:"baseURL" json:"baseURL,omitempty" validate:"required_with=Enable,omitempty,url"`

	// Client HTTP client configuration used when connecting to cairn
	Client *HTTPClientConfig `mapstructure:"client" json:"client,omitempty" validate:"required_with=Enable"`
}

// ======================================================================================
// REDIS

// RedisConnectionConfig connection parameter to Redis server
type RedisConnectionConfig struct {
	// Host of the server
	Host string `mapstructure:"host" json:"host" validate:"required"`
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

// PostgresSSLConfig Postgres connection SSL config
type PostgresSSLConfig struct {
	// Enabled whether to enable SSL when connecting to Postgres
	Enabled bool `mapstructure:"enabled" json:"enabled"`
	// CAFile the CA cert file to challenge remote with
	CAFile *string `mapstructure:"caFile" json:"caFile,omitempty" validate:"omitempty,file"`
}

// PostgresConfig Postgres connection config
type PostgresConfig struct {
	// DebugLog whether to output ORM layer debug logs
	DebugLog bool `mapstructure:"debugLog" json:"debugLog"`
	// Host Postgres server host
	Host string `mapstructure:"host" json:"host" validate:"required"`
	// Port Postgres server port
	Port uint16 `mapstructure:"port" json:"port" validate:"lte=65535,gte=0"`
	// Database the specific database to use
	Database string `mapstructure:"db" json:"db" validate:"required"`
	// User the user to connect with
	User string `mapstructure:"user" json:"user" validate:"required"`
	// Password the user password
	Password *string `json:"-" validate:"-"`
	// SSL the connection SSL settings
	SSL PostgresSSLConfig `mapstructure:"ssl" json:"ssl" validate:"required"`
}

// PersistenceConfig application persistence config
type PersistenceConfig struct {
	// SQLite persistence config
	SQLite *SQLiteConfig `mapstructure:"sqlite,omitempty" json:"sqlite,omitempty" validate:"required_without=Postgres"`

	// Postgres persistence config
	Postgres *PostgresConfig `mapstructure:"postgres,omitempty" json:"postgres,omitempty" validate:"required_without=SQLite"`
}

// ======================================================================================
// Application Config

// ApplicationConfig application config
type ApplicationConfig struct {
	// Metrics metrics framework configuration
	Metrics MetricsConfig `mapstructure:"metrics" json:"metrics" validate:"required"`

	// API server config
	API APIServerConfig `mapstructure:"api" json:"api" validate:"required"`

	// Persistence application persistence config
	Persistence PersistenceConfig `mapstructure:"persistence" json:"persistence" validate:"required"`

	// Redis connection parameter
	Redis RedisConnectionConfig `mapstructure:"redis" json:"redis" validate:"required"`

	// Cairn cairn service integration config.
	//
	// Deliberately not `required`: that means "not the zero value", and a CairnConfig with
	// Enable false and both pointers nil IS the zero value - which is the ordinary
	// cairn-disabled deployment, not an invalid one.
	Cairn CairnConfig `mapstructure:"cairn" json:"cairn"`
}

// ======================================================================================
// Default Configuration Setter

// InstallDefaultServerConfigValues setup default server configs
//
// NOTE: nothing may be registered under `cairn.*` here. A viper default materializes the key,
// mapstructure then allocates CairnConfig's pointers, and the `required_with=Enable` tags on
// them could never fire again.
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
	viper.SetDefault("api.apis.requestLogging.logRequestPayload", false)
	viper.SetDefault("api.apis.enableMCP", false)

	// Default REDIS config
	viper.SetDefault("redis.host", "127.0.0.1")
	viper.SetDefault("redis.port", 6479)
	viper.SetDefault("redis.dbNumber", 0)
}
