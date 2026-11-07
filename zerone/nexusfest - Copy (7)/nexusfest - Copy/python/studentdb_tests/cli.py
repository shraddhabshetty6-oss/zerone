"""BinStudentDB Python Test Framework CLI runner."""

from __future__ import annotations
import argparse
import os
import subprocess
import sys
import time

from studentdb_tests.binval import crc32_ieee
from studentdb_tests.datagen import generate_records, write_csv, write_manifest
from studentdb_tests.harness import ProcessManager, BinaryInfo, WorkspaceManager
from studentdb_tests.oracle import Oracle, RecordState
from studentdb_tests.perf import capture_environment_block
from studentdb_tests.reporting import ReportingEngine


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="python -m studentdb_tests",
        description="BinStudentDB Python Test and Torture Suite",
    )
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    # gen
    p_gen = subparsers.add_parser("gen", help="Generate synthetic student dataset")
    p_gen.add_argument("--count", type=int, default=1000, help="Number of records to generate")
    p_gen.add_argument("--seed", type=int, default=42, help="RNG seed")
    p_gen.add_argument("--out", type=str, default="data/golden/1000", help="Output directory")

    # selftest
    subparsers.add_parser("selftest", help="Execute internal framework self-tests")

    # functional
    p_func = subparsers.add_parser("functional", help="Run functional test suite")
    p_func.add_argument("--bin", type=str, default="bin/studentdb", help="Path to studentdb binary")
    p_func.add_argument("--dataset", type=str, default="data/golden/1000", help="Golden dataset path")

    # stress
    p_stress = subparsers.add_parser("stress", help="Run stress test")
    p_stress.add_argument("--bin", type=str, default="bin/studentdb", help="Path to studentdb binary")
    p_stress.add_argument("--ops", type=int, default=1000, help="Total operations")
    p_stress.add_argument("--seed", type=int, default=42, help="Seed")

    # concurrency
    p_conc = subparsers.add_parser("concurrency", help="Run concurrency test")
    p_conc.add_argument("--bin", type=str, default="bin/studentdb", help="Path to studentdb binary")
    p_conc.add_argument("--readers", type=int, default=4, help="Number of reader threads")
    p_conc.add_argument("--writers", type=int, default=2, help="Number of writer threads")
    p_conc.add_argument("--duration", type=int, default=5, help="Test duration in seconds")

    # torture
    p_tort = subparsers.add_parser("torture", help="Run torture scenarios T01–T15")
    p_tort.add_argument("--bin", type=str, default="bin/studentdb-fi", help="Path to faultinject binary")
    p_tort.add_argument("--scenario", type=str, default="T01", help="Scenario ID (e.g. T01, T04, T05) or 'all'")
    p_tort.add_argument("--iterations", type=int, default=3, help="Iterations per scenario")
    p_tort.add_argument("--seed", type=int, default=12345, help="Master seed")

    # report
    p_rep = subparsers.add_parser("report", help="Generate reports for run ID")
    p_rep.add_argument("--run-id", type=str, required=True, help="Run ID")

    args = parser.parse_args()

    if args.subcommand == "gen":
        handle_gen(args.count, args.seed, args.out)
    elif args.subcommand == "selftest":
        handle_selftest()
    elif args.subcommand == "functional":
        handle_functional(args.bin, args.dataset)
    elif args.subcommand == "stress":
        handle_stress(args.bin, args.ops, args.seed)
    elif args.subcommand == "concurrency":
        handle_concurrency(args.bin, args.readers, args.writers, args.duration)
    elif args.subcommand == "torture":
        handle_torture(args.bin, args.scenario, args.iterations, args.seed)
    elif args.subcommand == "report":
        handle_report(args.run_id)


def handle_gen(count: int, seed: int, out_dir: str) -> None:
    print(f"Generating {count} student records (seed={seed}) into {out_dir}...")
    records = generate_records(count, seed)
    os.makedirs(out_dir, exist_ok=True)
    csv_path = os.path.join(out_dir, "students.csv")
    sha256_hash = write_csv(csv_path, records)
    manifest_path = os.path.join(out_dir, "manifest.json")
    write_manifest(manifest_path, seed, count, sha256_hash)
    print(f"Successfully generated {count} records. SHA-256: {sha256_hash}")


def handle_selftest() -> None:
    print("Running framework self-tests...")
    failed = 0

    # 1. CRC32 vector check
    crc_val = crc32_ieee(b"123456789")
    if crc_val != 0xCBF43926:
        print(f"[FAIL] CRC32 self-test: got {crc_val:#010x}, want 0xCBF43926")
        failed += 1
    else:
        print("[PASS] CRC32 self-test matches IEEE vector 0xCBF43926")

    # 2. Dataset generator determinism
    r1 = generate_records(10, 42)
    r2 = generate_records(10, 42)
    if r1 != r2:
        print("[FAIL] Dataset generator determinism test failed")
        failed += 1
    else:
        print("[PASS] Dataset generator produces identical output for identical seed")

    # 3. Oracle comparison check
    orc = Oracle()
    orc.stage_transaction(1, [("insert", 20240001, {"first_name": "Test"})])
    orc.acknowledge_transaction(1)
    ok, _ = orc.compare({20240001: {"first_name": "Test"}})
    if not ok:
        print("[FAIL] Oracle ACKED match test failed")
        failed += 1
    else:
        print("[PASS] Oracle ACKED match validation passed")

    # 4. Oracle missing ACKED check
    ok_miss, findings = orc.compare({})
    if ok_miss or not any(f.verdict == "ACKED_LOSS" for f in findings):
        print("[FAIL] Oracle ACKED_LOSS detection failed")
        failed += 1
    else:
        print("[PASS] Oracle ACKED_LOSS correctly flagged")

    # 5. Oracle IN_DOUBT partial atomicity check
    orc2 = Oracle()
    orc2.stage_transaction(2, [
        ("insert", 20240002, {"first_name": "A"}),
        ("insert", 20240003, {"first_name": "B"}),
    ])
    # Only one of two present
    ok_part, part_find = orc2.compare({20240002: {"first_name": "A"}})
    if ok_part or not any(f.verdict == "PARTIAL_TX" for f in part_find):
        print("[FAIL] Oracle PARTIAL_TX detection failed")
        failed += 1
    else:
        print("[PASS] Oracle PARTIAL_TX correctly flagged")

    if failed == 0:
        print("\nAll framework self-tests PASSED (5/5).")
        sys.exit(0)
    else:
        print(f"\nFramework self-tests FAILED: {failed} errors.")
        sys.exit(1)


def handle_functional(binary_path: str, dataset_dir: str) -> None:
    from studentdb_tests.functional import run_functional_tests

    run_id = f"func_{int(time.time())}"
    ws_mgr = WorkspaceManager()
    ws = ws_mgr.create_disposable_workspace(run_id, "func", dataset_dir)
    subprocess.check_call([binary_path, "init", "--data-dir", ws])

    proc = ProcessManager(binary_path, ws)
    proc.start()
    try:
        results = run_functional_tests(proc)
        rep = ReportingEngine(run_id)
        for r in results:
            rep.add_result(r)
            status_color = "\033[92mPASS\033[0m" if r["status"] == "PASS" else "\033[91mFAIL\033[0m"
            print(f"[{status_color}] {r['test_id']} - {r['scenario']} ({r['duration_ms']} ms)")
        paths = rep.write_reports(capture_environment_block())
        print(f"\nReports written to: {paths['html']}")
    finally:
        proc.stop()


def handle_stress(binary_path: str, ops: int, seed: int) -> None:
    from studentdb_tests.stress import StressEngine

    run_id = f"stress_{int(time.time())}"
    ws_mgr = WorkspaceManager()
    ws = ws_mgr.create_disposable_workspace(run_id, "stress")

    # Initialize empty db
    subprocess.check_call([binary_path, "init", "--data-dir", ws])

    proc = ProcessManager(binary_path, ws)
    proc.start()
    try:
        engine = StressEngine(proc, seed=seed)
        res = engine.run_stress(num_ops=ops)
        print(f"Stress test {res['status']}: {res['total_ops']} ops in {res['duration_sec']}s ({res['ops_per_sec']} ops/s)")
    finally:
        proc.stop()


def handle_concurrency(binary_path: str, readers: int, writers: int, duration: int) -> None:
    from studentdb_tests.concurrency import ConcurrencyEngine

    run_id = f"conc_{int(time.time())}"
    ws_mgr = WorkspaceManager()
    ws = ws_mgr.create_disposable_workspace(run_id, "conc")
    subprocess.check_call([binary_path, "init", "--data-dir", ws])

    proc = ProcessManager(binary_path, ws)
    proc.start()
    try:
        eng = ConcurrencyEngine(proc)
        res = eng.run_concurrency_test(readers=readers, writers=writers, duration_sec=duration)
        print(f"Concurrency test {res['status']}: {res['reads_completed']} reads, {res['writes_completed']} writes")
    finally:
        proc.stop()


def handle_torture(binary_path: str, scenario: str, iterations: int, seed: int) -> None:
    from studentdb_tests.torture import TortureEngine

    engine = TortureEngine(binary_path)
    scenarios = ["T01", "T04", "T05", "T06", "T07", "T08", "T09"] if scenario == "all" else [scenario]

    total_runs = 0
    passed_runs = 0
    for sc in scenarios:
        print(f"\nRunning Torture Scenario {sc} ({iterations} iterations)...")
        results = engine.run_scenario(sc, iterations=iterations, master_seed=seed)
        for r in results:
            total_runs += 1
            if r.status == "PASS":
                passed_runs += 1
            status_text = "PASS" if r.status == "PASS" else "FAIL"
            print(f"  Iteration {r.iteration}: {status_text} (Verdict: {r.verdict}, {r.duration_ms} ms)")

    print(f"\nTorture execution complete: {passed_runs}/{total_runs} iterations passed.")


def handle_report(run_id: str) -> None:
    rep = ReportingEngine(run_id)
    paths = rep.write_reports(capture_environment_block())
    print(f"Reports generated:\n  HTML: {paths['html']}\n  JSON: {paths['json']}\n  Markdown: {paths['md']}")


if __name__ == "__main__":
    main()

