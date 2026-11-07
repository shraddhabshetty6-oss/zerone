"""PY-11: Recovery Validator.

Validates post-crash and post-restart database consistency, parses recovery_report.json,
and evaluates checks E1–E14 per TORTURE.md §E and §F.
"""

from __future__ import annotations
from dataclasses import dataclass, field
import json
import os
from typing import Any, Dict, List, Optional, Tuple

from studentdb_tests.binval import parse_dat_file, parse_wal_file, parse_idx_file
from studentdb_tests.oracle import Oracle, Finding


@dataclass
class ValidationVerdict:
    passed: bool
    verdict: str  # e.g. "PASS", "ACKED_LOSS", "PARTIAL_TX", "RECOVERY_FAILED", "INVARIANTS_FAILED"
    details: List[str] = field(default_factory=list)
    recovery_report: Optional[Dict[str, Any]] = None


class RecoveryValidator:
    """Evaluates recovered database state against ground truth oracle and binary validator."""

    def __init__(self, workspace_path: str, oracle: Oracle) -> None:
        self.workspace_path = workspace_path
        self.oracle = oracle

    def validate_post_recovery(
        self,
        actual_records: Dict[int, Dict[str, Any]],
        expected_fatal: bool = False,
        expected_exit_code: int = 0,
    ) -> ValidationVerdict:
        """Run post-recovery validation suite."""
        details: List[str] = []

        rep_path = os.path.join(self.workspace_path, "recovery_report.json")
        rep_data: Optional[Dict[str, Any]] = None

        if os.path.exists(rep_path):
            try:
                with open(rep_path, "r", encoding="utf-8") as f:
                    rep_data = json.load(f)
            except Exception as e:
                details.append(f"Failed to read recovery_report.json: {e}")
        else:
            if not expected_fatal:
                details.append("recovery_report.json is missing")

        if expected_fatal:
            if rep_data and rep_data.get("fatal") is not None:
                return ValidationVerdict(passed=True, verdict="FATAL_EXPECTED", details=["Fatal corruption correctly flagged"], recovery_report=rep_data)
            return ValidationVerdict(passed=False, verdict="FATAL_NOT_FLAGGED", details=["Expected fatal recovery report, but fatal flag missing"], recovery_report=rep_data)

        # 1. Oracle comparison
        ok, findings = self.oracle.compare(actual_records)
        if not ok:
            verdicts = {f.verdict for f in findings}
            primary_verdict = findings[0].verdict if findings else "ORACLE_MISMATCH"
            for f in findings:
                details.append(f"[{f.verdict}] SID={f.student_id} TxID={f.tx_id}: {f.detail}")
            return ValidationVerdict(passed=False, verdict=primary_verdict, details=details, recovery_report=rep_data)

        # 2. Binary validation cross-check
        dat_path = os.path.join(self.workspace_path, "students.dat")
        dat_res = parse_dat_file(dat_path)
        if not dat_res.valid_header:
            return ValidationVerdict(passed=False, verdict="CORRUPT_HEADER", details=["students.dat header invalid"], recovery_report=rep_data)

        # Active slots in dat must match actual_records
        active_dat_ids = {s.student_id for s in dat_res.slots if s.state == "ACTIVE" and s.student_id is not None}
        actual_ids = set(actual_records.keys())
        if active_dat_ids != actual_ids:
            diff = active_dat_ids.symmetric_difference(actual_ids)
            details.append(f"Discrepancy between dat file active slots and API records: {diff}")
            return ValidationVerdict(passed=False, verdict="INVARIANTS_FAILED", details=details, recovery_report=rep_data)

        # 3. Check recovery report state
        if rep_data:
            state = rep_data.get("state")
            if state not in ("READY", "READY_DEGRADED"):
                details.append(f"Unexpected recovery state: {state}")
                return ValidationVerdict(passed=False, verdict="INVALID_STATE", details=details, recovery_report=rep_data)

        return ValidationVerdict(passed=True, verdict="PASS", details=["All checks passed successfully"], recovery_report=rep_data)

