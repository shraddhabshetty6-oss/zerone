# TEST.md — Testing Strategy

| Field | Value |
|---|---|
| Document version | 1.0 |
| Derives from | [ARCHITECTURE.md](ARCHITECTURE.md) (components, invariants, formats). Torture scenarios: [TORTURE.md](TORTURE.md). Implementation prompts: [PROMPTS.md](PROMPTS.md). Execution instructions: [README.md](README.md). |
| Execution status | **Nothing has been executed.** Every `Actual Result` and `Status` cell below is `Not Executed`. Results are produced only by the Reporting Engine (PY-13) from real runs. No result, pass rate, or timing in this repository may be hand-written. |

> **Note on tables in §B and §H.** All test-case tables follow the mandatory format defined in §F.

---

# A. Testing Methodology

| Type | Scope | Tooling | Owner components | Trigger |
|---|---|---|---|---|
| Unit | One Go package, fakes for dependencies | `go test`, `-race`, `-fuzz` | GO-02…GO-14 | Every commit |
| Integration | Several real Go packages on real temp files (storage+wal+index+tx) | `go test` with `-tags integration` | GO-06, GO-10, GO-12 | Every commit |
| System | Real `studentdb` binary driven over HTTP/CLI | pytest + PY-01 | GO-15, GO-16 | Every merge |
| Functional | Requirements FR-01…FR-14 via public interface | PY-05 | all | Every merge |
| Regression | Re-run of functional + frozen failing torture repros (`repro.json`) | PY-05, PY-09 replay | all | Every merge / release |
| Performance | Latency and throughput (§E) | PY-12 | GO-08, GO-09, GO-10 | Nightly + release |
| Stress | Sustained volume, churn, many checkpoints | PY-06 | GO-02, GO-10, GO-11 | Nightly |
| Torture | Fault injection and corruption (T01–T15) | PY-08, PY-09, PY-10 | GO-11, GO-12, GO-14 | Smoke per merge; standard nightly; soak weekly/release |
| Recovery | Post-failure correctness (E1–E14 of TORTURE.md) | PY-11 | GO-12, GO-14 | With every torture iteration |
| Data integrity | Oracle comparison, CRC detection, offline binary decode | PY-03, PY-04 | GO-05, GO-03, GO-14 | Every system/stress/torture run |

**Principles:** deterministic by seed; isolated workspaces; failures preserve artifacts; no test depends on wall-clock timing for correctness (timing is measured only in performance tests).

---

# B. Component Testing

## B.1 Binary Storage — GO-02 (prefix `ST`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| ST-001 | Storage | Create database | `init` on empty dir | `students.dat` is 4096 B, header CRC valid, SlotCount 0 | Not Executed | Not Executed |
| ST-002 | Storage | Open valid file | Initialized dir | Opens; SlotCount derived from size | Not Executed | Not Executed |
| ST-003 | Storage | Corrupt header | Flip byte at offset 20 | `ErrCorruptHeader` | Not Executed | Not Executed |
| ST-004 | Storage | Offset math | Write/read slots 0, 15, 16, last | Bytes located at `4096 + n×256` | Not Executed | Not Executed |
| ST-005 | Storage | Growth | Allocate slot 17 on empty DB | File grows by exactly one block (16 slots) with real zero bytes | Not Executed | Not Executed |
| ST-006 | Storage | Non-aligned size | Truncate to `4096+4096+100` | Reported as not block-aligned | Not Executed | Not Executed |
| ST-007 | Storage | ENOSPC on write | Fake file returns ENOSPC | `ErrDiskFull`; no success reported | Not Executed | Not Executed |
| ST-008 | Storage | fsync failure | Fake fsync returns EIO | Sticky failed state; no retry; `ErrEngineFailed` | Not Executed | Not Executed |
| ST-009 | Storage | Allocator order | Free slots {5,3,9}, one quarantined | Lowest free first; quarantined never returned | Not Executed | Not Executed |
| ST-010 | Storage | Concurrent reads | 16 goroutines `ReadSlot` | Correct bytes; race detector clean | Not Executed | Not Executed |
| ST-S01 | Storage (stress) | Mixed load 1k | 10,000 mixed ops on 1,000-record DB | Oracle match; `verify` ok | Not Executed | Not Executed |
| ST-S02 | Storage (stress) | Many checkpoints | 10k DB, `wal_max_bytes` = 64 KiB, 50,000 ops | Oracle match; checkpoints occurred; `verify` ok | Not Executed | Not Executed |
| ST-S03 | Storage (stress) | Large load | 100k DB, 20,000 mixed ops | Oracle match; `verify` ok | Not Executed | Not Executed |
| ST-S04 | Storage (stress) | Slot reuse churn | 20,000 delete/reinsert cycles | File size stays within expected block count | Not Executed | Not Executed |
| ST-S05 | Storage (stress) | Growth | Insert until 1,000 blocks added | Every record readable; invariants hold | Not Executed | Not Executed |

## B.2 Serialization — GO-03, GO-04 (prefix `SE`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| SE-001 | Codec | Round trip | Typical record | `decode(encode(s)) == s` | Not Executed | Not Executed |
| SE-002 | Codec | Boundary lengths | Name 32 B, email 64 B, phone 20 B, dept 24 B | Round trip OK | Not Executed | Not Executed |
| SE-003 | Codec/Record | Multibyte at limit | 16×2-byte chars vs 33-byte name | 32 B accepted; 33 B `ErrValidation` | Not Executed | Not Executed |
| SE-004 | Codec | NUL in string | `"a\x00b"` | Rejected | Not Executed | Not Executed |
| SE-005 | Codec | Non-zero padding | Byte ≠ 0 after first NUL | `ErrInvalidRecord` | Not Executed | Not Executed |
| SE-006 | Codec | Bit-flip sweep | All 2,048 single-bit flips of an ACTIVE image | Every flip classified CORRUPT | Not Executed | Not Executed |
| SE-007 | Codec | FREE slot | 256 zero bytes | State FREE; no CRC failure | Not Executed | Not Executed |
| SE-008 | Codec | Tombstone | Delete image | DELETED; valid CRC; data fields zero | Not Executed | Not Executed |
| SE-009 | Codec | Golden vectors | `testdata/*.hex` | Bytes identical to PY-04 expectations | Not Executed | Not Executed |
| SE-010 | Codec | Fuzz decode | Random 256-byte inputs | No panic; always classified | Not Executed | Not Executed |

## B.3 CRC32 — GO-05 (prefix `CR`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| CR-001 | CRC32 | Known vector | `"123456789"` | `0xCBF43926` | Not Executed | Not Executed |
| CR-002 | CRC32 | Empty input | `""` | `0x00000000` | Not Executed | Not Executed |
| CR-003 | CRC32 | Streaming | Same data in chunks | Equals one-shot | Not Executed | Not Executed |
| CR-004 | CRC32 | Python cross-check | 1,000 random buffers | Equals `zlib.crc32` | Not Executed | Not Executed |
| CR-005 | CRC32 | Single-bit sweep | All bit flips of a 252-byte buffer | All detected | Not Executed | Not Executed |
| CR-006 | CRC32 | Burst ≤ 32 bits | 10,000 random bursts | All detected | Not Executed | Not Executed |
| CR-007 | CRC32 | Slot ranges | StampSlot/VerifySlot | Covers `[0,252)`, stored at `[252,256)` | Not Executed | Not Executed |
| CR-008 | CRC32 | Wrong length | 255-byte slot | Typed error, no panic | Not Executed | Not Executed |

## B.4 CRUD — GO-06, GO-15, GO-16 (prefix `CU`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| CU-001 | CRUD | Create + read | Valid student | 201; read returns identical fields | Not Executed | Not Executed |
| CU-002 | CRUD | Duplicate create | Existing ID | 409 `DUPLICATE_ID` | Not Executed | Not Executed |
| CU-003 | CRUD | Update | Changed fields | Replaced; `created_at` preserved; `updated_at` advanced | Not Executed | Not Executed |
| CU-004 | CRUD | Update missing | Unknown ID | 404 `NOT_FOUND` | Not Executed | Not Executed |
| CU-005 | CRUD | Delete | Existing ID | 200; subsequent read 404 | Not Executed | Not Executed |
| CU-006 | CRUD | Slot reuse | Delete then insert another ID | Reuses the freed slot; no file growth | Not Executed | Not Executed |
| CU-007 | CRUD | Validation | GPA 4.01, empty name | 422 `VALIDATION_FAILED` listing both fields | Not Executed | Not Executed |
| CU-008 | CRUD | Batch same-ID ops | Insert + update same ID | Succeeds atomically | Not Executed | Not Executed |
| CU-009 | CRUD | Batch with invalid op | 10 ops, one invalid | Nothing applied; WAL size unchanged | Not Executed | Not Executed |
| CU-010 | CRUD | Batch limits | 1,000 ops vs 1,001 ops | 1,000 accepted; 1,001 rejected 422 | Not Executed | Not Executed |
| CU-011 | CRUD | Server-managed fields | Client sends `created_at` | Ignored or rejected per API contract | Not Executed | Not Executed |
| CU-012 | CRUD | Persistence | Write, SIGTERM, restart | All records present; WAL header-only at start | Not Executed | Not Executed |
| CU-013 | CRUD | FAILED_READONLY | Trigger FP-09 (fault build) | Writes 503; reads succeed | Not Executed | Not Executed |
| CU-014 | CLI | CSV import | 1,000-row CSV | Count 1,000; `verify` exit 0 | Not Executed | Not Executed |

## B.5 Sequential Retrieval — GO-08 (prefix `SQ`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| SQ-001 | Sequential | Full scan | 1,000 records | Exactly all ACTIVE records | Not Executed | Not Executed |
| SQ-002 | Sequential | Pagination | limit ∈ {1, 7, 256, 1000} | No loss, no duplication | Not Executed | Not Executed |
| SQ-003 | Sequential | Deleted records | Delete 100 then scan | Deleted absent | Not Executed | Not Executed |
| SQ-004 | Sequential | Filter department | `department=X` | Only matching records | Not Executed | Not Executed |
| SQ-005 | Sequential | Filter combo | `enrollment_year` + `last_name` | Only matching records | Not Executed | Not Executed |
| SQ-006 | Sequential | Corrupt slot | One damaged slot, non-strict | Slot/offset in `errors[]`; others returned | Not Executed | Not Executed |
| SQ-007 | Sequential | Strict mode | Same, `strict=true` | Aborts with `CHECKSUM_MISMATCH` | Not Executed | Not Executed |
| SQ-008 | Sequential | Empty DB | No records | Empty items, null cursor | Not Executed | Not Executed |
| SQ-009 | Sequential | Cursor past end | cursor > SlotCount | Empty page, null cursor | Not Executed | Not Executed |

## B.6 Indexed Retrieval — GO-09 (prefix `IR`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| IR-001 | Indexed | Point hit | Existing ID | Exact record | Not Executed | Not Executed |
| IR-002 | Indexed | Point miss | Unknown ID | 404 | Not Executed | Not Executed |
| IR-003 | Indexed | Range | `[a,b]` | Inclusive, ascending | Not Executed | Not Executed |
| IR-004 | Indexed | Wrong-ID slot | Index entry → slot of other ID | `INDEX_INCONSISTENT`; no wrong record | Not Executed | Not Executed |
| IR-005 | Indexed | FREE slot target | Index entry → FREE slot | `INDEX_INCONSISTENT` | Not Executed | Not Executed |
| IR-006 | Indexed | Corrupt slot | Damaged slot | `CHECKSUM_MISMATCH` | Not Executed | Not Executed |
| IR-007 | Indexed | Equivalence | 1k and 10k DB | Index-range set == sequential-scan set | Not Executed | Not Executed |
| IR-008 | Indexed | Limit cap | limit 5,000 | Capped at 1,000 | Not Executed | Not Executed |

## B.7 Indexing — GO-07 (prefix `IX`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| IX-001 | Index | Random ops | 100k inserts/deletes vs reference map | Identical contents | Not Executed | Not Executed |
| IX-002 | Index | Duplicate insert | Existing key | `ErrDuplicateID` | Not Executed | Not Executed |
| IX-003 | Index | Delete | Delete then lookup | Miss | Not Executed | Not Executed |
| IX-004 | Index | Range bounds | Inclusive ends, keys 0 and 2³²−1 | Correct | Not Executed | Not Executed |
| IX-005 | Index | Range order/limit | limit < matches | Ascending, truncated | Not Executed | Not Executed |
| IX-006 | Index | Empty tree | All ops | No panic; correct empties | Not Executed | Not Executed |
| IX-007 | Index | Rebuild | Rebuild from scan | Equals live index | Not Executed | Not Executed |
| IX-008 | Index | Snapshot round trip | Save/load | Identical; metadata correct | Not Executed | Not Executed |
| IX-009 | Index | Snapshot bit flip | One flipped bit | `ErrSnapshotInvalid`; rebuild path | Not Executed | Not Executed |
| IX-010 | Index | Snapshot truncated | Cut at several offsets | `ErrSnapshotInvalid` | Not Executed | Not Executed |

## B.8 Write-Ahead Log — GO-11 (prefix `WL`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| WL-001 | WAL | Append/read small | 1-op tx | BEGIN, WRITE_SLOT, COMMIT recovered (sizes 36/296/40 B) | Not Executed | Not Executed |
| WL-002 | WAL | Append/read large | 1,000-op tx | All entries recovered | Not Executed | Not Executed |
| WL-003 | WAL | Truncate every offset | 3-tx log cut at each byte | Reader returns exactly the valid prefix | Not Executed | Not Executed |
| WL-004 | WAL | Tail bit flip | Flip in last entry | Torn tail reported | Not Executed | Not Executed |
| WL-005 | WAL | Mid-log flip | Flip in early entry, later entries valid | `midlog_corruption` | Not Executed | Not Executed |
| WL-006 | WAL | LSN gap | Skip an LSN | Entry invalid | Not Executed | Not Executed |
| WL-007 | WAL | Oversized length | Length field 4 GiB | Rejected without allocation | Not Executed | Not Executed |
| WL-008 | WAL | Rotation atomicity | Crash at FP-16/FP-17 | Either old or new WAL valid, never mixed | Not Executed | Not Executed |
| WL-009 | WAL | ENOSPC append | FP-10 | `ErrDiskFull`; writer failed | Not Executed | Not Executed |
| WL-010 | WAL | fsync error | Fake EIO | Sticky; no retry | Not Executed | Not Executed |
| WL-011 | WAL | Header CRC | Corrupt header | `ErrCorruptWAL` | Not Executed | Not Executed |
| WL-012 | WAL | Fuzz reader | Arbitrary bytes | No panic | Not Executed | Not Executed |

## B.9 Transactions — GO-10 (prefix `TX`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| TX-001 | Transaction | Visibility | Commit then read | Visible only after apply | Not Executed | Not Executed |
| TX-002 | Transaction | Rollback | Stage then rollback | No WAL bytes, no data change, reservations released | Not Executed | Not Executed |
| TX-003 | Transaction | Serialized writers | 50 concurrent writers | TxIDs unique and increasing | Not Executed | Not Executed |
| TX-004 | Transaction | Write order | Recording fakes | Append < fsync < data write < index update | Not Executed | Not Executed |
| TX-005 | Transaction | Crash after commit point | Drop fakes after W3 | Redo restores the tx | Not Executed | Not Executed |
| TX-006 | Transaction | Crash before commit point | Drop fakes before W3 | Tx absent after recovery | Not Executed | Not Executed |
| TX-007 | Transaction | WAL write error | Fake ENOSPC | Specific error; tx definitely not committed | Not Executed | Not Executed |
| TX-008 | Transaction | Error after commit point | Apply failure | `ENGINE_FAILED_READONLY` (in doubt) | Not Executed | Not Executed |
| TX-009 | Transaction | Checkpoint order | Recording fakes | K1…K5 in order | Not Executed | Not Executed |
| TX-010 | Transaction | WAL header after checkpoint | Checkpoint | CheckpointLSN equals last committed LSN | Not Executed | Not Executed |
| TX-011 | Transaction | NextTxID | Checkpoint + restart | TxID strictly increasing across restarts | Not Executed | Not Executed |
| TX-012 | Transaction | Lock timeout | Hold writer lock 6 s | `BUSY`; no deadlock | Not Executed | Not Executed |

## B.10 Crash Recovery — GO-12, GO-14 (prefix `RC`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| RC-001 | Recovery | Clean start | Header-only WAL | `tx_committed_redone` 0; READY | Not Executed | Not Executed |
| RC-002 | Recovery | Committed unapplied | Kill at FP-04 | Tx redone; present | Not Executed | Not Executed |
| RC-003 | Recovery | Incomplete tx | Kill at FP-02 | Discarded; absent; `uncommitted_discarded` ≥ 1 | Not Executed | Not Executed |
| RC-004 | Recovery | Torn tail | Partial entry at tail | Truncated; `torn_tail_bytes` reported | Not Executed | Not Executed |
| RC-005 | Recovery | Mid-log corruption | Flip in early entry | Exit 3; files untouched | Not Executed | Not Executed |
| RC-006 | Recovery | Torn slot | Kill at FP-06 | Slot repaired by redo | Not Executed | Not Executed |
| RC-007 | Recovery | Corrupt checkpointed slot | Flip bit after checkpoint | Quarantined; READY_DEGRADED | Not Executed | Not Executed |
| RC-008 | Recovery | Zero-filled slot | Zero an ACTIVE slot after checkpoint | In `suspected_data_loss[]` | Not Executed | Not Executed |
| RC-009 | Recovery | Truncated data file | Cut last blocks | Padded; lost IDs in `suspected_data_loss[]` | Not Executed | Not Executed |
| RC-010 | Recovery | Idempotence | Recover twice | Same state; stable report counts | Not Executed | Not Executed |
| RC-011 | Recovery | Crash during recovery | FP-18 then restart | Final state equals uninterrupted recovery | Not Executed | Not Executed |
| RC-012 | Recovery | Snapshot mismatch | Wrong CheckpointLSN | `index_source = scan` | Not Executed | Not Executed |
| RC-013 | Recovery | Fast vs full | Clean state | Identical index | Not Executed | Not Executed |
| RC-014 | Recovery | Duplicate IDs | Two valid ACTIVE slots, same ID | Higher `RecordVersion` kept; other quarantined | Not Executed | Not Executed |
| RC-015 | Recovery | WAL missing | Delete `.wal` | `WAL_MISSING` reported; checkpointed data intact | Not Executed | Not Executed |
| RC-016 | Recovery | Report schema | Any recovery | All §G.8 fields present | Not Executed | Not Executed |
| RC-P01 | Validator | Known-good fixture | Clean post-recovery state | `PASS` | Not Executed | Not Executed |
| RC-P02 | Validator | ACKED_LOSS fixture | ACKED ID missing | Flagged `ACKED_LOSS` | Not Executed | Not Executed |
| RC-P03 | Validator | SILENT_CORRUPTION fixture | Bad slot unreported by engine | Flagged | Not Executed | Not Executed |
| RC-P04 | Validator | PARTIAL_TX fixture | Half of a batch visible | Flagged `PARTIAL_TX` | Not Executed | Not Executed |
| RC-P05 | Validator | PHANTOM fixture | Never-sent ID present | Flagged `PHANTOM_RECORD` | Not Executed | Not Executed |
| RC-P06 | Validator | DUPLICATE fixture | ID twice | Flagged `DUPLICATE_RECORD` | Not Executed | Not Executed |
| RC-P07 | Validator | INDEX_MISMATCH fixture | Seq ≠ index set | Flagged | Not Executed | Not Executed |
| RC-P08 | Validator | MATRIX_VIOLATION fixture | FP-01 tx present | Flagged | Not Executed | Not Executed |

## B.11 Concurrency — GO-13 (prefix `CC`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| CC-001 | Concurrency | Readers + writer | 100 readers, 1 writer | Both make progress | Not Executed | Not Executed |
| CC-002 | Concurrency | Writer wait bound | Continuous readers | Writer acquires within `tx_lock_timeout` | Not Executed | Not Executed |
| CC-003 | Concurrency | Timeout | Held writer lock | `ErrBusy` | Not Executed | Not Executed |
| CC-004 | Concurrency | Second process | Two `serve` on one dir | Second exits 4 (`ErrLocked`) | Not Executed | Not Executed |
| CC-005 | Concurrency | Lock after kill | SIGKILL first process | Second process starts | Not Executed | Not Executed |
| CC-006 | Concurrency | Batch visibility (unit) | Readers during batch apply | Never partial | Not Executed | Not Executed |
| CC-007 | Concurrency | Race detector | Stress under `-race` | No data race | Not Executed | Not Executed |
| CC-008 | Concurrency | Lock order | Assertion build | `writer → state` never violated | Not Executed | Not Executed |
| CC-P01 | Concurrency (Py) | Same-ID race | 32 threads create ID X | Exactly one 201, rest 409 | Not Executed | Not Executed |
| CC-P02 | Concurrency (Py) | Batch visibility | Marker-pair batches vs readers | No reader sees one marker without the other | Not Executed | Not Executed |
| CC-P03 | Concurrency (Py) | Scan during writes | Paged scans + writers | Each page internally consistent; no dup within page | Not Executed | Not Executed |
| CC-P04 | Concurrency (Py) | BUSY provocation | Blocked writer + others | `503 BUSY` after timeout | Not Executed | Not Executed |
| CC-P05 | Concurrency (Py) | Race build | `studentdb-race` workload | No race report on stderr | Not Executed | Not Executed |
| CC-P06 | Concurrency (Py) | Kill under load | See TT-011 | Matches TT-011 | Not Executed | Not Executed |

## B.12 Consistency Validation — GO-14 (prefix `VA`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| VA-001 | Validator | I1 | Corrupt data header | Finding I1 | Not Executed | Not Executed |
| VA-002 | Validator | I2 | Non-aligned size | Finding I2 | Not Executed | Not Executed |
| VA-003 | Validator | I3 | Bad-CRC slot | Finding I3 with slot | Not Executed | Not Executed |
| VA-004 | Validator | I4 | Duplicate ACTIVE IDs | Finding I4 | Not Executed | Not Executed |
| VA-005 | Validator | I5 | Index missing an ACTIVE slot | Finding I5 | Not Executed | Not Executed |
| VA-006 | Validator | I6 | Index slot ≥ SlotCount | Finding I6 | Not Executed | Not Executed |
| VA-007 | Validator | I7 | Index key ≠ slot's ID | Finding I7 | Not Executed | Not Executed |
| VA-008 | Validator | I8 | Committed entries left after recovery | Finding I8 | Not Executed | Not Executed |
| VA-009 | Validator | I9/I10 | Wrong active count; free list overlaps ACTIVE | Findings I9, I10 | Not Executed | Not Executed |
| VA-010 | Validator | Clean DB | Healthy 10k DB | `ok = true`, no findings | Not Executed | Not Executed |

## B.13 Fault Injection Hooks — GO-17, PY-08 (prefix `FI`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| FI-001 | Fault injection | Each failpoint fires | Arm FP-01…FP-19 one by one | Each reports `fired ≥ 1` in a suitable workload | Not Executed | Not Executed |
| FI-002 | Fault injection | `after_hits` | N = 1, 2, 10 | Fires exactly on the N-th hit | Not Executed | Not Executed |
| FI-003 | Fault injection | Probability determinism | Same seed twice | Identical firing sequence | Not Executed | Not Executed |
| FI-004 | Fault injection | `partial:N` | N = 1, 127, 255 | Exactly N bytes written, then crash | Not Executed | Not Executed |
| FI-005 | Fault injection | `error:ENOSPC` | FP-10/11/12 | Mapped to documented errors | Not Executed | Not Executed |
| FI-006 | Fault injection | Production build | `studentdb` (no tag) | `/v1/admin/failpoints` returns 404; no registry | Not Executed | Not Executed |
| FI-007 | Fault injection | Unknown name | Arm `foo.bar` | Rejected (400); nothing armed | Not Executed | Not Executed |
| FI-008 | Fault injection | Power-loss emulation | Truncate WAL by ledger | Never cut below last synced length | Not Executed | Not Executed |

---

# C. Python Testing

## C.1 Dependencies and Environment
Python ≥ 3.11. Packages (see `python/requirements.txt`, illustrative): `pytest`, `pytest-timeout`, `requests`, `psutil`, `jinja2`. Environment variables (illustrative): `STUDENTDB_BIN` (production binary), `STUDENTDB_FI_BIN` (fault-inject binary), `STUDENTDB_RACE_BIN`, `SDBTEST_SEED`, `STUDENTDB_ADMIN_TOKEN`.

## C.2 Test Organization (`python/studentdb_tests/`)
| Directory | Component | pytest marker | Go build used |
|---|---|---|---|
| `harness/` | PY-01 | `harness` | any |
| `datagen/` | PY-02 | `datagen` | production (CLI) |
| `oracle/`, `binval/` | PY-03, PY-04 | `selftest` | none/any |
| `functional/` | PY-05 | `functional` | production + fault-inject |
| `stress/` | PY-06 | `stress` | production |
| `concurrency/` | PY-07 | `concurrency` | race |
| `faults/` | PY-08 | `faultinject` | fault-inject |
| `torture/` | PY-09 | `torture` | fault-inject |
| `corruption/` | PY-10 | `corruption` | fault-inject |
| `recovery/` | PY-11 | `recovery` | fault-inject |
| `perf/` | PY-12 | `perf` | production |
| `reporting/` | PY-13 | `reporting` | none |

Size markers: `size_1k`, `size_10k`, `size_100k`. Every test is decorated with its Test ID (`@pytest.mark.test_id("WL-003")`) so PY-13 can fill the §F table.

## C.3 Interaction with Go
Only through the contract in [ARCHITECTURE.md §B.4](ARCHITECTURE.md): process spawn + ready line, HTTP `/v1`, CLI, signals, read-only file inspection. Python tests contain no database logic. PY-04 is a read-only decoder validated against the same golden byte vectors as GO-03 (SE-009, HN-015).

## C.4 Illustrative Execution (not implemented)
```
go test -race ./...                                       # Go unit/integration
python -m studentdb_tests functional --bin bin/studentdb  # PY-05
python -m studentdb_tests torture --profile smoke --seed 12345 --bin bin/studentdb-fi
python -m studentdb_tests report --run-id <run_id>        # PY-13
```

## C.5 Python Framework Self-Tests

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| HN-001 | Datagen | Determinism | Same seed twice | Identical SHA-256 | Not Executed | Not Executed |
| HN-002 | Datagen | Seed sensitivity | Two seeds | Different output | Not Executed | Not Executed |
| HN-003 | Datagen | Validity | 100,000 records | 100% pass §I.2 rules | Not Executed | Not Executed |
| HN-004 | Datagen | Unique IDs | 100,000 records | No duplicate `student_id` | Not Executed | Not Executed |
| HN-005 | Datagen | Boundary coverage | Default profile | Max-length, multibyte, GPA 0/4 rows present | Not Executed | Not Executed |
| HN-006 | Datagen | Golden build | `init` + `import` + `checkpoint` | `studentdb verify` exit 0; files read-only | Not Executed | Not Executed |
| HN-007 | Harness | Build detection | production vs fault-inject binary | Capability reported correctly; torture refuses production | Not Executed | Not Executed |
| HN-008 | Harness | Ready line | Start server | Ready JSON parsed; port reachable | Not Executed | Not Executed |
| HN-009 | Harness | Kill hygiene | SIGKILL server | Process gone; no orphans | Not Executed | Not Executed |
| HN-010 | Harness | Restart | Kill then start | New ready line; recovery report path returned | Not Executed | Not Executed |
| HN-011 | Harness | Path guard | `..` escape | Refused | Not Executed | Not Executed |
| HN-012 | Harness | Symlink guard | Symlink to golden | Refused | Not Executed | Not Executed |
| HN-013 | Harness | Golden integrity | Run any test | Golden SHA-256 unchanged | Not Executed | Not Executed |
| HN-014 | Harness | API coverage | Stub server | Every §E.9 route callable | Not Executed | Not Executed |
| HN-015 | Binval | Golden vectors | `testdata/*.hex` | Parsed per §C.3 | Not Executed | Not Executed |
| HN-016 | Binval | Bad CRC slot | Flip a bit | Slot CORRUPT | Not Executed | Not Executed |
| HN-017 | Binval | WAL resync | Damaged entry + valid later | Mid-log corruption reported | Not Executed | Not Executed |
| HN-018 | Binval | Index parse | Valid `.idx` | Entries and trailer CRC verified | Not Executed | Not Executed |
| HN-019 | Binval | Agreement | Same files | Findings equal `studentdb verify --json` | Not Executed | Not Executed |
| HN-020 | Oracle | ACKED missing | Remove ACKED ID | Finding `ACKED_LOSS` | Not Executed | Not Executed |
| HN-021 | Oracle | IN_DOUBT atomicity | Half of a tx present | Finding `PARTIAL_TX` | Not Executed | Not Executed |
| HN-022 | Oracle | NEVER_SENT | Unexpected ID present | Finding `PHANTOM_RECORD` | Not Executed | Not Executed |
| HN-023 | Oracle | Timestamps | Implausible `created_at` | Flagged | Not Executed | Not Executed |
| HN-024 | Oracle | Both outcomes legal | IN_DOUBT present or absent atomically | Both PASS | Not Executed | Not Executed |
| CO-001 | Corruption | Bit flip | Seeded target slot | Manifest exact; PY-04 shows CORRUPT | Not Executed | Not Executed |
| CO-002 | Corruption | Byte overwrite | Seeded offset | Same | Not Executed | Not Executed |
| CO-003 | Corruption | Burst ≤ 32 bits | Seeded | Same | Not Executed | Not Executed |
| CO-004 | Corruption | Zero-fill slot | ACTIVE slot | PY-04 shows FREE (CRC-undetectable) | Not Executed | Not Executed |
| CO-005 | Corruption | CRC field | Bits in `[252,256)` | CORRUPT | Not Executed | Not Executed |
| CO-006 | Corruption | Slot swap | Two ACTIVE slots | Both CRC-valid; ID cross-check catches via index | Not Executed | Not Executed |
| CO-007 | Corruption | Truncations | `.dat`, `.wal`, `.idx` | Sizes as specified | Not Executed | Not Executed |
| CO-008 | Corruption | WAL tail damage | Garbage; missing COMMIT | As specified | Not Executed | Not Executed |
| CO-009 | Corruption | Mid-log damage; WAL delete | Early entry flipped | As specified | Not Executed | Not Executed |
| CO-010 | Corruption | Index tamper / crafted WAL | CRC recomputed; slot out of bound | As specified; same seed ⇒ same manifest | Not Executed | Not Executed |
| RP-001 | Reporting | Golden I/O | Fixed JSONL | Fixed report output | Not Executed | Not Executed |
| RP-002 | Reporting | Missing IDs | Unexecuted tests | Rows remain `Not Executed` | Not Executed | Not Executed |
| RP-003 | Reporting | Partial run | Interrupted run | Flagged `INCOMPLETE` | Not Executed | Not Executed |
| RP-004 | Reporting | Malformed rows | Bad JSONL line | Quarantined to `report_errors.json` | Not Executed | Not Executed |
| RP-005 | Reporting | Totals | Any run | Totals equal row counts; valid JUnit XML | Not Executed | Not Executed |

---

# D. Test Dataset

## D.1 Dataset Sizes
Generated by PY-02 from a seed; one read-only golden database per size.

| Dataset | Records | Slots allocated (blocks×16) | `students.dat` size | `students.idx` size | Use |
|---|---|---|---|---|---|
| Initial | 1,000 | 1,008 (63 blocks) | 262,144 B (256 KiB) | 8,036 B | All suites; torture smoke/standard; fastest feedback |
| Medium | 10,000 | 10,000 (625 blocks) | 2,564,096 B (≈ 2.45 MiB) | 80,036 B | Stress, standard torture, performance |
| Large | 100,000 | 100,000 (6,250 blocks) | 25,604,096 B (≈ 24.4 MiB) | 800,036 B | Performance, soak torture, recovery-time (NFR-03) |

(Sizes are arithmetic from [ARCHITECTURE.md §C and §E.5](ARCHITECTURE.md), not measurements.)

## D.2 How to Scale Testing
1. **Same code, bigger data:** every suite is parameterized by `size_1k|10k|100k`; correctness tests run at all sizes, expensive suites (soak, large perf) only at larger sizes on demand.
2. **Seed discipline:** the dataset seed is separate from the workload seed, so a golden dataset can be reused with many workload seeds.
3. **Linear cost model:** startup full verification is O(slots); expect recovery time to scale roughly linearly with size (to be **measured**, not assumed, by PF-009).
4. **Beyond 100,000:** repeat generation at 1,000,000 records (≈ 244 MiB data file by the same arithmetic) for exploratory runs; format needs no change (§A.3 O8).
5. **Isolation cost:** disposable copies cost O(file size) per iteration; use `smoke` on 100k only for performance/recovery-time checks, not for 5,000-iteration loops.

---

# E. Performance Testing

All performance tests run through PY-12 on the **production** build with `sync_mode=always` (unless a row says otherwise), warm-up discarded, ≥ 5 repetitions, environment block captured (CPU, RAM, disk type, filesystem, OS, Go/Python versions).

| Metric | Definition | Datasets | Test ID |
|---|---|---|---|
| Sequential retrieval latency | Time for a full scan (paged, limit 1,000) and per-page latency p50/p95/p99 | 1k, 10k, 100k | PF-001, PF-002 |
| Indexed retrieval latency | Point lookup p50/p95/p99/max, warm cache, loopback HTTP | 1k, 10k, 100k | PF-003 |
| Insert throughput | Ops/s, single-op tx and 100-op batches | 1k→ growth | PF-004 |
| Update throughput | Ops/s, single-op and batch | 10k, 100k | PF-005 |
| Delete throughput | Ops/s, single-op and batch | 10k, 100k | PF-006 |
| Mixed workload | 70% read / 20% update / 5% insert / 5% delete, 60 s | 10k | PF-007 |
| Concurrent request throughput | Total req/s at 1, 4, 8, 16 client threads (reads) and 1 writer + N readers | 100k | PF-008 |
| Recovery duration | Start → ready after SIGKILL with ≈ 1,000 un-checkpointed txs; and clean start | 1k, 10k, 100k | PF-009 |

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| PF-001 | Performance | Full sequential scan | 1k/10k/100k | Metrics recorded; all records returned | Not Executed | Not Executed |
| PF-002 | Performance | Paged sequential scan | limit 1,000 | Per-page percentiles recorded | Not Executed | Not Executed |
| PF-003 | Performance | Indexed point lookup | 100k random IDs | p50/p95/p99/max recorded | Not Executed | Not Executed |
| PF-004 | Performance | Insert throughput | single and batch-100 | Ops/s recorded; oracle match | Not Executed | Not Executed |
| PF-005 | Performance | Update throughput | single and batch-100 | Ops/s recorded | Not Executed | Not Executed |
| PF-006 | Performance | Delete throughput | single and batch-100 | Ops/s recorded | Not Executed | Not Executed |
| PF-007 | Performance | Mixed workload | 60 s | Throughput + error count recorded | Not Executed | Not Executed |
| PF-008 | Performance | Concurrency scaling | 1/4/8/16 threads | Req/s per level recorded | Not Executed | Not Executed |
| PF-009 | Performance | Recovery duration | Kill with pending WAL; clean start | Durations recorded; correctness verified | Not Executed | Not Executed |

---

# F. Test Case Format

Every test case in this repository uses:

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| (unique ID) | (component name from [ARCHITECTURE.md Appendix B](ARCHITECTURE.md)) | (what is tested) | (data/action) | (observable, falsifiable outcome) | `Not Executed` until a real run | `Not Executed` until a real run |

**Rules**
1. Initial value of **Actual Result** and **Status** is exactly `Not Executed`.
2. Allowed statuses after execution: `Pass`, `Fail`, `Blocked`, `Skipped (reason)`, `Not Executed`.
3. Only PY-13 (and Go test output ingested by it) may change these cells, from machine-produced results; **no result may be invented or estimated**.
4. A test with no executable implementation yet remains `Not Executed`.

## F.1 Torture Scenario Cases (prefix `TT`)

| Test ID | Component | Scenario | Input | Expected Result | Actual Result | Status |
|---|---|---|---|---|---|---|
| TT-001 | Torture / Recovery | T01 crash during insertion | `smoke`, FP-01…FP-08 + SIGKILL | All §E checks PASS; matrix satisfied | Not Executed | Not Executed |
| TT-002 | Torture / Recovery | T02 crash during updates | same | same | Not Executed | Not Executed |
| TT-003 | Torture / Recovery | T03 crash during deletes | same | same; slot reusable | Not Executed | Not Executed |
| TT-004 | Torture / Recovery | T04 interrupted writes | `partial:N` sweeps | Torn slot redone; torn WAL rolled back | Not Executed | Not Executed |
| TT-005 | Torture / Corruption | T05 corrupted records | Bit/byte/burst/zero/swap | 100% detection (zero-fill via witness) | Not Executed | Not Executed |
| TT-006 | Torture / Corruption | T06 corrupted CRCs | Record/header/WAL/idx CRCs | Per TORTURE §B.3 | Not Executed | Not Executed |
| TT-007 | Torture / Corruption | T07 truncated files | `.dat`/`.wal`/`.idx` cuts | Per TORTURE §B.3 | Not Executed | Not Executed |
| TT-008 | Torture / Corruption | T08 corrupted index | Flips, stale, tampered | Data intact; no wrong record | Not Executed | Not Executed |
| TT-009 | Torture / Recovery | T09 interrupted WAL writes | Tail garbage, mid-log, deletion | Per TORTURE §B.3 | Not Executed | Not Executed |
| TT-010 | Torture / Recovery | T10 disk full | FP-10/11/12 | Documented errors; restart recovers | Not Executed | Not Executed |
| TT-011 | Torture / Concurrency | T11 concurrent R/W + kill | 8R + 4W, random kill | No partial tx visible or recovered | Not Executed | Not Executed |
| TT-012 | Torture / Recovery | T12 repeated restarts | Graceful, SIGKILL, crash-in-recovery | Idempotent; stable state | Not Executed | Not Executed |
| TT-013 | Torture / Corruption | T13 invalid offsets | Bad index pointers; crafted WAL slot | No wrong record; no unbounded growth | Not Executed | Not Executed |
| TT-014 | Torture / Recovery | T14 unexpected termination | SIGKILL/SIGTERM random | SIGKILL per matrix; SIGTERM clean | Not Executed | Not Executed |
| TT-015 | Torture / Recovery | T15 incomplete transactions | Batches 2…1,000 ops, FP-01…FP-08 | All-or-nothing | Not Executed | Not Executed |

---

# G. Acceptance Criteria

All thresholds are **targets** to be confirmed on reference hardware (local SSD/NVMe, `sync_mode=always`, loopback HTTP) by the first measured run; adjust only through a documented change, never retroactively to match a result.

| Area | Criterion (measurable) | Evidence |
|---|---|---|
| **Data integrity** | In every `smoke` and `standard` torture run: `ACKED_LOSS = 0`, `SILENT_CORRUPTION = 0`, `PARTIAL_TX = 0`, `PHANTOM_RECORD = 0`, `DUPLICATE_RECORD = 0`; Data integrity percentage = 100% for ACKED records in recoverable scenarios. | TT-001…TT-015, PY-11 metrics |
| **Retrieval accuracy** | At 1k, 10k, 100k: sequential scan set == indexed range set == oracle set; field-identical for 100% of records; zero responses containing an unverified (CRC-failed) record. | SQ-001, IR-007, ST-S0x |
| **CRC detection** | 100% of single-bit flips (all 2,048 positions × ≥ 1,000 slots), 100% of bursts ≤ 32 bits (≥ 10,000 trials), 0 undetected in ≥ 10,000 random multi-byte corruptions (expected undetected probability ≈ 2⁻³² each; any undetected case is investigated, not waived). | SE-006, CR-005, CR-006, TT-005/006 |
| **Recovery correctness** | Recovery success rate = 100% of triggered iterations (`smoke`/`standard`); Corruption detection rate = 100%; idempotence 100%; every FATAL-expected case exits 3 and leaves files byte-identical; recovery duration ≤ 5 s at 100,000 records (NFR-03). | RC-001…RC-016, TT-xxx, PF-009 |
| **Performance (targets)** | Indexed point lookup p99 ≤ 5 ms at 100k (API, warm); full paged scan of 1,000 records ≤ 250 ms and of 100,000 records ≤ 10 s; single-op insert ≥ 200 ops/s and batch-100 ≥ 5,000 records/s with `sync_mode=always`; 8-thread read throughput ≥ 5,000 req/s at 100k. | PF-001…PF-009 |
| **Concurrency** | Zero anomalies in CC-P01…CC-P06; `go test -race` clean; no deadlock or `BUSY` under a 10-minute mixed load at < `tx_lock_timeout` hold times. | CC-001…CC-008, CC-P01…P06 |
| **Application stability** | Zero panics and zero hangs across all `smoke` torture runs and a 1-hour stress run; process RSS growth after warm-up ≤ 10%; every `GET /v1/admin/verify` after stress returns `ok = true`. | TT-xxx, ST-S01…S05 |
| **Documentation/traceability** | Every component in [ARCHITECTURE.md Appendix B](ARCHITECTURE.md) has ≥ 1 test ID (§H); every T01–T15 has a TT case. | §H |

**Release gate:** all criteria above met by actual reports from PY-13 (with `Not Executed` rows explicitly justified). No gate may pass on rows that remain `Not Executed`.

---

# H. Traceability

| Component | Tests |
|---|---|
| GO-02 Storage Engine | ST-001…ST-010, ST-S01…S05, TT-004, TT-007 |
| GO-03 Serialization Codec / GO-04 Record Model | SE-001…SE-010, CU-007 |
| GO-05 CRC32 Engine | CR-001…CR-008, TT-005, TT-006 |
| GO-06 Record Manager | CU-001…CU-014 |
| GO-07 Index Manager | IX-001…IX-010, TT-008 |
| GO-08 Sequential Retrieval | SQ-001…SQ-009, PF-001/002 |
| GO-09 Indexed Retrieval | IR-001…IR-008, PF-003, TT-013 |
| GO-10 Transaction Manager | TX-001…TX-012, TT-015 |
| GO-11 WAL Manager | WL-001…WL-012, TT-009 |
| GO-12 Recovery Manager | RC-001…RC-016, TT-001…TT-015 |
| GO-13 Concurrency Controller | CC-001…CC-008, CC-P01…P06, TT-011 |
| GO-14 Consistency Validator | VA-001…VA-010 |
| GO-15 / GO-16 API and CLI | CU-001…CU-014, CC-004, HN-014 |
| GO-17 Fault Injection Hooks | FI-001…FI-008 |
| PY-01…PY-04 | HN-001…HN-024 |
| PY-05 Functional | all API-observable IDs |
| PY-06 Stress | ST-S01…S05 |
| PY-07 Concurrency | CC-P01…CC-P06 |
| PY-08 Fault Injection | FI-001…FI-008 |
| PY-09 Torture | TT-001…TT-015 |
| PY-10 Corruption | CO-001…CO-010 |
| PY-11 Recovery Validator | RC-P01…RC-P08 |
| PY-12 Performance | PF-001…PF-009 |
| PY-13 Reporting | RP-001…RP-005 |
