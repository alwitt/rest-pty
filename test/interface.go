// Package test - various support components used in unit-testing.
package test

// ForTextSessionManager dummy session manager interface used for session runner testing
type ForTextSessionManager interface {
	// SessionIdleNotify callback triggered when a managed session went IDLE
	SessionIdleNotify()
	// IPCProcessErrorNotify callback triggered when a managed session encountered error
	// interacting with IPC request queue
	IPCProcessErrorNotify(error)
}
