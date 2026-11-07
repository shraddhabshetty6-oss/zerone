//go:build !faultinject

package httpapi

import "net/http"

func (s *Server) registerFailpointsRoutes(mux *http.ServeMux) {
	// In production builds, no failpoints routes exist (returns 404 by default)
}

