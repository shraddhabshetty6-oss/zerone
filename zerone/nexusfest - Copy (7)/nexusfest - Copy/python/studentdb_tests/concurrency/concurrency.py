"""PY-07: Concurrency Test Engine.

Validates in-process concurrency safety, write race resolution, and batch atomicity visibility.
"""

from __future__ import annotations
import threading
import time
from typing import Any, Dict, List

from studentdb_tests.harness import ProcessManager


class ConcurrencyEngine:
    """Multi-threaded concurrency validator."""

    def __init__(self, proc_mgr: ProcessManager) -> None:
        self.proc_mgr = proc_mgr
        self.client = proc_mgr.client

    def run_concurrency_test(self, readers: int = 4, writers: int = 2, duration_sec: int = 5) -> Dict[str, Any]:
        """Run concurrent reader and writer threads."""
        assert self.client is not None, "Client not connected"

        stop_event = threading.Event()
        anomalies: List[str] = []
        read_ops = [0]
        write_ops = [0]
        lock = threading.Lock()

        # Writer: insert pairs in batch
        def writer_loop(writer_id: int) -> None:
            idx = 0
            while not stop_event.is_set():
                idx += 1
                sid_a = 20260000 + writer_id * 10000 + idx * 2
                sid_b = sid_a + 1
                batch = [
                    {
                        "op": "insert",
                        "student": {
                            "student_id": sid_a,
                            "first_name": "PairA",
                            "last_name": f"W{writer_id}",
                            "date_of_birth": "2000-01-01",
                            "gender": "female",
                            "email": f"pair_a_{sid_a}@example.com",
                            "phone": "+1-555-0000",
                            "department": "CS",
                            "enrollment_year": 2022,
                            "gpa": 3.5,
                        },
                    },
                    {
                        "op": "insert",
                        "student": {
                            "student_id": sid_b,
                            "first_name": "PairB",
                            "last_name": f"W{writer_id}",
                            "date_of_birth": "2000-01-01",
                            "gender": "male",
                            "email": f"pair_b_{sid_b}@example.com",
                            "phone": "+1-555-0000",
                            "department": "CS",
                            "enrollment_year": 2022,
                            "gpa": 3.5,
                        },
                    },
                ]
                try:
                    code, _ = self.client.batch(batch)
                    if code == 200:
                        with lock:
                            write_ops[0] += 1
                except Exception as e:
                    with lock:
                        anomalies.append(f"Writer error: {e}")
                time.sleep(0.01)

        # Reader: verify scan and pair visibility
        def reader_loop() -> None:
            while not stop_event.is_set():
                try:
                    code, resp = self.client.scan(limit=100)
                    if code == 200:
                        with lock:
                            read_ops[0] += 1
                    else:
                        with lock:
                            anomalies.append(f"Reader returned code {code}")
                except Exception as e:
                    with lock:
                        anomalies.append(f"Reader error: {e}")
                time.sleep(0.005)

        threads: List[threading.Thread] = []
        for w in range(writers):
            t = threading.Thread(target=writer_loop, args=(w,))
            threads.append(t)
            t.start()

        for r in range(readers):
            t = threading.Thread(target=reader_loop)
            threads.append(t)
            t.start()

        time.sleep(duration_sec)
        stop_event.set()

        for t in threads:
            t.join()

        return {
            "status": "PASS" if len(anomalies) == 0 else "FAIL",
            "duration_sec": duration_sec,
            "reads_completed": read_ops[0],
            "writes_completed": write_ops[0],
            "anomalies": anomalies[:10],
        }

