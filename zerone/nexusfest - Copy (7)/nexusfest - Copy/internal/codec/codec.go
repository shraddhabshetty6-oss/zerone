// Package codec implements canonical serialization and deserialization per ARCHITECTURE.md §C.3, §C.4, §F.2.
package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"studentdb/internal/constants"
	"studentdb/internal/crc"
	"studentdb/internal/record"
)

var (
	// ErrChecksumMismatch is returned when a record slot or structure CRC does not match.
	ErrChecksumMismatch = errors.New("checksum mismatch (CHECKSUM_MISMATCH)")

	// ErrInvalidRecord is returned when a record fails canonical structure or padding rules.
	ErrInvalidRecord = errors.New("invalid record layout (INVALID_RECORD)")
)

// SlotState constants.
const (
	SlotStateFree    uint8 = 0
	SlotStateActive  uint8 = 1
	SlotStateDeleted uint8 = 2
	SlotStateCorrupt uint8 = 3
)

// ClassifySlot inspects a 256-byte buffer and classifies its state per §C.3.
func ClassifySlot(buf []byte) (state uint8, reason string) {
	if len(buf) != int(constants.RecordSize) {
		return SlotStateCorrupt, "wrong slot length"
	}

	// 1. All-zero check (FREE)
	isZero := true
	for _, b := range buf {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return SlotStateFree, ""
	}

	// 2. CRC32 check
	storedCRC := binary.LittleEndian.Uint32(buf[252:256])
	computedCRC := crc.Sum(buf[:252])
	if storedCRC != computedCRC {
		return SlotStateCorrupt, fmt.Sprintf("CRC mismatch: stored 0x%08X != computed 0x%08X", storedCRC, computedCRC)
	}

	// 3. Magic and format
	magic := binary.LittleEndian.Uint16(buf[0:2])
	if magic != constants.RecordMagic {
		return SlotStateCorrupt, fmt.Sprintf("bad magic: 0x%04X", magic)
	}
	recFormat := buf[2]
	if recFormat != constants.RecordFormat {
		return SlotStateCorrupt, fmt.Sprintf("bad record format: %d", recFormat)
	}

	status := buf[3]
	if status != constants.StatusActive && status != constants.StatusDeleted {
		return SlotStateCorrupt, fmt.Sprintf("invalid status: %d", status)
	}

	// 4. Check reserved bytes
	if buf[85] != 0 || buf[86] != 0 || buf[87] != 0 {
		return SlotStateCorrupt, "non-zero reserved bytes at offset 85..87"
	}
	for i := 216; i < 252; i++ {
		if buf[i] != 0 {
			return SlotStateCorrupt, fmt.Sprintf("non-zero reserved byte at offset %d", i)
		}
	}

	studentID := binary.LittleEndian.Uint32(buf[4:8])
	if studentID == 0 {
		return SlotStateCorrupt, "student_id is 0"
	}

	if status == constants.StatusDeleted {
		// In a tombstone, all user data fields must be zero
		for i := 16; i < 85; i++ {
			if buf[i] != 0 {
				return SlotStateCorrupt, fmt.Sprintf("tombstone has non-zero byte at offset %d", i)
			}
		}
		for i := 88; i < 200; i++ {
			if buf[i] != 0 {
				return SlotStateCorrupt, fmt.Sprintf("tombstone has non-zero byte at offset %d", i)
			}
		}
		return SlotStateDeleted, ""
	}

	// Active slot canonical string padding checks
	if err := checkNulPadding(buf[16:48]); err != nil {
		return SlotStateCorrupt, fmt.Sprintf("first_name padding: %v", err)
	}
	if err := checkNulPadding(buf[48:80]); err != nil {
		return SlotStateCorrupt, fmt.Sprintf("last_name padding: %v", err)
	}
	if err := checkNulPadding(buf[88:152]); err != nil {
		return SlotStateCorrupt, fmt.Sprintf("email padding: %v", err)
	}
	if err := checkNulPadding(buf[152:172]); err != nil {
		return SlotStateCorrupt, fmt.Sprintf("phone padding: %v", err)
	}
	if err := checkNulPadding(buf[172:196]); err != nil {
		return SlotStateCorrupt, fmt.Sprintf("department padding: %v", err)
	}

	// Gender validation
	gender := buf[84]
	if gender > record.GenderOther {
		return SlotStateCorrupt, fmt.Sprintf("invalid gender code: %d", gender)
	}

	return SlotStateActive, ""
}

func checkNulPadding(field []byte) error {
	nulIdx := bytes.IndexByte(field, 0)
	if nulIdx >= 0 {
		for i := nulIdx + 1; i < len(field); i++ {
			if field[i] != 0 {
				return errors.New("non-zero byte after first NUL terminator")
			}
		}
	}
	return nil
}

// EncodeStudent serializes an active Student into a canonical 256-byte slot image with CRC32 stamped.
func EncodeStudent(s *record.Student, recordVersion uint64) ([256]byte, error) {
	var buf [256]byte

	binary.LittleEndian.PutUint16(buf[0:2], constants.RecordMagic)
	buf[2] = constants.RecordFormat
	buf[3] = constants.StatusActive

	binary.LittleEndian.PutUint32(buf[4:8], s.StudentID)
	binary.LittleEndian.PutUint64(buf[8:16], recordVersion)

	// Strings
	copy(buf[16:48], s.FirstName)
	copy(buf[48:80], s.LastName)

	days, err := record.DaysSinceEpoch(s.DateOfBirth)
	if err != nil {
		return buf, fmt.Errorf("%w: invalid date_of_birth: %v", ErrInvalidRecord, err)
	}
	binary.LittleEndian.PutUint32(buf[80:84], uint32(days))

	gCode, _ := record.StringToGender(s.Gender)
	buf[84] = gCode
	// bytes 85..87 are zero (Reserved)

	copy(buf[88:152], s.Email)
	copy(buf[152:172], s.Phone)
	copy(buf[172:196], s.Department)

	binary.LittleEndian.PutUint16(buf[196:198], s.EnrollmentYear)
	gpa100 := uint16(math.Round(s.GPA * 100))
	binary.LittleEndian.PutUint16(buf[198:200], gpa100)

	createdSec := s.CreatedAt.UTC().Unix()
	if createdSec < 0 {
		createdSec = 0
	}
	binary.LittleEndian.PutUint64(buf[200:208], uint64(createdSec))

	updatedSec := s.UpdatedAt.UTC().Unix()
	if updatedSec < 0 {
		updatedSec = 0
	}
	binary.LittleEndian.PutUint64(buf[208:216], uint64(updatedSec))

	// bytes 216..251 are zero (Reserved)

	// Stamp CRC32
	checksum := crc.Sum(buf[:252])
	binary.LittleEndian.PutUint32(buf[252:256], checksum)

	return buf, nil
}

// EncodeTombstone encodes a DELETED tombstone slot.
func EncodeTombstone(studentID uint32, recordVersion uint64) [256]byte {
	var buf [256]byte

	binary.LittleEndian.PutUint16(buf[0:2], constants.RecordMagic)
	buf[2] = constants.RecordFormat
	buf[3] = constants.StatusDeleted
	binary.LittleEndian.PutUint32(buf[4:8], studentID)
	binary.LittleEndian.PutUint64(buf[8:16], recordVersion)

	// All other bytes up to 252 remain 0

	checksum := crc.Sum(buf[:252])
	binary.LittleEndian.PutUint32(buf[252:256], checksum)
	return buf
}

// DecodeStudent decodes a 256-byte buffer into a Student record.
func DecodeStudent(buf []byte) (*record.Student, uint8, uint64, error) {
	state, reason := ClassifySlot(buf)
	if state == SlotStateCorrupt {
		storedCRC := binary.LittleEndian.Uint32(buf[252:256])
		computedCRC := crc.Sum(buf[:252])
		if storedCRC != computedCRC {
			return nil, state, 0, fmt.Errorf("%w: %s", ErrChecksumMismatch, reason)
		}
		return nil, state, 0, fmt.Errorf("%w: %s", ErrInvalidRecord, reason)
	}
	if state == SlotStateFree {
		return nil, SlotStateFree, 0, nil
	}

	status := buf[3]
	studentID := binary.LittleEndian.Uint32(buf[4:8])
	recordVersion := binary.LittleEndian.Uint64(buf[8:16])

	if status == constants.StatusDeleted {
		return &record.Student{
			StudentID:     studentID,
			RecordVersion: recordVersion,
		}, SlotStateDeleted, recordVersion, nil
	}

	extractString := func(b []byte) string {
		idx := bytes.IndexByte(b, 0)
		if idx >= 0 {
			return string(b[:idx])
		}
		return string(b)
	}

	firstName := extractString(buf[16:48])
	lastName := extractString(buf[48:80])
	dobDays := int32(binary.LittleEndian.Uint32(buf[80:84]))
	dateOfBirth := record.EpochDaysToDate(dobDays)
	gender := record.GenderToString(buf[84])
	email := extractString(buf[88:152])
	phone := extractString(buf[152:172])
	dept := extractString(buf[172:196])
	enrollYear := binary.LittleEndian.Uint16(buf[196:198])
	gpa100 := binary.LittleEndian.Uint16(buf[198:200])
	gpa := float64(gpa100) / 100.0

	createdSec := int64(binary.LittleEndian.Uint64(buf[200:208]))
	updatedSec := int64(binary.LittleEndian.Uint64(buf[208:216]))

	s := &record.Student{
		StudentID:      studentID,
		FirstName:      firstName,
		LastName:       lastName,
		DateOfBirth:    dateOfBirth,
		Gender:         gender,
		Email:          email,
		Phone:          phone,
		Department:     dept,
		EnrollmentYear: enrollYear,
		GPA:            gpa,
		CreatedAt:      time.Unix(createdSec, 0).UTC(),
		UpdatedAt:      time.Unix(updatedSec, 0).UTC(),
		RecordVersion:  recordVersion,
	}

	return s, SlotStateActive, recordVersion, nil
}

// DataHeader represents the header of students.dat (4096 bytes).
type DataHeader struct {
	FormatVersion uint16
	HeaderSize    uint32
	BlockSize     uint32
	RecordSize    uint32
	SlotsPerBlock uint32
	CreatedAtUnix int64
	DatabaseUUID  [16]byte
}

// EncodeDataHeader encodes the 4096-byte header with stamped CRC32.
func EncodeDataHeader(h *DataHeader) [4096]byte {
	var buf [4096]byte
	copy(buf[0:8], []byte(constants.DataHeaderMagic))
	binary.LittleEndian.PutUint16(buf[8:10], h.FormatVersion)
	// buf[10:12] reserved 0
	binary.LittleEndian.PutUint32(buf[12:16], h.HeaderSize)
	binary.LittleEndian.PutUint32(buf[16:20], h.BlockSize)
	binary.LittleEndian.PutUint32(buf[20:24], h.RecordSize)
	binary.LittleEndian.PutUint32(buf[24:28], h.SlotsPerBlock)
	binary.LittleEndian.PutUint64(buf[28:36], uint64(h.CreatedAtUnix))
	copy(buf[36:52], h.DatabaseUUID[:])
	// bytes 52..4091 zero

	_ = crc.StampHeader(buf[:])
	return buf
}

// DecodeDataHeader decodes and validates the 4096-byte header.
func DecodeDataHeader(buf []byte) (*DataHeader, error) {
	if len(buf) < int(constants.HeaderSize) {
		return nil, errors.New("data header buffer too small")
	}
	if !crc.VerifyHeader(buf) {
		return nil, ErrChecksumMismatch
	}
	if string(buf[0:8]) != constants.DataHeaderMagic {
		return nil, errors.New("bad data file magic")
	}
	version := binary.LittleEndian.Uint16(buf[8:10])
	if version != constants.FormatVersion {
		return nil, fmt.Errorf("unsupported format version: %d", version)
	}

	h := &DataHeader{
		FormatVersion: version,
		HeaderSize:    binary.LittleEndian.Uint32(buf[12:16]),
		BlockSize:     binary.LittleEndian.Uint32(buf[16:20]),
		RecordSize:    binary.LittleEndian.Uint32(buf[20:24]),
		SlotsPerBlock: binary.LittleEndian.Uint32(buf[24:28]),
		CreatedAtUnix: int64(binary.LittleEndian.Uint64(buf[28:36])),
	}
	copy(h.DatabaseUUID[:], buf[36:52])
	return h, nil
}

// WALHeader represents the header of students.wal (32 bytes).
type WALHeader struct {
	Version       uint16
	CheckpointLSN uint64
	NextTxID      uint64
}

// EncodeWALHeader serializes the 32-byte WAL header with CRC32.
func EncodeWALHeader(h *WALHeader) [32]byte {
	var buf [32]byte
	copy(buf[0:4], []byte(constants.WALHeaderMagic))
	binary.LittleEndian.PutUint16(buf[4:6], h.Version)
	// buf[6:8] reserved
	binary.LittleEndian.PutUint64(buf[8:16], h.CheckpointLSN)
	binary.LittleEndian.PutUint64(buf[16:24], h.NextTxID)
	// buf[24:28] reserved
	_ = crc.StampWALHeader(buf[:])
	return buf
}

// DecodeWALHeader decodes and validates the 32-byte WAL header.
func DecodeWALHeader(buf []byte) (*WALHeader, error) {
	if len(buf) < int(constants.WALHeaderSize) {
		return nil, errors.New("wal header buffer too small")
	}
	if !crc.VerifyWALHeader(buf) {
		return nil, ErrChecksumMismatch
	}
	if string(buf[0:4]) != constants.WALHeaderMagic {
		return nil, errors.New("bad wal file magic")
	}
	version := binary.LittleEndian.Uint16(buf[4:6])
	if version != constants.FormatVersion {
		return nil, fmt.Errorf("unsupported wal format version: %d", version)
	}

	return &WALHeader{
		Version:       version,
		CheckpointLSN: binary.LittleEndian.Uint64(buf[8:16]),
		NextTxID:      binary.LittleEndian.Uint64(buf[16:24]),
	}, nil
}

// WALEntry represents a decoded WAL entry.
type WALEntry struct {
	LSN        uint64
	TxID       uint64
	Type       uint8 // 1=BEGIN, 2=WRITE_SLOT, 3=COMMIT
	PayloadLen uint32
	Payload    []byte
}

// EncodeWALEntry serializes a single WAL entry with CRC32 stamped.
func EncodeWALEntry(e *WALEntry) []byte {
	totalLen := 32 + len(e.Payload) + 4
	buf := make([]byte, totalLen)

	copy(buf[0:4], []byte(constants.WALEntryMagic))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(totalLen))
	binary.LittleEndian.PutUint64(buf[8:16], e.LSN)
	binary.LittleEndian.PutUint64(buf[16:24], e.TxID)
	buf[24] = e.Type
	// buf[25:28] reserved
	binary.LittleEndian.PutUint32(buf[28:32], uint32(len(e.Payload)))
	copy(buf[32:32+len(e.Payload)], e.Payload)

	_ = crc.StampWALEntry(buf)
	return buf
}

// DecodeWALEntry parses a single WAL entry from buf. Returns entry, total length consumed, and error.
func DecodeWALEntry(buf []byte) (*WALEntry, int, error) {
	if len(buf) < 36 {
		return nil, 0, errors.New("wal entry buffer too short")
	}
	if string(buf[0:4]) != constants.WALEntryMagic {
		return nil, 0, errors.New("bad wal entry magic")
	}
	totalLen := int(binary.LittleEndian.Uint32(buf[4:8]))
	if totalLen < 36 || totalLen > 1024*1024+36 { // 1 MiB max
		return nil, 0, errors.New("invalid wal entry length")
	}
	if len(buf) < totalLen {
		return nil, 0, errors.New("incomplete wal entry")
	}

	if !crc.VerifyWALEntry(buf[:totalLen]) {
		return nil, totalLen, ErrChecksumMismatch
	}

	lsn := binary.LittleEndian.Uint64(buf[8:16])
	txID := binary.LittleEndian.Uint64(buf[16:24])
	entryType := buf[24]
	payloadLen := binary.LittleEndian.Uint32(buf[28:32])

	if int(payloadLen) != totalLen-36 {
		return nil, totalLen, errors.New("wal entry payload length mismatch")
	}

	payload := make([]byte, payloadLen)
	copy(payload, buf[32:32+payloadLen])

	return &WALEntry{
		LSN:        lsn,
		TxID:       txID,
		Type:       entryType,
		PayloadLen: payloadLen,
		Payload:    payload,
	}, totalLen, nil
}

