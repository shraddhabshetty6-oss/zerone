package record_test

import (
	"testing"
	"time"

	"studentdb/internal/record"
)

func TestStudentValidation(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	valid := record.Student{
		StudentID:      20240001,
		FirstName:      "Asha",
		LastName:       "Rao",
		DateOfBirth:    "2004-05-17",
		Gender:         "female",
		Email:          "asha.rao@example.com",
		Phone:          "+91-9000000000",
		Department:     "Computer Science",
		EnrollmentYear: 2022,
		GPA:            3.75,
	}

	if err := valid.ValidateWithClock(clock); err != nil {
		t.Fatalf("Expected valid student, got: %v", err)
	}

	// Boundary GPA: 0.00 and 4.00
	valid.GPA = 0.00
	if err := valid.ValidateWithClock(clock); err != nil {
		t.Fatalf("GPA 0.00 should be valid, got: %v", err)
	}
	valid.GPA = 4.00
	if err := valid.ValidateWithClock(clock); err != nil {
		t.Fatalf("GPA 4.00 should be valid, got: %v", err)
	}

	// Invalid GPA: 4.01
	valid.GPA = 4.01
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("GPA 4.01 should fail validation")
	}

	// Invalid StudentID: 0
	valid.GPA = 3.50
	valid.StudentID = 0
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("StudentID 0 should fail validation")
	}
	valid.StudentID = 20240001

	// Invalid Date: Feb 30
	valid.DateOfBirth = "2004-02-30"
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("Invalid date Feb 30 should fail validation")
	}

	// Future Date
	valid.DateOfBirth = "2030-01-01"
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("Future DOB should fail validation")
	}
	valid.DateOfBirth = "2004-05-17"

	// Invalid Email: multiple @
	valid.Email = "asha@rao@example.com"
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("Multiple @ in email should fail validation")
	}

	// Invalid Email: spaces
	valid.Email = "asha rao@example.com"
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("Whitespace in email should fail validation")
	}
	valid.Email = "asha.rao@example.com"

	// NUL in string
	valid.FirstName = "Asha\x00Rao"
	if err := valid.ValidateWithClock(clock); err == nil {
		t.Fatal("NUL in first name should fail validation")
	}
}

func TestDateEpochRoundTrip(t *testing.T) {
	dates := []string{"1970-01-01", "2000-01-01", "2024-05-17", "1900-01-01"}
	for _, d := range dates {
		days, err := record.DaysSinceEpoch(d)
		if err != nil {
			t.Fatalf("DaysSinceEpoch(%s) error: %v", d, err)
		}
		got := record.EpochDaysToDate(days)
		if got != d {
			t.Fatalf("EpochDaysToDate(%d) = %s; want %s", days, got, d)
		}
	}
}

