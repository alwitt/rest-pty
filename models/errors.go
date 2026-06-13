package models

import "fmt"

// ConsistencyError a data consistency error not caused by a system failure
type ConsistencyError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e ConsistencyError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e ConsistencyError) Unwrap() error {
	return e.Core
}

// UnknownSessionError a call referenced an unknown session
type UnknownSessionError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e UnknownSessionError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e UnknownSessionError) Unwrap() error {
	return e.Core
}

// RuntimeError general runtime error
type RuntimeError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e RuntimeError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e RuntimeError) Unwrap() error {
	return e.Core
}

// PTYError PTY specific error
type PTYError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e PTYError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e PTYError) Unwrap() error {
	return e.Core
}

// RedisError error encountered with REDIS
type RedisError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e RedisError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e RedisError) Unwrap() error {
	return e.Core
}

// PersistenceError error encountered with persistence
type PersistenceError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e PersistenceError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e PersistenceError) Unwrap() error {
	return e.Core
}

// ValidationError error when data fails validation
type ValidationError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e ValidationError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e ValidationError) Unwrap() error {
	return e.Core
}
