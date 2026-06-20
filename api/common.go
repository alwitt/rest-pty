// Package api - application REST API
package api //revive:disable-line:var-naming

import (
	"net/http"

	"github.com/gorilla/mux"
)

// methodHandlers DICT of method-endpoint handler
type methodHandlers map[string]http.HandlerFunc

// registerPathPrefix registers new method handler for a path prefix
func registerPathPrefix(parent *mux.Router, prefix string, handler methodHandlers) *mux.Router {
	router := parent.PathPrefix(prefix).Subrouter()
	for method, handler := range handler {
		router.Methods(method).Path("").HandlerFunc(handler)
	}
	return router
}
