package db

import "github.com/alwitt/rest-pty/models"

// SessionEntry user DB entry
type SessionEntry struct {
	models.Session
}

// TableName hard code table name
func (SessionEntry) TableName() string {
	return "sessions"
}
