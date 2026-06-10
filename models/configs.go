// Package models - application data models
package models

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
