// Package models - application data models
package models

import "github.com/alwitt/goutils"

// ======================================================================================
// REDIS

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
	// TargetHost the target FQDN of the requests to proxy
	TargetHost string `mapstructure:"host" json:"host" validate:"required,fqdn"`
	// RequestLogging sets API request logging parameters
	RequestLogging HTTPRequestLogging `mapstructure:"requestLogging" json:"requestLogging" validate:"required"`
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
	DBNumber uint32 `mapstructure:"dbNumber" json:"dbNumber"`
}

// ======================================================================================
// Application Config

// ApplicationConfig application config
type ApplicationConfig struct {
	// Server defines HTTP server parameters
	Server HTTPServerConfig `mapstructure:"server" json:"server" validate:"required"`
	// APIs defines API settings for a submodule
	APIs APIConfig `mapstructure:"apis" json:"apis" validate:"required"`
}
