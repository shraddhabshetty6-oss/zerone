"""PY-05: Functional Test Suite.

Verifies requirements FR-01 through FR-12 via the public API with Test IDs from TEST.md.
"""

from __future__ import annotations
import json
import time
from typing import Any, Dict, List, Tuple

from studentdb_tests.harness import ProcessManager, ApiClient


def run_functional_tests(proc_mgr: ProcessManager) -> List[Dict[str, Any]]:
    """Execute all functional test cases and emit results."""
    client = proc_mgr.client
    assert client is not None, "Client must be connected"

    results: List[Dict[str, Any]] = []

    def record_test(test_id: str, name: str, fn: Any) -> None:
        t0 = time.perf_counter()
        status = "FAIL"
        detail = ""
        try:
            fn()
            status = "PASS"
        except Exception as e:
            detail = str(e)
        duration_ms = int((time.perf_counter() - t0) * 1000)
        results.append({
            "test_id": test_id,
            "scenario": name,
            "status": status,
            "duration_ms": duration_ms,
            "details": detail,
        })

    # CU-001: Create student
    def test_create() -> None:
        st = {
            "student_id": 20240101,
            "first_name": "Asha",
            "last_name": "Rao",
            "date_of_birth": "2004-05-17",
            "gender": "female",
            "email": "asha.rao@example.com",
            "phone": "+91-9000000000",
            "department": "Computer Science",
            "enrollment_year": 2022,
            "gpa": 3.75,
        }
        code, resp = client.create_student(st)
        assert code == 201, f"Expected 201 Created, got {code}: {resp}"
        assert resp["student"]["student_id"] == 20240101

    record_test("CU-001", "Create Student", test_create)

    # CU-002: Duplicate student creation fails 409
    def test_duplicate() -> None:
        st = {
            "student_id": 20240101,
            "first_name": "Asha",
            "last_name": "Rao",
            "date_of_birth": "2004-05-17",
            "gender": "female",
            "email": "asha.dup@example.com",
            "phone": "+91-9000000000",
            "department": "Computer Science",
            "enrollment_year": 2022,
            "gpa": 3.75,
        }
        code, resp = client.create_student(st)
        assert code == 409, f"Expected 409 Conflict, got {code}: {resp}"

    record_test("CU-002", "Duplicate ID Rejection", test_duplicate)

    # CU-003: Read student
    def test_read() -> None:
        code, resp = client.get_student(20240101)
        assert code == 200, f"Expected 200, got {code}: {resp}"
        assert resp["first_name"] == "Asha"
        assert resp["gpa"] == 3.75

    record_test("CU-003", "Read Student Point Lookup", test_read)

    # CU-004: Read non-existent returns 404
    def test_read_missing() -> None:
        code, resp = client.get_student(99999999)
        assert code == 404, f"Expected 404, got {code}: {resp}"

    record_test("CU-004", "Read Missing Record 404", test_read_missing)

    # CU-005: Update student
    def test_update() -> None:
        st = {
            "first_name": "Asha",
            "last_name": "Rao-Sharma",
            "date_of_birth": "2004-05-17",
            "gender": "female",
            "email": "asha.sharma@example.com",
            "phone": "+91-9000000000",
            "department": "Computer Science",
            "enrollment_year": 2022,
            "gpa": 3.95,
        }
        code, resp = client.update_student(20240101, st)
        assert code == 200, f"Expected 200, got {code}: {resp}"
        assert resp["student"]["last_name"] == "Rao-Sharma"
        assert resp["student"]["gpa"] == 3.95

    record_test("CU-005", "Update Student Record", test_update)

    # CU-006: Delete student
    def test_delete() -> None:
        code, resp = client.delete_student(20240101)
        assert code == 200, f"Expected 200, got {code}: {resp}"

        # Verify not found after delete
        c2, _ = client.get_student(20240101)
        assert c2 == 404, f"Expected 404 after delete, got {c2}"

    record_test("CU-006", "Delete Student Record", test_delete)

    # CU-007: Validation failure returns 422
    def test_validation() -> None:
        invalid = {
            "student_id": 20240102,
            "first_name": "Test",
            "last_name": "User",
            "date_of_birth": "2004-05-17",
            "gender": "female",
            "email": "invalid_email_no_at",
            "phone": "+123",
            "department": "CS",
            "enrollment_year": 2022,
            "gpa": 5.00,  # Invalid: > 4.00
        }
        code, resp = client.create_student(invalid)
        assert code == 422, f"Expected 422, got {code}: {resp}"
        assert resp.get("code") == "VALIDATION_FAILED"

    record_test("CU-007", "Validation Error 422", test_validation)

    # TX-001: Atomic batch
    def test_batch() -> None:
        s1 = {
            "student_id": 20240201,
            "first_name": "BatchOne",
            "last_name": "Student",
            "date_of_birth": "2003-01-01",
            "gender": "male",
            "email": "batch1@example.com",
            "phone": "+1-555-0101",
            "department": "Mathematics",
            "enrollment_year": 2021,
            "gpa": 3.60,
        }
        s2 = {
            "student_id": 20240202,
            "first_name": "BatchTwo",
            "last_name": "Student",
            "date_of_birth": "2003-02-02",
            "gender": "female",
            "email": "batch2@example.com",
            "phone": "+1-555-0102",
            "department": "Physics",
            "enrollment_year": 2021,
            "gpa": 3.80,
        }
        ops = [
            {"op": "insert", "student": s1},
            {"op": "insert", "student": s2},
        ]
        code, resp = client.batch(ops)
        assert code == 200, f"Expected 200, got {code}: {resp}"

        # Verify both inserted
        c1, _ = client.get_student(20240201)
        c2, _ = client.get_student(20240202)
        assert c1 == 200 and c2 == 200

    record_test("TX-001", "Atomic Batch Execution", test_batch)

    # SQ-001: Sequential Scan with Filter
    def test_scan() -> None:
        code, resp = client.scan(department="Mathematics")
        assert code == 200, f"Expected 200, got {code}: {resp}"
        items = resp.get("items", [])
        assert len(items) >= 1
        assert all(it["department"] == "Mathematics" for it in items)

    record_test("SQ-001", "Sequential Scan with Filter", test_scan)

    # IR-001: Indexed Range
    def test_range() -> None:
        code, resp = client.range_index(from_id=20240200, to_id=20240250)
        assert code == 200, f"Expected 200, got {code}: {resp}"
        items = resp.get("items", [])
        assert len(items) == 2
        assert items[0]["student_id"] == 20240201
        assert items[1]["student_id"] == 20240202

    record_test("IR-001", "Indexed Range Query", test_range)

    # VA-001: Online Verify
    def test_verify() -> None:
        code, resp = client.admin_verify()
        assert code == 200, f"Expected 200, got {code}: {resp}"
        assert resp.get("ok") is True

    record_test("VA-001", "Online Invariant Verification", test_verify)

    return results

