// Package httpapi implements the HTTP API v1 server per ARCHITECTURE.md §E.9, §E.8.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"studentdb/internal/buildinfo"
	"studentdb/internal/codec"
	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/engine"
	"studentdb/internal/record"
	"studentdb/internal/retrieval"
	"studentdb/internal/storage"
	"studentdb/internal/tx"
	"studentdb/internal/wal"
)

// Server coordinates the HTTP listener and route handlers.
type Server struct {
	db         *engine.DB
	server     *http.Server
	listener   net.Listener
	adminToken string
	addr       string
	syncMode   string
	startVer   string
	shutdownCh chan struct{}
}

// ErrorResponse represents a standardized API error body per §E.8.
type ErrorResponse struct {
	Code       string                   `json:"code"`
	Message    string                   `json:"message"`
	Violations []record.ValidationError `json:"violations,omitempty"`
}

// NewServer creates a new HTTP API server.
func NewServer(db *engine.DB, addr, syncMode, startVer string) (*Server, error) {
	adminToken := os.Getenv("STUDENTDB_ADMIN_TOKEN")

	s := &Server{
		db:         db,
		adminToken: adminToken,
		addr:       addr,
		syncMode:   syncMode,
		startVer:   startVer,
		shutdownCh: make(chan struct{}),
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.server = &http.Server{
		Handler:      s.checkLoopbackMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s, nil
}

// Start begins listening on the configured loopback address.
func (s *Server) Start() (string, error) {
	// Loopback-only enforcement per §B.4
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		host = s.addr
		port = "0"
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "" {
		return "", fmt.Errorf("security: binding to non-loopback address %q is forbidden", host)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return "", err
	}
	s.listener = ln

	go func() {
		_ = s.server.Serve(ln)
	}()

	return ln.Addr().String(), nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) checkLoopbackMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			if host != "127.0.0.1" && host != "::1" && host != "localhost" {
				http.Error(w, "Forbidden: loopback only", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) adminAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken != "" {
			authHeader := r.Header.Get("Authorization")
			expected := "Bearer " + s.adminToken
			if authHeader != expected {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid admin bearer token")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/students", s.handleCreateStudent)
	mux.HandleFunc("GET /v1/students/{id}", s.handleGetStudent)
	mux.HandleFunc("PUT /v1/students/{id}", s.handleUpdateStudent)
	mux.HandleFunc("DELETE /v1/students/{id}", s.handleDeleteStudent)
	mux.HandleFunc("POST /v1/tx", s.handleBatchTx)
	mux.HandleFunc("GET /v1/students", s.handleListStudents)

	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/admin/stats", s.adminAuthMiddleware(s.handleAdminStats))
	mux.HandleFunc("GET /v1/admin/verify", s.adminAuthMiddleware(s.handleAdminVerify))
	mux.HandleFunc("POST /v1/admin/checkpoint", s.adminAuthMiddleware(s.handleAdminCheckpoint))
	mux.HandleFunc("POST /v1/admin/shutdown", s.adminAuthMiddleware(s.handleAdminShutdown))
	mux.HandleFunc("GET /v1/admin/capabilities", s.handleAdminCapabilities)

	// Register failpoints routes (compiled conditionally)
	s.registerFailpointsRoutes(mux)
}

func (s *Server) handleCreateStudent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var st record.Student
	if err := dec.Decode(&st); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	created, txInfo, err := s.db.Create(r.Context(), &st)
	if err != nil {
		mapEngineError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"student":    created,
		"tx_id":      txInfo.TxID,
		"commit_lsn": txInfo.CommitLSN,
	})
}

func (s *Server) handleGetStudent(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "student id must be positive uint32")
		return
	}

	st, err := s.db.Get(r.Context(), uint32(id))
	if err != nil {
		mapEngineError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUpdateStudent(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "student id must be positive uint32")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var st record.Student
	if err := dec.Decode(&st); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	updated, txInfo, err := s.db.Update(r.Context(), uint32(id), &st)
	if err != nil {
		mapEngineError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"student":    updated,
		"tx_id":      txInfo.TxID,
		"commit_lsn": txInfo.CommitLSN,
	})
}

func (s *Server) handleDeleteStudent(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "student id must be positive uint32")
		return
	}

	txInfo, err := s.db.Delete(r.Context(), uint32(id))
	if err != nil {
		mapEngineError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tx_id":      txInfo.TxID,
		"commit_lsn": txInfo.CommitLSN,
	})
}

func (s *Server) handleBatchTx(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req struct {
		Ops []engine.BatchOp `json:"ops"`
	}
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	txInfo, results, err := s.db.Batch(r.Context(), req.Ops)
	if err != nil {
		mapEngineError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tx_id":      txInfo.TxID,
		"commit_lsn": txInfo.CommitLSN,
		"results":    results,
	})
}

func (s *Server) handleListStudents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode := q.Get("mode")

	limit := 1000
	if lStr := q.Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}

	if mode == "index" {
		var fromID uint32 = 0
		var toID uint32 = 0xFFFFFFFF
		if fStr := q.Get("from"); fStr != "" {
			if f, err := strconv.ParseUint(fStr, 10, 32); err == nil {
				fromID = uint32(f)
			}
		}
		if tStr := q.Get("to"); tStr != "" {
			if t, err := strconv.ParseUint(tStr, 10, 32); err == nil {
				toID = uint32(t)
			}
		}

		items, nextFrom, err := s.db.RangeScan(fromID, toID, limit)
		if err != nil {
			mapEngineError(w, err)
			return
		}

		resp := map[string]any{
			"items": items,
		}
		if nextFrom != nil {
			resp["next_from"] = *nextFrom
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Sequential scan mode
	var cursor uint32 = 0
	if cStr := q.Get("cursor"); cStr != "" {
		if c, err := strconv.ParseUint(cStr, 10, 32); err == nil {
			cursor = uint32(c)
		}
	}

	filter := retrieval.Filter{
		Department: q.Get("department"),
		LastName:   q.Get("last_name"),
	}
	if eyStr := q.Get("enrollment_year"); eyStr != "" {
		if ey, err := strconv.ParseUint(eyStr, 10, 16); err == nil {
			val := uint16(ey)
			filter.EnrollmentYear = &val
		}
	}

	strict := q.Get("strict") == "true"

	page, err := s.db.SequentialScan(cursor, limit, filter, strict)
	if err != nil {
		mapEngineError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"state": s.db.State(),
	})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.db.Stats())
}

func (s *Server) handleAdminVerify(w http.ResponseWriter, r *http.Request) {
	res := s.db.VerifyOnline()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAdminCheckpoint(w http.ResponseWriter, r *http.Request) {
	checkpointLSN, err := s.db.ForceCheckpoint()
	if err != nil {
		mapEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checkpoint_lsn": checkpointLSN,
	})
}

func (s *Server) handleAdminShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "shutting_down",
	})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = s.db.Close()
		os.Exit(0)
	}()
}

func (s *Server) handleAdminCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"build":          buildinfo.Build,
		"format_version": constants.FormatVersion,
		"sync_mode":      s.syncMode,
		"startup_verify": s.startVer,
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, ErrorResponse{
		Code:    code,
		Message: msg,
	})
}

func mapEngineError(w http.ResponseWriter, err error) {
	var valErr *record.ErrValidation
	if errors.As(err, &valErr) {
		writeJSON(w, http.StatusUnprocessableEntity, ErrorResponse{
			Code:       "VALIDATION_FAILED",
			Message:    valErr.Error(),
			Violations: valErr.Violations,
		})
		return
	}

	switch {
	case errors.Is(err, engine.ErrDuplicateID):
		writeError(w, http.StatusConflict, "DUPLICATE_ID", err.Error())
	case errors.Is(err, engine.ErrNotFound), errors.Is(err, retrieval.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, codec.ErrChecksumMismatch):
		writeError(w, http.StatusInternalServerError, "CHECKSUM_MISMATCH", err.Error())
	case errors.Is(err, codec.ErrInvalidRecord):
		writeError(w, http.StatusInternalServerError, "INVALID_RECORD", err.Error())
	case errors.Is(err, retrieval.ErrIndexInconsistent):
		writeError(w, http.StatusInternalServerError, "INDEX_INCONSISTENT", err.Error())
	case errors.Is(err, storage.ErrDiskFull), errors.Is(err, wal.ErrDiskFull):
		writeError(w, 507, "DISK_FULL", err.Error())
	case errors.Is(err, concurrency.ErrBusy):
		writeError(w, http.StatusServiceUnavailable, "BUSY", err.Error())
	case errors.Is(err, tx.ErrFailedReadOnly), errors.Is(err, storage.ErrEngineFailed), errors.Is(err, wal.ErrEngineFailed):
		writeError(w, http.StatusServiceUnavailable, "ENGINE_FAILED_READONLY", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

