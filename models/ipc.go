package models

// IPCMessageEnvelope type erased IPC message object
type IPCMessageEnvelope interface {
	// StringPayload return its payload as a string
	StringPayload() (string, error)
}
