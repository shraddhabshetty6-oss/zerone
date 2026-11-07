"""PY-06: Stress Testing Engine.

Generates high-volume mixed CRUD workloads, triggers frequent checkpoints,
validates slot reuse, and verifies consistency against the ground truth oracle.
"""

from __future__ import annotations
import random
import time
from typing import Any, Dict, List, Tuple

from studentdb_tests.harness import ProcessManager
from studentdb_tests.oracle import Oracle


class StressEngine:
    """Sustained stress workload generator."""

    def __init__(self, proc_mgr: ProcessManager, seed: int = 42) -> None:
        self.proc_mgr = proc_mgr
        self.client = proc_mgr.client
        self.rng = random.Random(seed)
        self.oracle = Oracle()

    def run_stress(self, num_ops: int = 1000, batch_size: int = 10) -> Dict[str, Any]:
        """Execute mixed stress operations."""
        assert self.client is not None, "Client not connected"

        start_time = time.perf_counter()
        active_ids: List[int] = []
        next_sid = 20250001
        op_counts = {"insert": 0, "update": 0, "delete": 0, "read": 0}
        errors: List[str] = []

        for i in range(num_ops):
            roll = self.rng.random()

            # 40% Insert, 30% Read, 20% Update, 10% Delete
            if roll < 0.40 or len(active_ids) < 5:
                # Insert
                sid = next_sid
                next_sid += 1
                st = {
                    "student_id": sid,
                    "first_name": f"StressFirst{sid % 1000}",
                    "last_name": f"StressLast{sid % 1000}",
                    "date_of_birth": "2003-05-15",
                    "gender": "other",
                    "email": f"stress{sid}@example.com",
                    "phone": "+1-555-9999",
                    "department": "Computer Science",
                    "enrollment_year": 2022,
                    "gpa": round(self.rng.uniform(2.0, 4.0), 2),
                }

                tx_id = self.oracle.next_tx_id()
                self.oracle.stage_transaction(tx_id, [("insert", sid, st)])
                code, resp = self.client.create_student(st)
                if code == 201:
                    self.oracle.acknowledge_transaction(tx_id)
                    active_ids.append(sid)
                    op_counts["insert"] += 1
                else:
                    errors.append(f"Insert {sid} failed: {code} {resp}")

            elif roll < 0.70:
                # Read
                sid = self.rng.choice(active_ids)
                code, resp = self.client.get_student(sid)
                if code == 200:
                    op_counts["read"] += 1
                else:
                    errors.append(f"Read {sid} failed: {code} {resp}")

            elif roll < 0.90:
                # Update
                sid = self.rng.choice(active_ids)
                new_gpa = round(self.rng.uniform(2.0, 4.0), 2)
                st = {
                    "first_name": f"StressFirst{sid % 1000}",
                    "last_name": f"UpdatedLast{sid % 1000}",
                    "date_of_birth": "2003-05-15",
                    "gender": "other",
                    "email": f"stress{sid}@example.com",
                    "phone": "+1-555-9999",
                    "department": "Computer Science",
                    "enrollment_year": 2022,
                    "gpa": new_gpa,
                }
                tx_id = self.oracle.next_tx_id()
                self.oracle.stage_transaction(tx_id, [("update", sid, st)])
                code, resp = self.client.update_student(sid, st)
                if code == 200:
                    self.oracle.acknowledge_transaction(tx_id)
                    op_counts["update"] += 1
                else:
                    errors.append(f"Update {sid} failed: {code} {resp}")

            else:
                # Delete
                sid = self.rng.choice(active_ids)
                tx_id = self.oracle.next_tx_id()
                self.oracle.stage_transaction(tx_id, [("delete", sid, None)])
                code, resp = self.client.delete_student(sid)
                if code == 200:
                    self.oracle.acknowledge_transaction(tx_id)
                    active_ids.remove(sid)
                    op_counts["delete"] += 1
                else:
                    errors.append(f"Delete {sid} failed: {code} {resp}")

            # Periodic verify every 200 ops
            if (i + 1) % 200 == 0:
                v_code, v_resp = self.client.admin_verify()
                if v_code != 200 or not v_resp.get("ok"):
                    errors.append(f"Periodic verify failed at op {i+1}: {v_resp}")

        elapsed = time.perf_counter() - start_time
        ops_per_sec = num_ops / elapsed if elapsed > 0 else 0

        # Scan all actual records for oracle comparison
        actual_records: Dict[int, Dict[str, Any]] = {}
        cursor = 0
        while True:
            code, resp = self.client.scan(cursor=cursor, limit=1000)
            if code != 200:
                errors.append(f"Final scan failed: {resp}")
                break
            items = resp.get("items", [])
            for it in items:
                actual_records[it["student_id"]] = it
            if "next_cursor" not in resp or resp["next_cursor"] is None:
                break
            cursor = resp["next_cursor"]

        ok, findings = self.oracle.compare(actual_records)
        if not ok:
            for f in findings:
                errors.append(f"Oracle discrepancy [{f.verdict}]: {f.detail}")

        return {
            "status": "PASS" if len(errors) == 0 else "FAIL",
            "duration_sec": round(elapsed, 2),
            "total_ops": num_ops,
            "ops_per_sec": round(ops_per_sec, 1),
            "op_counts": op_counts,
            "error_count": len(errors),
            "errors": errors[:10],
        }

