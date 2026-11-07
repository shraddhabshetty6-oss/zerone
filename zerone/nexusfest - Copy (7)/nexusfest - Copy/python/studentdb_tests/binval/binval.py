"""PY-04: Binary Validator.

Independent read-only format decoder of students.dat, students.wal, and students.idx.
Uses Python struct and zlib.crc32 (IEEE 802.3 polynomial).
Opens files strictly in read-only mode ('rb').
"""

from __future__ import annotations
from dataclasses import dataclass, field
import datetime
import os
import struct
from typing import Any, Dict, List, Optional, Tuple
import zlib


def crc32_ieee(data: bytes) -> int:
    """Compute unsigned CRC32-IEEE checksum."""
    return zlib.crc32(data) & 0xFFFFFFFF


@dataclass
class SlotInfo:
    slot: int
    offset: int
    state: str  # "FREE", "ACTIVE", "DELETED", "CORRUPT"
    student_id: Optional[int] = None
    record_version: Optional[int] = None
    data: Optional[Dict[str, Any]] = None
    reason: Optional[str] = None


@dataclass
class DatScanResult:
    valid_header: bool
    format_version: int
    database_uuid: str
    slot_count: int
    aligned: bool
    slots: List[SlotInfo] = field(default_factory=list)
    active_count: int = 0
    deleted_count: int = 0
    free_count: int = 0
    corrupt_count: int = 0
    errors: List[str] = field(default_factory=list)


@dataclass
class WalEntryInfo:
    offset: int
    length: int
    lsn: int
    tx_id: int
    entry_type: int  # 1=BEGIN, 2=WRITE_SLOT, 3=COMMIT
    payload: bytes


@dataclass
class WalScanResult:
    valid_header: bool
    checkpoint_lsn: int
    next_tx_id: int
    entries: List[WalEntryInfo] = field(default_factory=list)
    torn_tail_bytes: int = 0
    midlog_corruption: bool = False
    errors: List[str] = field(default_factory=list)


@dataclass
class IdxScanResult:
    valid: bool
    entry_count: int
    checkpoint_lsn: int
    slot_count: int
    entries: List[Tuple[int, int]] = field(default_factory=list)  # (student_id, slot)
    errors: List[str] = field(default_factory=list)


def parse_dat_file(path: str) -> DatScanResult:
    """Read and validate students.dat."""
    res = DatScanResult(
        valid_header=False,
        format_version=0,
        database_uuid="",
        slot_count=0,
        aligned=False,
    )

    if not os.path.exists(path):
        res.errors.append(f"File not found: {path}")
        return res

    file_size = os.path.getsize(path)
    if file_size < 4096:
        res.errors.append("File size smaller than header (4096 bytes)")
        return res

    with open(path, "rb") as f:
        header_bytes = f.read(4096)
        if len(header_bytes) != 4096:
            res.errors.append("Failed to read header")
            return res

        # Header CRC32 check over [0, 4092)
        stored_crc = struct.unpack("<I", header_bytes[4092:4096])[0]
        calc_crc = crc32_ieee(header_bytes[:4092])
        if stored_crc != calc_crc:
            res.errors.append(f"Header CRC mismatch: stored {stored_crc:#010x} != calc {calc_crc:#010x}")
            return res

        magic = header_bytes[0:8]
        if magic != b"BSDBDATA":
            res.errors.append(f"Invalid data header magic: {magic!r}")
            return res

        version = struct.unpack("<H", header_bytes[8:10])[0]
        res.format_version = version
        uuid_bytes = header_bytes[36:52]
        res.database_uuid = uuid_bytes.hex()
        res.valid_header = True

        data_size = file_size - 4096
        res.aligned = (data_size % 4096 == 0)
        res.slot_count = data_size // 256

        # Scan each 256-byte slot
        for s in range(res.slot_count):
            slot_offset = 4096 + s * 256
            slot_bytes = f.read(256)
            if len(slot_bytes) != 256:
                break

            # 1. All-zero check (FREE)
            if slot_bytes == b"\x00" * 256:
                res.free_count += 1
                res.slots.append(SlotInfo(slot=s, offset=slot_offset, state="FREE"))
                continue

            # 2. Checksum check
            s_crc = struct.unpack("<I", slot_bytes[252:256])[0]
            c_crc = crc32_ieee(slot_bytes[:252])
            if s_crc != c_crc:
                res.corrupt_count += 1
                res.slots.append(SlotInfo(
                    slot=s,
                    offset=slot_offset,
                    state="CORRUPT",
                    reason=f"CRC mismatch: stored {s_crc:#010x} != calc {c_crc:#010x}",
                ))
                continue

            magic, rec_format, status, sid, ver = struct.unpack("<HBB I Q", slot_bytes[0:16])
            if magic != 0x5352:
                res.corrupt_count += 1
                res.slots.append(SlotInfo(slot=s, offset=slot_offset, state="CORRUPT", reason=f"Bad magic {magic:#06x}"))
                continue

            if status == 2:  # DELETED (tombstone)
                res.deleted_count += 1
                res.slots.append(SlotInfo(slot=s, offset=slot_offset, state="DELETED", student_id=sid, record_version=ver))
                continue

            if status != 1:  # Unknown status
                res.corrupt_count += 1
                res.slots.append(SlotInfo(slot=s, offset=slot_offset, state="CORRUPT", reason=f"Unknown status {status}"))
                continue

            # ACTIVE slot: decode fields
            first_name = _extract_str(slot_bytes[16:48])
            last_name = _extract_str(slot_bytes[48:80])
            dob_days = struct.unpack("<i", slot_bytes[80:84])[0]
            dob_str = _days_to_date(dob_days)
            gender_code = slot_bytes[84]
            gender_map = {0: "unspecified", 1: "female", 2: "male", 3: "other"}
            gender = gender_map.get(gender_code, "unknown")
            email = _extract_str(slot_bytes[88:152])
            phone = _extract_str(slot_bytes[152:172])
            dept = _extract_str(slot_bytes[172:196])
            enroll_year, gpa100 = struct.unpack("<HH", slot_bytes[196:200])

            res.active_count += 1
            res.slots.append(SlotInfo(
                slot=s,
                offset=slot_offset,
                state="ACTIVE",
                student_id=sid,
                record_version=ver,
                data={
                    "student_id": sid,
                    "first_name": first_name,
                    "last_name": last_name,
                    "date_of_birth": dob_str,
                    "gender": gender,
                    "email": email,
                    "phone": phone,
                    "department": dept,
                    "enrollment_year": enroll_year,
                    "gpa": round(gpa100 / 100.0, 2),
                },
            ))

    return res


def parse_wal_file(path: str) -> WalScanResult:
    """Read and validate students.wal."""
    res = WalScanResult(valid_header=False, checkpoint_lsn=0, next_tx_id=0)

    if not os.path.exists(path):
        res.errors.append("File not found")
        return res

    with open(path, "rb") as f:
        data = f.read()

    if len(data) < 32:
        res.errors.append("WAL file too small (< 32 bytes)")
        return res

    stored_crc = struct.unpack("<I", data[28:32])[0]
    calc_crc = crc32_ieee(data[:28])
    if stored_crc != calc_crc:
        res.errors.append("WAL header CRC mismatch")
        return res

    if data[:4] != b"BSWL":
        res.errors.append("Invalid WAL header magic")
        return res

    res.valid_header = True
    res.checkpoint_lsn, res.next_tx_id = struct.unpack("<QQ", data[8:24])

    offset = 32
    expected_lsn = res.checkpoint_lsn + 1

    while offset < len(data):
        rem = data[offset:]
        if len(rem) < 36:
            res.torn_tail_bytes = len(rem)
            break

        if rem[:4] != b"WALE":
            # Search for subsequent WALE marker
            if b"WALE" in rem[1:]:
                res.midlog_corruption = True
            else:
                res.torn_tail_bytes = len(rem)
            break

        total_len = struct.unpack("<I", rem[4:8])[0]
        if total_len < 36 or total_len > len(rem):
            if b"WALE" in rem[4:]:
                res.midlog_corruption = True
            else:
                res.torn_tail_bytes = len(rem)
            break

        entry_bytes = rem[:total_len]
        entry_crc = struct.unpack("<I", entry_bytes[-4:])[0]
        if crc32_ieee(entry_bytes[:-4]) != entry_crc:
            if b"WALE" in rem[4:]:
                res.midlog_corruption = True
            else:
                res.torn_tail_bytes = len(rem)
            break

        lsn, tx_id, etype = struct.unpack("<QQB", entry_bytes[8:25])
        payload_len = struct.unpack("<I", entry_bytes[28:32])[0]
        payload = entry_bytes[32:32 + payload_len]

        res.entries.append(WalEntryInfo(
            offset=offset,
            length=total_len,
            lsn=lsn,
            tx_id=tx_id,
            entry_type=etype,
            payload=payload,
        ))
        offset += total_len
        expected_lsn += 1

    return res


def parse_idx_file(path: str) -> IdxScanResult:
    """Read and validate students.idx."""
    res = IdxScanResult(valid=False, entry_count=0, checkpoint_lsn=0, slot_count=0)

    if not os.path.exists(path):
        res.errors.append("File not found")
        return res

    with open(path, "rb") as f:
        data = f.read()

    if len(data) < 36:
        res.errors.append("Index file too small")
        return res

    stored_crc = struct.unpack("<I", data[-4:])[0]
    calc_crc = crc32_ieee(data[:-4])
    if stored_crc != calc_crc:
        res.errors.append("Index trailer CRC mismatch")
        return res

    if data[:4] != b"BSIX":
        res.errors.append("Invalid index magic")
        return res

    entry_count, cp_lsn, slots = struct.unpack("<QQQ", data[8:32])
    expected_size = 32 + entry_count * 8 + 4
    if len(data) != expected_size:
        res.errors.append("Index size mismatch")
        return res

    res.valid = True
    res.entry_count = entry_count
    res.checkpoint_lsn = cp_lsn
    res.slot_count = slots

    offset = 32
    for _ in range(entry_count):
        sid, slot = struct.unpack("<II", data[offset:offset + 8])
        res.entries.append((sid, slot))
        offset += 8

    return res


def _extract_str(buf: bytes) -> str:
    nul_pos = buf.find(b"\x00")
    if nul_pos >= 0:
        return buf[:nul_pos].decode("utf-8", errors="replace")
    return buf.decode("utf-8", errors="replace")


def _days_to_date(days: int) -> str:
    epoch = datetime.date(1970, 1, 1)
    d = epoch + datetime.timedelta(days=days)
    return d.isoformat()

