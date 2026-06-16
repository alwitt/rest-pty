package session

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
