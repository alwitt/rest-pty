// Package test - various support components used in unit-testing.
package test

import (
	"github.com/alwitt/goutils"
	goutilsRedis "github.com/alwitt/goutils/redis"
)

// TaskProcessorForTest wrapper interface for generating a mock of `TaskProcessor`
type TaskProcessorForTest interface {
	goutils.TaskProcessor
}

// ForTextSessionManager dummy session manager interface used for session runner testing
type ForTextSessionManager interface {
	// SessionIdleNotify callback triggered when a managed session went IDLE
	SessionIdleNotify()
	// IPCProcessErrorNotify callback triggered when a managed session encountered error
	// interacting with IPC request queue
	IPCProcessErrorNotify(error)
}

// RedisClientForTest wrapper interface for generating a mock of `goutilsRedis.Client`
type RedisClientForTest interface {
	goutilsRedis.Client
}

// RedisQueueForTest wrapper interface for generating a mock of `goutilsRedis.Queue`
type RedisQueueForTest interface {
	goutilsRedis.Queue
}

// RedisBufferForTest wrapper interface for generating a mock of `goutilsRedis.RingBuffer`
type RedisBufferForTest interface {
	goutilsRedis.RingBuffer
}
