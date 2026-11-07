"""PY-02: Deterministic Dataset Generator.

Generates reproducible synthetic student datasets matching ARCHITECTURE.md §I.2 schema constraints.
Embeds comprehensive first/last name and department lists without external dependencies.
Produces students.csv and manifest.json (seed, count, SHA-256).
"""

from __future__ import annotations
import csv
import datetime
import hashlib
import json
import os
import random
from typing import Any, Dict, List

FIRST_NAMES = [
    "Asha", "Aarav", "Priya", "Rahul", "Ananya", "Rohan", "Sneha", "Vikram",
    "John", "Emma", "Liam", "Olivia", "Noah", "Sophia", "Lucas", "Isabella",
    "Wei", "Yan", "Hiroshi", "Sakura", "Min-jun", "Ji-woo", "Kenji", "Mei",
    "Fatima", "Tariq", "Zainab", "Omar", "Amina", "Hassan", "Layla", "Youssef",
    "José", "María", "Carlos", "Sofia", "Mateo", "Camila", "Alejandro", "Valentina",
    "Jean", "Marie", "Pierre", "Camille", "Lukas", "Hannah", "Max", "Mia",
    "René", "Müller", "Björn", "Astrid", "Chao", "Qian", "Pradeep", "Lakshmi",
    "Abhay", "Deepika", "Suresh", "Kavita", "Rajesh", "Pooja", "Arjun", "Shreya",
]

LAST_NAMES = [
    "Rao", "Sharma", "Patel", "Verma", "Singh", "Kumar", "Gupta", "Nair",
    "Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis",
    "Wang", "Li", "Zhang", "Chen", "Liu", "Tanaka", "Sato", "Suzuki",
    "Al-Mansoor", "Khan", "Al-Sayed", "Farah", "Haddad", "Al-Nuaimi", "Qasim", "Bishara",
    "Rodriguez", "Hernandez", "Lopez", "Gonzalez", "Perez", "Sanchez", "Ramirez", "Torres",
    "Dubois", "Moreau", "Laurent", "Simon", "Weber", "Schmidt", "Schneider", "Fischer",
    "O'Connor", "MacLeod", "Lindqvist", "Janssen", "Popov", "Ivanov", "Novak", "Kowalski",
]

DEPARTMENTS = [
    "Computer Science",
    "Data Science",
    "Electrical Eng",
    "Mechanical Eng",
    "Civil Engineering",
    "Mathematics",
    "Physics",
    "Chemistry",
    "Biotechnology",
    "Economics",
    "Management",
    "Information Tech",
]

GENDERS = ["female", "male", "other", "unspecified"]


def generate_records(count: int, seed: int) -> List[Dict[str, Any]]:
    """Generate deterministic synthetic student records."""
    rng = random.Random(seed)
    records: List[Dict[str, Any]] = []

    base_id = 20240001
    used_emails = set()

    for i in range(count):
        sid = base_id + i

        first = rng.choice(FIRST_NAMES)
        last = rng.choice(LAST_NAMES)

        # Include occasional boundary cases
        if i == 0:
            # Min GPA boundary
            gpa = 0.00
            first = "A"
            last = "B"
            dob = "1900-01-01"
            year = 1950
        elif i == 1:
            # Max GPA boundary and multibyte UTF-8 name
            gpa = 4.00
            first = "Müller-José"
            last = "Sørensen-René"
            dob = "2006-12-31"
            year = 2100
        else:
            gpa = round(rng.uniform(1.50, 4.00), 2)
            # Random date between 1995-01-01 and 2007-12-31
            start_date = datetime.date(1995, 1, 1)
            days_range = (datetime.date(2007, 12, 31) - start_date).days
            dob = (start_date + datetime.timedelta(days=rng.randint(0, days_range))).isoformat()
            year = rng.randint(2018, 2024)

        gender = rng.choice(GENDERS)
        dept = rng.choice(DEPARTMENTS)

        # Ensure unique email
        clean_first = "".join(c for c in first.lower() if c.isalnum())
        clean_last = "".join(c for c in last.lower() if c.isalnum())
        email = f"{clean_first}.{clean_last}.{sid}@example.com"
        if len(email) > 64:
            email = f"s{sid}@example.com"
        used_emails.add(email)

        phone_num = f"+1-{rng.randint(200, 999)}-{rng.randint(100, 999)}-{rng.randint(1000, 9999)}"

        rec = {
            "student_id": sid,
            "first_name": first,
            "last_name": last,
            "date_of_birth": dob,
            "gender": gender,
            "email": email,
            "phone": phone_num,
            "department": dept,
            "enrollment_year": year,
            "gpa": gpa,
        }
        records.append(rec)

    return records


def write_csv(path: str, records: List[Dict[str, Any]]) -> str:
    """Write records to CSV file matching BinStudentDB import format."""
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    with open(path, "w", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow([
            "student_id", "first_name", "last_name", "date_of_birth", "gender",
            "email", "phone", "department", "enrollment_year", "gpa",
        ])
        for r in records:
            writer.writerow([
                r["student_id"],
                r["first_name"],
                r["last_name"],
                r["date_of_birth"],
                r["gender"],
                r["email"],
                r["phone"],
                r["department"],
                r["enrollment_year"],
                f"{r['gpa']:.2f}",
            ])

    # Compute SHA-256
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def write_manifest(manifest_path: str, seed: int, count: int, sha256_hash: str) -> None:
    """Write manifest.json."""
    manifest = {
        "generator_version": "1.0.0",
        "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "seed": seed,
        "count": count,
        "sha256": sha256_hash,
    }
    with open(manifest_path, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)

