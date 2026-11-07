package codec_test

import (
	"testing"
	"time"

	"studentdb/internal/codec"
	"studentdb/internal/record"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	st := &record.Student{
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
		CreatedAt:      time.Unix(1715904000, 0).UTC(),
		UpdatedAt:      time.Unix(1715904000, 0).UTC(),
	}

	image, err := codec.EncodeStudent(st, 42)
	if err != nil {
		t.Fatalf("EncodeStudent failed: %v", err)
	}

	state, reason := codec.ClassifySlot(image[:])
	if state != codec.SlotStateActive {
		t.Fatalf("Expected ACTIVE slot, got state %d (reason: %s)", state, reason)
	}

	decoded, decState, ver, err := codec.DecodeStudent(image[:])
	if err != nil {
		t.Fatalf("DecodeStudent failed: %v", err)
	}
	if decState != codec.SlotStateActive {
		t.Fatalf("DecodeStudent state = %d, want ACTIVE", decState)
	}
	if ver != 42 {
		t.Fatalf("RecordVersion = %d, want 42", ver)
	}

	if decoded.StudentID != st.StudentID || decoded.FirstName != st.FirstName || decoded.LastName != st.LastName ||
		decoded.Email != st.Email || decoded.GPA != st.GPA || decoded.Department != st.Department {
		t.Fatalf("Decoded record fields mismatch: %+v vs %+v", decoded, st)
	}
}

func TestClassifyFreeAndTombstone(t *testing.T) {
	// 1. FREE slot (all zero)
	var freeBuf [256]byte
	state, _ := codec.ClassifySlot(freeBuf[:])
	if state != codec.SlotStateFree {
		t.Fatalf("Expected FREE slot, got %d", state)
	}

	// 2. Tombstone (DELETED)
	tombstone := codec.EncodeTombstone(20240001, 100)
	state, reason := codec.ClassifySlot(tombstone[:])
	if state != codec.SlotStateDeleted {
		t.Fatalf("Expected DELETED slot, got %d (reason: %s)", state, reason)
	}

	_, decState, ver, err := codec.DecodeStudent(tombstone[:])
	if err != nil {
		t.Fatalf("Decode tombstone failed: %v", err)
	}
	if decState != codec.SlotStateDeleted || ver != 100 {
		t.Fatalf("Tombstone decode state=%d ver=%d", decState, ver)
	}
}

func TestBitFlipCorruption(t *testing.T) {
	st := &record.Student{
		StudentID:      20240002,
		FirstName:      "John",
		LastName:       "Doe",
		DateOfBirth:    "2002-01-01",
		Gender:         "male",
		Email:          "john.doe@example.com",
		Phone:          "1234567890",
		Department:     "Physics",
		EnrollmentYear: 2020,
		GPA:            3.50,
	}

	image, err := codec.EncodeStudent(st, 1)
	if err != nil {
		t.Fatalf("EncodeStudent failed: %v", err)
	}

	// Flip a bit in the body [0, 252)
	corrupt1 := image
	corrupt1[20] ^= 0x01
	state, _ := codec.ClassifySlot(corrupt1[:])
	if state != codec.SlotStateCorrupt {
		t.Fatal("Flipped bit in slot body was not classified as CORRUPT")
	}

	// Flip a bit in the CRC field [252, 256)
	corrupt2 := image
	corrupt2[253] ^= 0x01
	state2, _ := codec.ClassifySlot(corrupt2[:])
	if state2 != codec.SlotStateCorrupt {
		t.Fatal("Flipped bit in CRC field was not classified as CORRUPT")
	}
}

