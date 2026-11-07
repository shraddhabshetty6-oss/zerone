package crc_test

import (
	"testing"

	"studentdb/internal/crc"
)

func TestCRC32KnownVectors(t *testing.T) {
	// Standard IEEE test vector
	input := []byte("123456789")
	expected := uint32(0xCBF43926)
	got := crc.Sum(input)
	if got != expected {
		t.Fatalf("Sum('123456789') = 0x%08X; want 0x%08X", got, expected)
	}

	// Empty input
	if gotEmpty := crc.Sum([]byte{}); gotEmpty != 0 {
		t.Fatalf("Sum('') = 0x%08X; want 0", gotEmpty)
	}

	// Streaming matches one-shot
	h := crc.New()
	h.Write([]byte("12345"))
	h.Write([]byte("6789"))
	if h.Sum32() != expected {
		t.Fatalf("Streaming Sum32() = 0x%08X; want 0x%08X", h.Sum32(), expected)
	}
}

func TestSlotStampingAndBitFlips(t *testing.T) {
	var buf [256]byte
	for i := 0; i < 252; i++ {
		buf[i] = byte(i % 251)
	}

	if err := crc.StampSlot(buf[:]); err != nil {
		t.Fatalf("StampSlot failed: %v", err)
	}

	if !crc.VerifySlot(buf[:]) {
		t.Fatal("VerifySlot returned false on unmodified stamped slot")
	}

	// Flip every single bit in [0, 252) and verify detection
	for byteIdx := 0; byteIdx < 252; byteIdx++ {
		for bit := 0; bit < 8; bit++ {
			corrupted := buf
			corrupted[byteIdx] ^= (1 << bit)
			if crc.VerifySlot(corrupted[:]) {
				t.Fatalf("Undetected single-bit flip at byte %d, bit %d", byteIdx, bit)
			}
		}
	}
}
