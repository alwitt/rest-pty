package db

import "github.com/alwitt/rest-pty/models"

// sessionEntry user DB entry
type sessionEntry struct {
	models.Session
}

// TableName hard code table name
func (sessionEntry) TableName() string {
	return "sessions"
}
