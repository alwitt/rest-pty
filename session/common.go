package session

import (
	"context"

	"github.com/alwitt/goutils"
	"github.com/apex/log"
)

// BuildSessionInputBufferName helper function to build a session's INPUT BUFFER name
func BuildSessionInputBufferName(sessionID string) string {
	return sessionID + ".input"
}

// BuildSessionOutputBufferName helper function to build a session's INPUT BUFFER name
func BuildSessionOutputBufferName(sessionID string) string {
	return sessionID + ".output"
}

// BuildSessionIPCQueueName helper function to build a session's IPC queue name
func BuildSessionIPCQueueName(sessionID string) string {
	return sessionID + ".ipc"
}

// BuildSessionIPCRespQueueName helper function to build response IPC queue name for a request
func BuildSessionIPCRespQueueName(requestID string) string {
	return "req." + requestID + ".resp"
}

// taskProcessorFactoryFunc function signature for a task processor factory function
type taskProcessorFactoryFunc func(
	ctxt context.Context,
	instanceName string,
	taskBufferLen int,
	logTags log.Fields,
	metricsHelper goutils.TaskProcessorMetricHelper,
) (goutils.TaskProcessor, error)
