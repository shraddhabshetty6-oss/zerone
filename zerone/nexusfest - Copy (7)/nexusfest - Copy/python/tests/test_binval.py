"""Unit tests for PY-04 Binary Validator."""

import struct
import tempfile
import unittest
import zlib
from studentdb_tests.binval import crc32_ieee, parse_dat_file, parse_wal_file, parse_idx_file


class TestBinval(unittest.TestCase):
    def test_crc32_ieee(self):
        self.assertEqual(crc32_ieee(b"123456789"), 0xCBF43926)
        self.assertEqual(crc32_ieee(b""), 0)

    def test_dat_file_parser_free_and_active_slots(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            dat_path = f"{tmpdir}/students.dat"

            # Build a valid 4096-byte header
            header = bytearray(4096)
            header[0:8] = b"BSDBDATA"
            struct.pack_into("<H", header, 8, 1)  # version 1
            struct.pack_into("<I", header, 12, 4096)
            struct.pack_into("<I", header, 16, 4096)
            struct.pack_into("<I", header, 20, 256)
            struct.pack_into("<I", header, 24, 16)
            struct.pack_into("<Q", header, 28, 1700000000)
            header[36:52] = b"\x01" * 16  # uuid
            # Stamp header CRC at [4092:4096]
            h_crc = crc32_ieee(header[:4092])
            struct.pack_into("<I", header, 4092, h_crc)

            # Slot 0: FREE (256 zeros)
            slot0 = bytes(256)

            # Slot 1: ACTIVE student record
            slot1 = bytearray(256)
            struct.pack_into("<H", slot1, 0, 0x5352)  # Magic
            slot1[2] = 1                              # RecordFormat
            slot1[3] = 1                              # StatusActive
            struct.pack_into("<I", slot1, 4, 20240001) # StudentID
            struct.pack_into("<Q", slot1, 8, 10)       # RecordVersion
            def put(offset: int, val: bytes) -> None:
                slot1[offset:offset+len(val)] = val

            put(16, b"Asha")
            put(48, b"Rao")
            struct.pack_into("<i", slot1, 80, 12555)  # DateOfBirth days
            slot1[84] = 1                             # female
            put(88, b"asha.rao@example.com")
            put(152, b"+91-9000000000")
            put(172, b"Computer Science")
            struct.pack_into("<H", slot1, 196, 2022)  # EnrollmentYear
            struct.pack_into("<H", slot1, 198, 375)   # GPAx100
            s1_crc = crc32_ieee(slot1[:252])
            struct.pack_into("<I", slot1, 252, s1_crc)

            # Pad to block boundary (16 slots = 4096 bytes)
            padding_slots = bytes(256 * 14)

            with open(dat_path, "wb") as f:
                f.write(header)
                f.write(slot0)
                f.write(slot1)
                f.write(padding_slots)

            res = parse_dat_file(dat_path)
            self.assertTrue(res.valid_header)
            self.assertEqual(res.slot_count, 16)
            self.assertEqual(res.free_count, 15)
            self.assertEqual(res.active_count, 1)

            active_slot = next(s for s in res.slots if s.state == "ACTIVE")
            self.assertEqual(active_slot.student_id, 20240001)
            self.assertEqual(active_slot.data["first_name"], "Asha")
            self.assertEqual(active_slot.data["gpa"], 3.75)


if __name__ == "__main__":
    unittest.main()
