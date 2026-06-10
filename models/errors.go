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

// REDISError error encountered with REDIS
type REDISError struct {
	Core    error
	Message string
}

// Error implement error interface
func (e REDISError) Error() string {
	if e.Core != nil {
		return fmt.Sprintf("%s [%v]", e.Message, e.Core)
	}
	return e.Message
}

// Unwrap implement wrapped error
func (e REDISError) Unwrap() error {
	return e.Core
}
