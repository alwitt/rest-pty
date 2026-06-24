package models

import (
	"github.com/alwitt/goutils"
)

// ======================================================================================
// PTY Errors

// PTYError PTY specific error
type PTYError struct{ goutils.BaseError }

// NewPTYError builds a PTYError, optionally capturing the call stack.
func NewPTYError(message string, core error, getCallStack bool) PTYError {
	base := goutils.BaseError{Name: "PTYError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return PTYError{BaseError: base}
}

// ======================================================================================
// Persistence Errors - SQL

// PersistenceError error encountered with persistence
type PersistenceError struct{ goutils.BaseError }

// NewPersistenceError builds a PersistenceError, optionally capturing the call stack.
func NewPersistenceError(message string, core error, getCallStack bool) PersistenceError {
	base := goutils.BaseError{Name: "PersistenceError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return PersistenceError{BaseError: base}
}

// ======================================================================================
// Session Runner Errors

// SessionRunnerStartUpError error during session runner `StartSession`
type SessionRunnerStartUpError struct{ goutils.BaseError }

// NewSessionRunnerStartUpError builds a SessionRunnerStartUpError, optionally
// capturing the call stack.
func NewSessionRunnerStartUpError(
	message string, core error, getCallStack bool,
) SessionRunnerStartUpError {
	base := goutils.BaseError{Name: "SessionRunnerStartUpError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionRunnerStartUpError{BaseError: base}
}

// SessionRunnerShutdownError error during session runner `StopSession`
type SessionRunnerShutdownError struct{ goutils.BaseError }

// NewSessionRunnerShutdownError builds a SessionRunnerShutdownError, optionally
// capturing the call stack.
func NewSessionRunnerShutdownError(
	message string, core error, getCallStack bool,
) SessionRunnerShutdownError {
	base := goutils.BaseError{Name: "SessionRunnerShutdownError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionRunnerShutdownError{BaseError: base}
}

// SessionRunnerSubmitCommandError error when session runner submit commands to driver
type SessionRunnerSubmitCommandError struct{ goutils.BaseError }

// NewSessionRunnerSubmitCommandError builds a SessionRunnerSubmitCommandError, optionally
// capturing the call stack.
func NewSessionRunnerSubmitCommandError(
	message string, core error, getCallStack bool,
) SessionRunnerSubmitCommandError {
	base := goutils.BaseError{Name: "SessionRunnerSubmitCommandError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionRunnerSubmitCommandError{BaseError: base}
}

// SessionRunnerIPCProcessError error session runner encountered processing IPC messages
type SessionRunnerIPCProcessError struct{ goutils.BaseError }

// NewSessionRunnerIPCProcessError builds a SessionRunnerIPCProcessError, optionally
// capturing the call stack.
func NewSessionRunnerIPCProcessError(
	message string, core error, getCallStack bool,
) SessionRunnerIPCProcessError {
	base := goutils.BaseError{Name: "SessionRunnerIPCProcessError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionRunnerIPCProcessError{BaseError: base}
}

// ======================================================================================
// Session Manager Errors

// SessionManagerStartSessionError error session manager start session runner
type SessionManagerStartSessionError struct{ goutils.BaseError }

// NewSessionManagerStartSessionError builds a SessionManagerStartSessionError, optionally
// capturing the call stack.
func NewSessionManagerStartSessionError(
	message string, core error, getCallStack bool,
) SessionManagerStartSessionError {
	base := goutils.BaseError{Name: "SessionManagerStartSessionError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionManagerStartSessionError{BaseError: base}
}

// SessionManagerStopSessionError error session manager stop session runner
type SessionManagerStopSessionError struct{ goutils.BaseError }

// NewSessionManagerStopSessionError builds a SessionManagerStopSessionError, optionally
// capturing the call stack.
func NewSessionManagerStopSessionError(
	message string, core error, getCallStack bool,
) SessionManagerStopSessionError {
	base := goutils.BaseError{Name: "SessionManagerStopSessionError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionManagerStopSessionError{BaseError: base}
}

// SessionManagerStopAllSessionsError error session manager bulk stop session runners
type SessionManagerStopAllSessionsError struct{ goutils.BaseError }

// NewSessionManagerStopAllSessionsError builds a SessionManagerStopAllSessionsError, optionally
// capturing the call stack.
func NewSessionManagerStopAllSessionsError(
	message string, core error, getCallStack bool,
) SessionManagerStopAllSessionsError {
	base := goutils.BaseError{Name: "SessionManagerStopAllSessionsError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return SessionManagerStopAllSessionsError{BaseError: base}
}

// ======================================================================================
// Application Lifecycle Errors

// BootStrapError application boot strap error
type BootStrapError struct{ goutils.BaseError }

// NewBootStrapError builds a BootStrapError, optionally capturing the call stack.
func NewBootStrapError(message string, core error, getCallStack bool) BootStrapError {
	base := goutils.BaseError{Name: "BootStrapError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return BootStrapError{BaseError: base}
}

// ShutdownError application shutdown error
type ShutdownError struct{ goutils.BaseError }

// NewShutdownError builds a ShutdownError, optionally capturing the call stack.
func NewShutdownError(message string, core error, getCallStack bool) ShutdownError {
	base := goutils.BaseError{Name: "ShutdownError", Message: message, Core: core}
	if getCallStack {
		base.Stack = goutils.GetCallStack(1)
	}
	return ShutdownError{BaseError: base}
}
