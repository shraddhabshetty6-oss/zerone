"""PY-03: Ground Truth Oracle (in-memory expected state model).

Maintains per-StudentID expected records tagged as ACKED, IN_DOUBT, or NEVER_SENT.
Compares actual database records against expectations and produces classified verdicts.
"""

from __future__ import annotations
from dataclasses import dataclass, field
from enum import Enum
from typing import Any, Dict, List, Optional, Set, Tuple


class RecordState(str, Enum):
    ACKED = "ACKED"            # Response received; MUST be present
    IN_DOUBT = "IN_DOUBT"      # Request sent, interrupted before ack; atomically present-or-absent
    NEVER_SENT = "NEVER_SENT"  # Never sent to database; MUST be absent


@dataclass
class ExpectedRecord:
    student_id: int
    data: Dict[str, Any]
    state: RecordState
    tx_id: int
    is_deleted: bool = False


@dataclass
class TxRecord:
    tx_id: int
    state: RecordState
    ops: List[Tuple[str, int, Optional[Dict[str, Any]]]] = field(default_factory=list)


@dataclass
class Finding:
    verdict: str  # e.g., "ACKED_LOSS", "PHANTOM_RECORD", "PARTIAL_TX", "UNEXPLAINED_MUTATION"
    student_id: Optional[int]
    tx_id: Optional[int]
    detail: str


class Oracle:
    """Ground truth model for BinStudentDB expected states."""

    def __init__(self) -> None:
        self.records: Dict[int, ExpectedRecord] = {}
        self.transactions: Dict[int, TxRecord] = {}
        self._next_tx_id = 1

    def next_tx_id(self) -> int:
        tid = self._next_tx_id
        self._next_tx_id += 1
        return tid

    def stage_transaction(self, tx_id: int, ops: List[Tuple[str, int, Optional[Dict[str, Any]]]]) -> None:
        """Stage a transaction with state IN_DOUBT before sending to DB."""
        self.transactions[tx_id] = TxRecord(tx_id=tx_id, state=RecordState.IN_DOUBT, ops=ops)
        for op_type, sid, student_data in ops:
            if op_type == "delete":
                self.records[sid] = ExpectedRecord(
                    student_id=sid,
                    data={},
                    state=RecordState.IN_DOUBT,
                    tx_id=tx_id,
                    is_deleted=True,
                )
            else:
                self.records[sid] = ExpectedRecord(
                    student_id=sid,
                    data=student_data or {},
                    state=RecordState.IN_DOUBT,
                    tx_id=tx_id,
                    is_deleted=False,
                )

    def acknowledge_transaction(self, tx_id: int) -> None:
        """Mark a transaction and all its staged records as ACKED."""
        if tx_id in self.transactions:
            tx = self.transactions[tx_id]
            tx.state = RecordState.ACKED
            for _, sid, _ in tx.ops:
                if sid in self.records and self.records[sid].tx_id == tx_id:
                    self.records[sid].state = RecordState.ACKED

    def record_never_sent(self, sid: int) -> None:
        """Explicitly record a student ID that was never sent."""
        self.records[sid] = ExpectedRecord(
            student_id=sid,
            data={},
            state=RecordState.NEVER_SENT,
            tx_id=0,
            is_deleted=True,
        )

    def compare(self, actual: Dict[int, Dict[str, Any]]) -> Tuple[bool, List[Finding]]:
        """Compare actual DB records (dict of student_id -> student_dict) against expectations."""
        findings: List[Finding] = []

        # 1. Check ACKED records
        for sid, exp in self.records.items():
            if exp.state == RecordState.ACKED:
                if exp.is_deleted:
                    if sid in actual:
                        findings.append(Finding(
                            verdict="ACKED_DELETE_FAILED",
                            student_id=sid,
                            tx_id=exp.tx_id,
                            detail=f"StudentID {sid} was acknowledged deleted but still present in DB",
                        ))
                else:
                    if sid not in actual:
                        findings.append(Finding(
                            verdict="ACKED_LOSS",
                            student_id=sid,
                            tx_id=exp.tx_id,
                            detail=f"Acknowledged StudentID {sid} is missing from DB",
                        ))
                    else:
                        act = actual[sid]
                        # Verify key fields match
                        for key in ("first_name", "last_name", "date_of_birth", "gender", "email", "phone", "department", "enrollment_year"):
                            if key in exp.data and exp.data[key] != act.get(key):
                                findings.append(Finding(
                                    verdict="UNEXPLAINED_MUTATION",
                                    student_id=sid,
                                    tx_id=exp.tx_id,
                                    detail=f"Field {key} mismatch: expected {exp.data[key]!r}, got {act.get(key)!r}",
                                ))

            elif exp.state == RecordState.NEVER_SENT:
                if sid in actual:
                    findings.append(Finding(
                        verdict="PHANTOM_RECORD",
                        student_id=sid,
                        tx_id=exp.tx_id,
                        detail=f"StudentID {sid} was never sent but found in DB",
                    ))

        # 2. Check IN_DOUBT transaction atomicity (all-or-nothing)
        for tx_id, tx in self.transactions.items():
            if tx.state == RecordState.IN_DOUBT:
                op_statuses: List[bool] = []
                for op_type, sid, _ in tx.ops:
                    present = sid in actual
                    if op_type == "delete":
                        op_statuses.append(not present)
                    else:
                        op_statuses.append(present)

                # Atomicity check: all ops must have succeeded OR all ops must have been aborted
                first = op_statuses[0] if op_statuses else True
                if not all(s == first for s in op_statuses):
                    findings.append(Finding(
                        verdict="PARTIAL_TX",
                        student_id=None,
                        tx_id=tx_id,
                        detail=f"Transaction {tx_id} violated all-or-nothing atomicity (partial execution: {op_statuses})",
                    ))

        # 3. Check for completely unexpected records
        known_ids = set(self.records.keys())
        for sid in actual:
            if sid not in known_ids:
                findings.append(Finding(
                    verdict="PHANTOM_RECORD",
                    student_id=sid,
                    tx_id=None,
                    detail=f"Unknown StudentID {sid} present in actual DB",
                ))

        ok = (len(findings) == 0)
        return ok, findings

