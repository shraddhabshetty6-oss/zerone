// Package retrieval implements sequential and indexed retrieval engines per ARCHITECTURE.md §E.3, §E.4.
package retrieval

import (
	"errors"

	"studentdb/internal/record"
)

var (
	// ErrNotFound is returned when a requested StudentID is not found.
	ErrNotFound = errors.New("record not found (NOT_FOUND)")

	// ErrIndexInconsistent is returned when the primary index points to a slot with mismatched ID or non-active status.
	ErrIndexInconsistent = errors.New("index inconsistent with data slot (INDEX_INCONSISTENT)")
)

// Filter specifies equality filter criteria for sequential scans.
type Filter struct {
	Department     string
	EnrollmentYear *uint16
	LastName       string
}

// Matches returns true if the student matches all non-empty filter criteria.
func (f *Filter) Matches(s *record.Student) bool {
	if f.Department != "" && s.Department != f.Department {
		return false
	}
	if f.EnrollmentYear != nil && s.EnrollmentYear != *f.EnrollmentYear {
		return false
	}
	if f.LastName != "" && s.LastName != f.LastName {
		return false
	}
	return true
}

// ScanError describes a corruption error found during sequential scan.
type ScanError struct {
	Slot   uint32 `json:"slot"`
	Offset int64  `json:"offset"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// ScanPage is the response for a sequential scan page.
type ScanPage struct {
	Items      []*record.Student `json:"items"`
	Errors     []ScanError       `json:"errors"`
	NextCursor *uint32           `json:"next_cursor,omitempty"`
}

