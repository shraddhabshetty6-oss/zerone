//go:build faultinject

package httpapi

import (
	"io"
	"net/http"

	"studentdb/internal/faultinject"
)

func (s *Server) registerFailpointsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/failpoints", s.adminAuthMiddleware(s.handleGetFailpoints))
	mux.HandleFunc("POST /v1/admin/failpoints", s.adminAuthMiddleware(s.handlePostFailpoints))
	mux.HandleFunc("DELETE /v1/admin/failpoints", s.adminAuthMiddleware(s.handleDeleteFailpoints))
}

func (s *Server) handleGetFailpoints(w http.ResponseWriter, r *http.Request) {
	counters := faultinject.Counters()
	writeJSON(w, http.StatusOK, map[string]any{
		"counters": counters,
	})
}

func (s *Server) handlePostFailpoints(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	if err := faultinject.Arm(body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SPEC", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "armed",
	})
}

func (s *Server) handleDeleteFailpoints(w http.ResponseWriter, r *http.Request) {
	faultinject.Disarm()
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "disarmed",
	})
}

