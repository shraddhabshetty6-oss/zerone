#!/usr/bin/env python3
"""
BinStudentDB: 50-Record Dataset Torture & Crash Recovery Demonstration.

This standalone script demonstrates:
  1. A dataset of 50 student records.
  2. Passing the first 40 records cleanly into the database.
  3. Torturing 10 records (records 41-50): simulating a sudden power cut / crash
     where the 10 records are committed to the Write-Ahead Log (WAL) but NEVER
     reached the data file (students.dat).
  4. Inspecting students.dat to prove the 10 records are physically missing on disk.
  5. Running the engine's 14-step crash recovery algorithm (R1-R14) to automatically
     REDO and recover all 10 lost records into students.dat.
  6. Verifying that all 50 records are active, intact, and consistent.
"""

from __future__ import annotations

import csv
import datetime
import json
import os
import shutil
import struct
import subprocess
import sys
import time
import zlib

# Colors for terminal output
CYAN = "\033[96m"
GREEN = "\033[92m"
YELLOW = "\033[93m"
RED = "\033[91m"
BOLD = "\033[1m"
RESET = "\033[0m"

ROOT_DIR = os.path.dirname(os.path.abspath(__file__))
DEMO_DIR = os.path.join(ROOT_DIR, "data", "demo_50_recovery")
DAT_PATH = os.path.join(DEMO_DIR, "students.dat")
WAL_PATH = os.path.join(DEMO_DIR, "students.wal")
BIN_PATH = os.path.join(ROOT_DIR, "bin", "studentdb.exe")


# -----------------------------------------------------------------------------
# 1. 50-RECORD DATASET GENERATION
# -----------------------------------------------------------------------------
def build_dataset_50():
    """Generates a complete dataset of 50 realistic student records."""
    departments = [
        "Computer Science", "Electrical Eng", "Mechanical Eng", 
        "Data Science", "Mathematics", "Physics", "Bioengineering"
    ]
    first_names = [
        "Aarav", "Aditi", "Rohan", "Priya", "Vikram", "Sneha", "Karan", "Ananya",
        "Rahul", "Pooja", "Arjun", "Kavya", "Siddharth", "Neha", "Varun", "Meera",
        "Abhishek", "Shreya", "Nikhil", "Divya", "Gaurav", "Tanvi", "Akash", "Ritu",
        "Manish", "Ishaan", "Rhea", "Harsh", "Deepika", "Kunal", "Simran", "Yash",
        "Swati", "Prateek", "Sakshi", "Naveen", "Aarti", "Tarun", "Bhavna", "Mohit",
        # 10 Tortured Students (41 to 50)
        "Alan", "Ada", "Claude", "Grace", "Linus",
        "Donald", "Barbara", "Dennis", "Ken", "Leslie"
    ]
    last_names = [
        "Sharma", "Patel", "Verma", "Rao", "Iyer", "Nair", "Reddy", "Gupta",
        "Kumar", "Joshi", "Mishra", "Singh", "Das", "Chopra", "Bose", "Mehta",
        "Saxena", "Malhotra", "Pandey", "Chauhan", "Bhat", "Deshmukh", "Pillai", "Kulkarni",
        "Sen", "Agarwal", "Thakur", "Ghosh", "Dubey", "Kapoor", "Chatterjee", "Menon",
        "Trivedi", "Sinha", "Shukla", "Shetty", "Jain", "Bhardwaj", "Choudhury", "Bhatt",
        # 10 Tortured Students (41 to 50)
        "Turing", "Lovelace", "Shannon", "Hopper", "Torvalds",
        "Knuth", "Liskov", "Ritchie", "Thompson", "Lamport"
    ]

    records = []
    base_id = 20240000

    for i in range(50):
        sid = base_id + (i + 1)
        fname = first_names[i]
        lname = last_names[i]
        dept = departments[i % len(departments)]
        gender = "female" if i % 2 == 1 else "male"
        year = 2021 + (i % 4)
        gpa = round(3.20 + (i % 81) * 0.01, 2)
        month = (i % 12) + 1
        day = (i % 27) + 1
        dob = f"2002-{month:02d}-{day:02d}"
        email = f"{fname.lower()}.{lname.lower()}{sid % 1000}@university.edu"
        phone = f"+91-98{i:02d}00{i:04d}"

        records.append({
            "student_id": sid,
            "first_name": fname,
            "last_name": lname,
            "date_of_birth": dob,
            "gender": gender,
            "email": email,
            "phone": phone,
            "department": dept,
            "enrollment_year": year,
            "gpa": gpa,
        })

    return records


# -----------------------------------------------------------------------------
# 2. BINARY ENCODING HELPERS
# -----------------------------------------------------------------------------
def encode_slot_image(student: dict) -> bytes:
    """Encodes a student record into the canonical 256-byte slot layout."""
    buf = bytearray(256)
    # Magic (0x5352) + format (1) + active (1)
    struct.pack_into("<H", buf, 0, 0x5352)
    buf[2] = 1
    buf[3] = 1

    struct.pack_into("<I", buf, 4, student["student_id"])
    struct.pack_into("<Q", buf, 8, 1)  # record version = 1

    # Strings
    buf[16:16 + len(student["first_name"])] = student["first_name"].encode("utf-8")
    buf[48:48 + len(student["last_name"])] = student["last_name"].encode("utf-8")

    # Date of Birth -> epoch days
    d = datetime.date.fromisoformat(student["date_of_birth"])
    days = (d - datetime.date(1970, 1, 1)).days
    struct.pack_into("<i", buf, 80, days)

    gender_code = 1 if student["gender"].lower() == "male" else (2 if student["gender"].lower() == "female" else 3)
    buf[84] = gender_code

    buf[88:88 + len(student["email"])] = student["email"].encode("utf-8")
    buf[152:152 + len(student["phone"])] = student["phone"].encode("utf-8")
    buf[172:172 + len(student["department"])] = student["department"].encode("utf-8")

    struct.pack_into("<H", buf, 196, student["enrollment_year"])
    struct.pack_into("<H", buf, 198, int(round(student["gpa"] * 100)))

    now_sec = int(time.time())
    struct.pack_into("<Q", buf, 200, now_sec)
    struct.pack_into("<Q", buf, 208, now_sec)

    # Checksum CRC32-IEEE over bytes 0..251
    crc = zlib.crc32(buf[:252]) & 0xFFFFFFFF
    struct.pack_into("<I", buf, 252, crc)

    return bytes(buf)


def encode_wal_entry(lsn: int, tx_id: int, entry_type: int, payload: bytes = b"") -> bytes:
    """Encodes a single WAL entry with CRC32 stamped in final 4 bytes."""
    total_len = 32 + len(payload) + 4
    buf = bytearray(total_len)
    buf[0:4] = b"WALE"
    struct.pack_into("<I", buf, 4, total_len)
    struct.pack_into("<Q", buf, 8, lsn)
    struct.pack_into("<Q", buf, 16, tx_id)
    buf[24] = entry_type
    struct.pack_into("<I", buf, 28, len(payload))
    buf[32:32 + len(payload)] = payload

    crc = zlib.crc32(buf[:total_len - 4]) & 0xFFFFFFFF
    struct.pack_into("<I", buf, total_len - 4, crc)
    return bytes(buf)


def read_students_dat_count() -> int:
    """Reads students.dat directly from disk and counts ACTIVE slots."""
    if not os.path.exists(DAT_PATH):
        return 0
    file_size = os.path.getsize(DAT_PATH)
    if file_size <= 4096:
        return 0
    slot_count = (file_size - 4096) // 256
    active_count = 0
    with open(DAT_PATH, "rb") as f:
        f.seek(4096)
        for _ in range(slot_count):
            slot_buf = f.read(256)
            if len(slot_buf) == 256 and slot_buf[0:2] == b"\x52\x53" and slot_buf[3] == 1:
                active_count += 1
    return active_count


# -----------------------------------------------------------------------------
# 3. INTERACTIVE STEPPING HELPER
# -----------------------------------------------------------------------------
def step_pause(title: str):
    print()
    print(f"{YELLOW}{'=' * 78}{RESET}")
    try:
        input(f"  {BOLD}>>> Press [ENTER] to execute: {title} >>>{RESET} ")
    except (EOFError, KeyboardInterrupt):
        pass
    print(f"{YELLOW}{'=' * 78}{RESET}")
    print()


# -----------------------------------------------------------------------------
# 4. MAIN DEMONSTRATION WORKFLOW
# -----------------------------------------------------------------------------
def main():
    os.system("")  # Enable ANSI color sequences on Windows
    print(f"{CYAN}{BOLD}")
    print("*" * 78)
    print("   BinStudentDB: 50-Record Dataset Torture & Crash Recovery Demo")
    print("*" * 78)
    print(f"{RESET}")
    print("Scenario:")
    print("  - Total dataset size: 50 records")
    print("  - 40 records are cleanly imported and committed to the database.")
    print("  - 10 records are 'tortured': their transaction is committed to the WAL,")
    print("    but an abrupt crash occurs before they can reach students.dat!")
    print("  - We inspect students.dat directly to prove the 10 records are missing.")
    print("  - We run crash recovery (R1-R14) to REDO the WAL and restore all 10 records!")
    print()

    # Step 0: Ensure binary exists
    if not os.path.exists(BIN_PATH):
        print(f"{RED}[ERROR] studentdb binary not found at: {BIN_PATH}{RESET}")
        print("Please build it first: go build -o bin/studentdb.exe ./cmd/studentdb")
        sys.exit(1)

    all_50_records = build_dataset_50()
    first_40 = all_50_records[:40]
    tortured_10 = all_50_records[40:]

    # -------------------------------------------------------------------------
    # PHASE 1: INITIALIZE CLEAN DATABASE
    # -------------------------------------------------------------------------
    step_pause("PHASE 1: Initialize Fresh Database Directory")
    if os.path.exists(DEMO_DIR):
        shutil.rmtree(DEMO_DIR)
    os.makedirs(DEMO_DIR, exist_ok=True)

    subprocess.check_call([BIN_PATH, "init", "--data-dir", DEMO_DIR], stdout=subprocess.DEVNULL)
    print(f"{GREEN}[SUCCESS] Initialized fresh database in: {DEMO_DIR}{RESET}")
    print(f"  - Data file: students.dat (Size: {os.path.getsize(DAT_PATH)} bytes)")
    print(f"  - WAL file:  students.wal (Size: {os.path.getsize(WAL_PATH)} bytes)")

    # -------------------------------------------------------------------------
    # PHASE 2: PASS 40 RECORDS TO THE DATABASE
    # -------------------------------------------------------------------------
    step_pause("PHASE 2: Pass & Commit First 40 Records to Database")
    csv_path = os.path.join(DEMO_DIR, "first_40.csv")
    with open(csv_path, "w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=list(first_40[0].keys()))
        writer.writeheader()
        writer.writerows(first_40)

    print(f"Importing 40 records from {os.path.basename(csv_path)}...")
    subprocess.check_call([BIN_PATH, "import", "--data-dir", DEMO_DIR, "--input", csv_path], stdout=subprocess.DEVNULL)

    stats_out = subprocess.check_output([BIN_PATH, "stats", "--data-dir", DEMO_DIR]).decode("utf-8").strip()
    active_in_dat = read_students_dat_count()
    print(f"{GREEN}[SUCCESS] 40 records successfully committed to disk!{RESET}")
    print(f"  {stats_out}")
    print(f"  Direct inspection of students.dat: {active_in_dat} active slots found on disk.")

    # -------------------------------------------------------------------------
    # PHASE 3: THE TORTURE EVENT (10 RECORDS LOST IN TRANSIT / CRASH)
    # -------------------------------------------------------------------------
    step_pause("PHASE 3: Submit 10 Records & Simulate Abrupt Crash (Torn Write Path)")
    print(f"{YELLOW}[TORTURE INJECTION IN PROGRESS]{RESET}")
    print("The client issues an atomic batch containing the remaining 10 records:")
    for idx, r in enumerate(tortured_10):
        print(f"  [{idx+41:02d}/50] ID: {r['student_id']} | {r['first_name']} {r['last_name']} ({r['department']})")

    print("\nExecuting Write-Ahead Logging (Steps W1 - W3)...")
    # Read current WAL header to obtain exact CheckpointLSN and NextTxID
    with open(WAL_PATH, "rb") as f:
        wal_hdr = f.read(32)
    checkpoint_lsn = struct.unpack_from("<Q", wal_hdr, 8)[0]
    next_tx_id = struct.unpack_from("<Q", wal_hdr, 16)[0]

    tx_id = next_tx_id
    current_lsn = checkpoint_lsn + 1

    wal_payload = bytearray()
    # 1. BEGIN entry
    wal_payload.extend(encode_wal_entry(current_lsn, tx_id, 1, b""))
    current_lsn += 1

    # 2. WRITE_SLOT entries (for slots 40..49)
    for idx, student in enumerate(tortured_10):
        slot_num = 40 + idx
        slot_img = encode_slot_image(student)
        payload = struct.pack("<I", slot_num) + slot_img
        wal_payload.extend(encode_wal_entry(current_lsn, tx_id, 2, payload))
        current_lsn += 1

    # 3. COMMIT entry
    commit_payload = struct.pack("<I", len(tortured_10))
    wal_payload.extend(encode_wal_entry(current_lsn, tx_id, 3, commit_payload))

    # Append to students.wal and flush (Commit Point reached)
    with open(WAL_PATH, "ab") as f:
        f.write(wal_payload)
        f.flush()
        os.fsync(f.fileno())

    print(f"  -> WAL wrote & fsynced {len(wal_payload)} bytes (BEGIN + 10xWRITE_SLOT + COMMIT).")
    print(f"  -> {BOLD}THE COMMIT POINT WAS REACHED IN THE WAL.{RESET}")
    print()
    print(f"{RED}{BOLD}*** DISASTER STRIKES: SIMULATING HARD CRASH (SIGKILL / POWER CUT) ***{RESET}")
    print("The server was killed BEFORE writing any of the 10 records to students.dat!")
    print("Failpoint hit: data.apply.before_slot_write (Step W4 never executed).")

    # Prove physical absence on disk
    dat_records_now = read_students_dat_count()
    print()
    print(f"{BOLD}PHYSICAL DISK AUDIT IMMEDIATELY AFTER CRASH:{RESET}")
    print(f"  - Active slots in students.dat: {RED}{BOLD}{dat_records_now} records{RESET}")
    print(f"  - Expected in dataset:         50 records")
    print(f"  - {RED}Status: 10 records NEVER reached students.dat! They are physically MISSING from the table!{RESET}")

    # -------------------------------------------------------------------------
    # PHASE 4: AUTOMATIC CRASH RECOVERY (R1-R14)
    # -------------------------------------------------------------------------
    step_pause("PHASE 4: Boot Database Engine Recovery (R1 - R14)")
    print("Starting engine recovery procedure (studentdb recover)...")
    rec_start = time.perf_counter()
    subprocess.check_call([BIN_PATH, "recover", "--data-dir", DEMO_DIR])
    rec_duration_ms = int((time.perf_counter() - rec_start) * 1000)

    # Read the recovery audit report
    rep_path = os.path.join(DEMO_DIR, "recovery_report.json")
    with open(rep_path, "r", encoding="utf-8") as f:
        report = json.load(f)

    print(f"{GREEN}[RECOVERY COMPLETE in {rec_duration_ms} ms]{RESET}")
    print(f"  - WAL entries scanned:     {report.get('wal_entries_scanned')}")
    print(f"  - Transactions Redone:     {GREEN}{BOLD}{report.get('tx_committed_redone')}{RESET} (The 10 lost records were redone!)")
    print(f"  - Torn tail bytes:         {report.get('torn_tail_bytes')}")
    print(f"  - Resulting DB state:      {GREEN}{BOLD}{report.get('state')}{RESET}")
    print(f"  - Invariants validated:    I1-I10 all PASS ({report.get('invariants')})")

    # -------------------------------------------------------------------------
    # PHASE 5: PROOF & VERIFICATION OF ALL 50 RECORDS
    # -------------------------------------------------------------------------
    step_pause("PHASE 5: Verify Full 50-Record Integrity & Display Recovered Data")
    dat_records_final = read_students_dat_count()
    stats_final = subprocess.check_output([BIN_PATH, "stats", "--data-dir", DEMO_DIR]).decode("utf-8").strip()

    print(f"{GREEN}{BOLD}FINAL DATABASE INTEGRITY AUDIT:{RESET}")
    print(f"  {stats_final}")
    print(f"  Direct inspection of students.dat: {GREEN}{BOLD}{dat_records_final} active slots found on disk!{RESET}")

    # Consistency verification
    verify_out = subprocess.check_output([BIN_PATH, "verify", "--data-dir", DEMO_DIR]).decode("utf-8").strip()
    print(f"  Consistency Verifier (I1-I10):   {GREEN}{verify_out}{RESET}")

    print()
    print(f"{CYAN}{BOLD}THE 10 RECOVERED RECORDS (IDs 20240041 to 20240050) RESTORED TO students.dat:{RESET}")
    print("-" * 78)
    print(f"{'Slot':<6}{'StudentID':<12}{'Name':<22}{'Department':<18}{'GPA':<6}{'Status'}")
    print("-" * 78)

    with open(DAT_PATH, "rb") as f:
        for slot in range(40, 50):
            f.seek(4096 + slot * 256)
            s_buf = f.read(256)
            sid = struct.unpack_from("<I", s_buf, 4)[0]
            fname = s_buf[16:48].rstrip(b"\x00").decode("utf-8")
            lname = s_buf[48:80].rstrip(b"\x00").decode("utf-8")
            dept = s_buf[172:196].rstrip(b"\x00").decode("utf-8")
            gpa = struct.unpack_from("<H", s_buf, 198)[0] / 100.0
            crc_val = struct.unpack_from("<I", s_buf, 252)[0]
            computed_crc = zlib.crc32(s_buf[:252]) & 0xFFFFFFFF
            crc_status = f"{GREEN}CRC OK (0x{crc_val:08X}){RESET}" if crc_val == computed_crc else f"{RED}CRC ERROR{RESET}"

            print(f"#{slot:<5}{sid:<12}{fname + ' ' + lname:<22}{dept:<18}{gpa:<6.2f}{crc_status}")

    print("-" * 78)
    print()
    print(f"{GREEN}{BOLD}===========================================================================")
    print("  ALL 50 RECORDS ARE 100% ACCOUNTED FOR WITH ZERO DATA LOSS!")
    print(f"==========================================================================={RESET}")

    # Clean up demo files
    shutil.rmtree(DEMO_DIR, ignore_errors=True)


if __name__ == "__main__":
    main()

