// Package record defines the Student record model and validation rules per ARCHITECTURE.md §I.2.
package record

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Gender enum values per ARCHITECTURE.md §C.3.
const (
	GenderUnspecified uint8 = 0
	GenderFemale      uint8 = 1
	GenderMale        uint8 = 2
	GenderOther       uint8 = 3
)

// GenderToString maps a gender byte code to its canonical string representation.
func GenderToString(g uint8) string {
	switch g {
	case GenderFemale:
		return "female"
	case GenderMale:
		return "male"
	case GenderOther:
		return "other"
	default:
		return "unspecified"
	}
}

// StringToGender maps a canonical string representation to its gender byte code.
func StringToGender(s string) (uint8, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "unspecified", "":
		return GenderUnspecified, true
	case "female":
		return GenderFemale, true
	case "male":
		return GenderMale, true
	case "other":
		return GenderOther, true
	default:
		return 0, false
	}
}

// ValidationError records a constraint violation for a single field.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ErrValidation accumulates all field violations encountered during validation.
type ErrValidation struct {
	Violations []ValidationError
}

func (e *ErrValidation) Error() string {
	if len(e.Violations) == 0 {
		return "validation failed"
	}
	var sb strings.Builder
	sb.WriteString("validation failed: ")
	for i, v := range e.Violations {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(fmt.Sprintf("%s: %s", v.Field, v.Message))
	}
	return sb.String()
}

// Student represents a student entity in memory and over JSON API.
type Student struct {
	StudentID      uint32    `json:"student_id"`
	FirstName      string    `json:"first_name"`
	LastName       string    `json:"last_name"`
	DateOfBirth    string    `json:"date_of_birth"` // YYYY-MM-DD
	Gender         string    `json:"gender"`        // unspecified | female | male | other
	Email          string    `json:"email"`
	Phone          string    `json:"phone"`
	Department     string    `json:"department"`
	EnrollmentYear uint16    `json:"enrollment_year"`
	GPA            float64   `json:"gpa"` // 0.00 - 4.00
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	RecordVersion  uint64    `json:"record_version,omitempty"`
}

// MarshalJSON marshals Student to JSON matching ARCHITECTURE.md §E.9.
func (s Student) MarshalJSON() ([]byte, error) {
	type Alias Student
	aux := struct {
		Alias
		CreatedAt string  `json:"created_at,omitempty"`
		UpdatedAt string  `json:"updated_at,omitempty"`
		GPA       float64 `json:"gpa"`
	}{
		Alias: Alias(s),
		GPA:   math.Round(s.GPA*100) / 100,
	}
	if !s.CreatedAt.IsZero() {
		aux.CreatedAt = s.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !s.UpdatedAt.IsZero() {
		aux.UpdatedAt = s.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(aux)
}

// UnmarshalJSON unmarshals Student from JSON.
func (s *Student) UnmarshalJSON(data []byte) error {
	type Alias Student
	aux := struct {
		Alias
		CreatedAt string `json:"created_at,omitempty"`
		UpdatedAt string `json:"updated_at,omitempty"`
	}{
		Alias: Alias(*s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*s = Student(aux.Alias)
	if aux.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339, aux.CreatedAt)
		if err == nil {
			s.CreatedAt = t.UTC()
		}
	}
	if aux.UpdatedAt != "" {
		t, err := time.Parse(time.RFC3339, aux.UpdatedAt)
		if err == nil {
			s.UpdatedAt = t.UTC()
		}
	}
	return nil
}

// DaysSinceEpoch converts a YYYY-MM-DD date string to days since 1970-01-01 UTC.
func DaysSinceEpoch(dateStr string) (int32, error) {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return 0, err
	}
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	days := int32(t.Sub(epoch).Hours() / 24)
	return days, nil
}

// EpochDaysToDate converts days since 1970-01-01 UTC to a YYYY-MM-DD date string.
func EpochDaysToDate(days int32) string {
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	t := epoch.Add(time.Duration(days) * 24 * time.Hour)
	return t.Format("2006-01-02")
}

// Normalize trims whitespace from user-supplied string fields.
func (s *Student) Normalize() {
	s.FirstName = strings.TrimSpace(s.FirstName)
	s.LastName = strings.TrimSpace(s.LastName)
	s.DateOfBirth = strings.TrimSpace(s.DateOfBirth)
	s.Gender = strings.ToLower(strings.TrimSpace(s.Gender))
	s.Email = strings.TrimSpace(s.Email)
	s.Phone = strings.TrimSpace(s.Phone)
	s.Department = strings.TrimSpace(s.Department)
}

// Validate checks all field constraints against the current time.
func (s *Student) Validate() error {
	return s.ValidateWithClock(time.Now().UTC())
}

// ValidateWithClock validates all constraints per ARCHITECTURE.md §I.2 using the provided clock.
func (s *Student) ValidateWithClock(now time.Time) error {
	s.Normalize()
	var violations []ValidationError

	// StudentID: 1..4,294,967,295
	if s.StudentID == 0 {
		violations = append(violations, ValidationError{
			Field:   "student_id",
			Message: "must be between 1 and 4294967295",
		})
	}

	// FirstName: 1..32 bytes UTF-8; no NUL, no control chars
	if err := validateName("first_name", s.FirstName, 32); err != nil {
		violations = append(violations, *err)
	}

	// LastName: 1..32 bytes UTF-8; no NUL, no control chars
	if err := validateName("last_name", s.LastName, 32); err != nil {
		violations = append(violations, *err)
	}

	// DateOfBirth: 1900-01-01 .. today; valid Gregorian date
	if s.DateOfBirth == "" {
		violations = append(violations, ValidationError{
			Field:   "date_of_birth",
			Message: "date of birth is required",
		})
	} else {
		dob, err := time.Parse("2006-01-02", s.DateOfBirth)
		if err != nil {
			violations = append(violations, ValidationError{
				Field:   "date_of_birth",
				Message: "must be in YYYY-MM-DD format with valid calendar date",
			})
		} else {
			minDate := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
			today := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
			if dob.Before(minDate) || dob.After(today) {
				violations = append(violations, ValidationError{
					Field:   "date_of_birth",
					Message: fmt.Sprintf("must be between 1900-01-01 and %s", today.Format("2006-01-02")),
				})
			}
		}
	}

	// Gender: one of unspecified, female, male, other
	if _, ok := StringToGender(s.Gender); !ok {
		violations = append(violations, ValidationError{
			Field:   "gender",
			Message: "must be one of: unspecified, female, male, other",
		})
	}

	// Email: 3..64 bytes; exactly one '@'; no whitespace
	if len(s.Email) < 3 || len(s.Email) > 64 {
		violations = append(violations, ValidationError{
			Field:   "email",
			Message: "byte length must be between 3 and 64",
		})
	} else {
		atCount := strings.Count(s.Email, "@")
		if atCount != 1 {
			violations = append(violations, ValidationError{
				Field:   "email",
				Message: "must contain exactly one '@'",
			})
		} else if strings.ContainsAny(s.Email, " \t\r\n") {
			violations = append(violations, ValidationError{
				Field:   "email",
				Message: "cannot contain whitespace",
			})
		}
	}

	// Phone: 0..20 ASCII chars from 0-9 + - ( ) space
	if len(s.Phone) > 20 {
		violations = append(violations, ValidationError{
			Field:   "phone",
			Message: "length must be at most 20 characters",
		})
	} else {
		for _, c := range s.Phone {
			if !strings.ContainsRune("0123456789+-() ", c) {
				violations = append(violations, ValidationError{
					Field:   "phone",
					Message: "contains invalid character (allowed: digits, +, -, (, ), space)",
				})
				break
			}
		}
	}

	// Department: 1..24 bytes UTF-8
	if len(s.Department) < 1 || len(s.Department) > 24 {
		violations = append(violations, ValidationError{
			Field:   "department",
			Message: "byte length must be between 1 and 24",
		})
	} else if strings.ContainsRune(s.Department, 0) {
		violations = append(violations, ValidationError{
			Field:   "department",
			Message: "cannot contain NUL bytes",
		})
	} else if !utf8.ValidString(s.Department) {
		violations = append(violations, ValidationError{
			Field:   "department",
			Message: "must be valid UTF-8",
		})
	}

	// EnrollmentYear: 1950..2100
	if s.EnrollmentYear < 1950 || s.EnrollmentYear > 2100 {
		violations = append(violations, ValidationError{
			Field:   "enrollment_year",
			Message: "must be between 1950 and 2100",
		})
	}

	// GPA: 0.00..4.00
	if math.IsNaN(s.GPA) || s.GPA < 0.0 || s.GPA > 4.000001 {
		violations = append(violations, ValidationError{
			Field:   "gpa",
			Message: "must be between 0.00 and 4.00",
		})
	}

	if len(violations) > 0 {
		return &ErrValidation{Violations: violations}
	}
	return nil
}

func validateName(field, val string, maxBytes int) *ValidationError {
	if len(val) < 1 || len(val) > maxBytes {
		return &ValidationError{
			Field:   field,
			Message: fmt.Sprintf("byte length must be between 1 and %d", maxBytes),
		}
	}
	if strings.ContainsRune(val, 0) {
		return &ValidationError{
			Field:   field,
			Message: "cannot contain NUL bytes",
		}
	}
	if !utf8.ValidString(val) {
		return &ValidationError{
			Field:   field,
			Message: "must be valid UTF-8",
		}
	}
	for _, r := range val {
		if unicode.IsControl(r) {
			return &ValidationError{
				Field:   field,
				Message: "cannot contain control characters",
			}
		}
	}
	return nil
}

