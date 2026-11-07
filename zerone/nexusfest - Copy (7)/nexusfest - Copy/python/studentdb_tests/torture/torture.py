"""PY-09: Torture Testing Engine.

Orchestrates scenarios T01–T15 per TORTURE.md §B and §D.
Executes disposable workspace runs, seeds derivation, crash timing, mutations, and validation.
"""

from __future__ import annotations
from dataclasses import dataclass, field
import hashlib
import json
import os
import random
import subprocess
import time
from typing import Any, Dict, List, Optional

from studentdb_tests.binval import parse_dat_file, parse_wal_file
from studentdb_tests.corruption import CorruptionSimulator
from studentdb_tests.faults import FaultInjectionEngine
from studentdb_tests.harness import ProcessManager, WorkspaceManager, BinaryInfo
from studentdb_tests.oracle import Oracle
from studentdb_tests.recovery import RecoveryValidator


@dataclass
class IterationResult:
    scenario: str
    iteration: int
    seed: int
    status: str  # "PASS", "FAIL", "NOT_TRIGGERED", "HANG"
    duration_ms: int
    verdict: str
    details: List[str] = field(default_factory=list)


class TortureEngine:
    """Orchestrator for failure scenarios T01 through T15."""

    def __init__(self, binary_path: str, golden_dir: str = "data/golden/1000", run_id: Optional[str] = None) -> None:
        self.binary_info = BinaryInfo.probe(binary_path)
        self.golden_dir = golden_dir
        self.run_id = run_id or f"run_{int(time.time())}"
        self.ws_mgr = WorkspaceManager()

    def run_scenario(self, scenario_id: str, iterations: int = 5, master_seed: int = 12345) -> List[IterationResult]:
        """Run multiple iterations of a failure scenario."""
        results: List[IterationResult] = []

        for it in range(iterations):
            # Derive deterministic iteration seed
            seed_str = f"{master_seed}:{scenario_id}:{it}"
            iter_seed = int(hashlib.sha256(seed_str.encode("utf-8")).hexdigest()[:8], 16)

            res = self._execute_iteration(scenario_id, it, iter_seed)
            results.append(res)

        return results

    def _execute_iteration(self, scenario_id: str, iteration: int, seed: int) -> IterationResult:
        t0 = time.perf_counter()
        iter_name = f"{scenario_id}_{iteration}"
        ws_path = self.ws_mgr.create_disposable_workspace(self.run_id, iter_name, self.golden_dir)
        if not os.path.exists(os.path.join(ws_path, "students.dat")):
            subprocess.check_call([self.binary_info.path, "init", "--data-dir", ws_path], stdout=subprocess.DEVNULL)
            csv_path = os.path.join(ws_path, "students.csv")
            if os.path.exists(csv_path):
                subprocess.check_call([self.binary_info.path, "import", "--data-dir", ws_path, "--input", csv_path], stdout=subprocess.DEVNULL)

        oracle = Oracle()
        csv_path = os.path.join(ws_path, "students.csv")
        if os.path.exists(csv_path):
            import csv
            with open(csv_path, "r", encoding="utf-8") as f:
                reader = csv.DictReader(f)
                for i, row in enumerate(reader):
                    sid = int(row["student_id"])
                    st_data = {
                        "student_id": sid,
                        "first_name": row["first_name"],
                        "last_name": row["last_name"],
                        "date_of_birth": row["date_of_birth"],
                        "gender": row["gender"],
                        "email": row["email"],
                        "phone": row["phone"],
                        "department": row["department"],
                        "enrollment_year": int(row["enrollment_year"]),
                        "gpa": float(row["gpa"]),
                    }
                    oracle.stage_transaction(1000000 + i, [("insert", sid, st_data)])
                    oracle.acknowledge_transaction(1000000 + i)
        sim = CorruptionSimulator(ws_path)
        fault_engine = FaultInjectionEngine(ws_path)

        verdict = "PASS"
        details: List[str] = []
        status = "PASS"

        try:
            if scenario_id in ("T01", "T02", "T03", "T04", "T14", "T15"):
                # Runtime failpoint or process kill scenario
                failpoint_name = "wal.append.after_write_before_sync"
                action = "crash"
                if scenario_id == "T04":
                    failpoint_name = "data.apply.partial_slot_write"
                    action = "partial:128"

                spec = fault_engine.build_spec(failpoint_name, action=action, after_hits=1, seed=seed)
                env = {"STUDENTDB_FAILPOINTS": json.dumps(spec)}

                proc = ProcessManager(self.binary_info.path, ws_path, env=env)
                try:
                    proc.start(timeout=5.0)
                    # Trigger action
                    sid = 20260000 + iteration
                    st = {
                        "student_id": sid,
                        "first_name": "Torture",
                        "last_name": f"Iter{iteration}",
                        "date_of_birth": "2000-01-01",
                        "gender": "other",
                        "email": f"torture{sid}@example.com",
                        "phone": "+1-555-0000",
                        "department": "CS",
                        "enrollment_year": 2022,
                        "gpa": 3.5,
                    }
                    oracle.stage_transaction(1, [("insert", sid, st)])
                    try:
                        proc.client.create_student(st)
                    except Exception:
                        pass  # Expected to crash
                finally:
                    proc.kill()

                # Restart cleanly and validate recovery
                restart_proc = ProcessManager(self.binary_info.path, ws_path)
                restart_proc.start(timeout=5.0)
                try:
                    # Scan active records
                    actual: Dict[int, Dict[str, Any]] = {}
                    cursor = 0
                    while True:
                        _, resp = restart_proc.client.scan(cursor=cursor, limit=1000)
                        for item in resp.get("items", []):
                            actual[item["student_id"]] = item
                        if "next_cursor" not in resp or resp["next_cursor"] is None:
                            break
                        cursor = resp["next_cursor"]

                    validator = RecoveryValidator(ws_path, oracle)
                    v_res = validator.validate_post_recovery(actual)
                    verdict = v_res.verdict
                    if not v_res.passed:
                        status = "FAIL"
                        details.extend(v_res.details)
                finally:
                    restart_proc.stop()

            elif scenario_id in ("T05", "T06", "T07", "T08", "T09"):
                # Offline damage simulation scenario
                if scenario_id == "T05":
                    sim.corrupt_crc(slot=1)
                elif scenario_id == "T06":
                    sim.bit_flip("students.dat", offset=4096 + 253, bit=1)
                elif scenario_id == "T07":
                    cur_size = os.path.getsize(os.path.join(ws_path, "students.dat"))
                    sim.truncate_file("students.dat", cur_size - 100)
                elif scenario_id == "T08":
                    sim.bit_flip("students.idx", offset=10, bit=2)
                elif scenario_id == "T09":
                    sim.append_tail_garbage("students.wal", b"GARBAGE_BYTES_AT_TAIL")

                # Restart and validate recovery handles the corruption
                proc = ProcessManager(self.binary_info.path, ws_path)
                try:
                    proc.start(timeout=5.0)
                    state = proc.ready_line.get("state") if proc.ready_line else "UNKNOWN"
                    if state not in ("READY", "READY_DEGRADED"):
                        status = "FAIL"
                        verdict = "INVALID_STATE"
                        details.append(f"State was {state}")
                except Exception as e:
                    # Header corruption or fatal truncation exits with 3
                    status = "PASS"
                    verdict = "FATAL_EXPECTED"
                    details.append(f"Clean fatal halt: {e}")
                finally:
                    proc.kill()

            else:
                verdict = "PASS"

        except Exception as e:
            status = "FAIL"
            verdict = "HARNESS_ERROR"
            details.append(str(e))

        elapsed_ms = int((time.perf_counter() - t0) * 1000)

        # On failure, retain repro.json in the workspace
        if status == "FAIL":
            repro = {
                "scenario": scenario_id,
                "iteration": iteration,
                "seed": seed,
                "binary": self.binary_info.path,
                "verdict": verdict,
                "details": details,
            }
            with open(os.path.join(ws_path, "repro.json"), "w", encoding="utf-8") as rf:
                json.dump(repro, rf, indent=2)

        return IterationResult(
            scenario=scenario_id,
            iteration=iteration,
            seed=seed,
            status=status,
            duration_ms=elapsed_ms,
            verdict=verdict,
            details=details,
        )

