package models

import "fmt"

// BadInputError malformed data input error
type BadInputError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e BadInputError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e BadInputError) Unwrap() error {
	return e.Core
}

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

// BootStrapError application boot strap error
type BootStrapError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e BootStrapError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e BootStrapError) Unwrap() error {
	return e.Core
}

// ShutdownError application shutdown error
type ShutdownError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e ShutdownError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e ShutdownError) Unwrap() error {
	return e.Core
}

// ======================================================================================
// Session Runner Errors

// SessionRunnerStartUpError error during session runner `StartSession`
type SessionRunnerStartUpError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionRunnerStartUpError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionRunnerStartUpError) Unwrap() error {
	return e.Core
}

// SessionRunnerShutdownError error during session runner `StopSession`
type SessionRunnerShutdownError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionRunnerShutdownError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionRunnerShutdownError) Unwrap() error {
	return e.Core
}

// SessionRunnerSubmitCommandError error when session runner submit commands to driver
type SessionRunnerSubmitCommandError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionRunnerSubmitCommandError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionRunnerSubmitCommandError) Unwrap() error {
	return e.Core
}

// SessionRunnerIPCProcessError error session runner encountered processing IPC messages
type SessionRunnerIPCProcessError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionRunnerIPCProcessError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionRunnerIPCProcessError) Unwrap() error {
	return e.Core
}

// ======================================================================================
// Session Manager Errors

// SessionManagerStartSessionError error session manager start session runner
type SessionManagerStartSessionError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionManagerStartSessionError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionManagerStartSessionError) Unwrap() error {
	return e.Core
}

// SessionManagerStopSessionError error session manager stop session runner
type SessionManagerStopSessionError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionManagerStopSessionError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionManagerStopSessionError) Unwrap() error {
	return e.Core
}

// SessionManagerStopAllSessionsError error session manager bulk stop session runners
type SessionManagerStopAllSessionsError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e SessionManagerStopAllSessionsError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e SessionManagerStopAllSessionsError) Unwrap() error {
	return e.Core
}
