// Package crc implements the CRC32-IEEE checksum engine used across BinStudentDB.
package crc

import (
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"

	"studentdb/internal/constants"
)

var (
	// ErrBufferTooSmall is returned when a buffer is shorter than the expected structure size.
	ErrBufferTooSmall = errors.New("crc: buffer too small")
)

// Table is the IEEE CRC-32 polynomial table.
var Table = crc32.IEEETable

// Sum computes the IEEE CRC32 checksum of data.
func Sum(data []byte) uint32 {
	return crc32.Checksum(data, Table)
}

// New returns a new hash.Hash32 computing the IEEE CRC32 checksum.
func New() hash.Hash32 {
	return crc32.New(Table)
}

// Verify checks whether the IEEE CRC32 of data matches stored.
func Verify(data []byte, stored uint32) bool {
	return Sum(data) == stored
}

// StampSlot computes the CRC32 of the first 252 bytes of slot (which must be 256 bytes)
// and writes the little-endian uint32 into bytes 252..256.
func StampSlot(slot []byte) error {
	if len(slot) < int(constants.RecordSize) {
		return ErrBufferTooSmall
	}
	crc := Sum(slot[:constants.RecordCRCOffset])
	binary.LittleEndian.PutUint32(slot[constants.RecordCRCOffset:constants.RecordSize], crc)
	return nil
}

// VerifySlot verifies the CRC32 stored at bytes 252..256 against bytes 0..252.
func VerifySlot(slot []byte) bool {
	if len(slot) < int(constants.RecordSize) {
		return false
	}
	stored := binary.LittleEndian.Uint32(slot[constants.RecordCRCOffset:constants.RecordSize])
	return Verify(slot[:constants.RecordCRCOffset], stored)
}

// StampHeader computes the CRC32 over [0, 4092) and writes it into [4092, 4096).
func StampHeader(header []byte) error {
	if len(header) < int(constants.HeaderSize) {
		return ErrBufferTooSmall
	}
	crc := Sum(header[:constants.HeaderSize-4])
	binary.LittleEndian.PutUint32(header[constants.HeaderSize-4:constants.HeaderSize], crc)
	return nil
}

// VerifyHeader verifies the header CRC32 over [0, 4092) against [4092, 4096).
func VerifyHeader(header []byte) bool {
	if len(header) < int(constants.HeaderSize) {
		return false
	}
	stored := binary.LittleEndian.Uint32(header[constants.HeaderSize-4:constants.HeaderSize])
	return Verify(header[:constants.HeaderSize-4], stored)
}

// StampWALHeader computes CRC32 over [0, 28) and writes it into [28, 32).
func StampWALHeader(header []byte) error {
	if len(header) < int(constants.WALHeaderSize) {
		return ErrBufferTooSmall
	}
	crc := Sum(header[:constants.WALHeaderSize-4])
	binary.LittleEndian.PutUint32(header[constants.WALHeaderSize-4:constants.WALHeaderSize], crc)
	return nil
}

// VerifyWALHeader verifies CRC32 over [0, 28) against [28, 32).
func VerifyWALHeader(header []byte) bool {
	if len(header) < int(constants.WALHeaderSize) {
		return false
	}
	stored := binary.LittleEndian.Uint32(header[constants.WALHeaderSize-4:constants.WALHeaderSize])
	return Verify(header[:constants.WALHeaderSize-4], stored)
}

// StampWALEntry computes CRC32 over [0, len(entry)-4) and writes it into the final 4 bytes.
func StampWALEntry(entry []byte) error {
	if len(entry) < 36 { // minimum entry size: BEGIN is 36 bytes
		return ErrBufferTooSmall
	}
	crc := Sum(entry[:len(entry)-4])
	binary.LittleEndian.PutUint32(entry[len(entry)-4:], crc)
	return nil
}

// VerifyWALEntry verifies CRC32 over [0, len(entry)-4) against the final 4 bytes.
func VerifyWALEntry(entry []byte) bool {
	if len(entry) < 36 {
		return false
	}
	stored := binary.LittleEndian.Uint32(entry[len(entry)-4:])
	return Verify(entry[:len(entry)-4], stored)
}

