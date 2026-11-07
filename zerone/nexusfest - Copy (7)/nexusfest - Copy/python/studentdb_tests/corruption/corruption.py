"""PY-10: Binary Corruption Simulator.

Performs controlled offline mutations on disposable stopped database files.
Generates reproducible damage manifests.
"""

from __future__ import annotations
from dataclasses import dataclass, field
import os
from typing import List, Optional


@dataclass
class DamageManifest:
    file: str
    offset: int
    length: int
    kind: str  # "bit_flip", "byte_overwrite", "zero_fill_slot", "corrupt_crc", "truncate", "tail_garbage", "midlog"
    affected_slots: List[int] = field(default_factory=list)
    affected_ids: List[int] = field(default_factory=list)


class CorruptionSimulator:
    """Offline binary file mutator for stopped databases."""

    def __init__(self, workspace_path: str) -> None:
        self.workspace_path = os.path.abspath(workspace_path)
        # Path guard: refuse to mutate anything in golden datasets
        if "golden" in self.workspace_path:
            raise RuntimeError(f"Safety guard: refusing to mutate golden data path: {workspace_path}")

    def _resolve(self, filename: str) -> str:
        p = os.path.join(self.workspace_path, filename)
        if not os.path.exists(p):
            raise FileNotFoundError(f"File not found: {p}")
        return p

    def bit_flip(self, filename: str, offset: int, bit: int = 0) -> DamageManifest:
        path = self._resolve(filename)
        with open(path, "r+b") as f:
            f.seek(offset)
            b = f.read(1)
            if not b:
                raise ValueError("Offset beyond file size")
            corrupted = bytes([b[0] ^ (1 << bit)])
            f.seek(offset)
            f.write(corrupted)

        affected_slot = (offset - 4096) // 256 if filename.endswith(".dat") and offset >= 4096 else None
        return DamageManifest(
            file=filename,
            offset=offset,
            length=1,
            kind="bit_flip",
            affected_slots=[affected_slot] if affected_slot is not None else [],
        )

    def byte_overwrite(self, filename: str, offset: int, data: bytes) -> DamageManifest:
        path = self._resolve(filename)
        with open(path, "r+b") as f:
            f.seek(offset)
            f.write(data)

        affected_slot = (offset - 4096) // 256 if filename.endswith(".dat") and offset >= 4096 else None
        return DamageManifest(
            file=filename,
            offset=offset,
            length=len(data),
            kind="byte_overwrite",
            affected_slots=[affected_slot] if affected_slot is not None else [],
        )

    def zero_fill_slot(self, slot: int) -> DamageManifest:
        """Zero out an entire 256-byte slot in students.dat."""
        path = self._resolve("students.dat")
        offset = 4096 + slot * 256
        zeros = b"\x00" * 256
        with open(path, "r+b") as f:
            f.seek(offset)
            f.write(zeros)

        return DamageManifest(
            file="students.dat",
            offset=offset,
            length=256,
            kind="zero_fill_slot",
            affected_slots=[slot],
        )

    def corrupt_crc(self, slot: int) -> DamageManifest:
        """Corrupt the 4-byte CRC of a slot in students.dat."""
        path = self._resolve("students.dat")
        offset = 4096 + slot * 256 + 252
        with open(path, "r+b") as f:
            f.seek(offset)
            f.write(b"\xDE\xAD\xBE\xEF")

        return DamageManifest(
            file="students.dat",
            offset=offset,
            length=4,
            kind="corrupt_crc",
            affected_slots=[slot],
        )

    def truncate_file(self, filename: str, new_size: int) -> DamageManifest:
        """Truncate a database file to new_size bytes."""
        path = self._resolve(filename)
        orig_size = os.path.getsize(path)
        with open(path, "r+b") as f:
            f.truncate(new_size)

        return DamageManifest(
            file=filename,
            offset=new_size,
            length=max(0, orig_size - new_size),
            kind="truncate",
        )

    def append_tail_garbage(self, filename: str, garbage: bytes) -> DamageManifest:
        """Append garbage bytes to the end of a file (e.g. WAL tail)."""
        path = self._resolve(filename)
        orig_size = os.path.getsize(path)
        with open(path, "ab") as f:
            f.write(garbage)

        return DamageManifest(
            file=filename,
            offset=orig_size,
            length=len(garbage),
            kind="tail_garbage",
        )

