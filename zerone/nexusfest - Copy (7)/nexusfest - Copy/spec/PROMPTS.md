# PROMPTS.md — AI Coding-Agent Development Prompts

| Field | Value |
|---|---|
| Document version | 1.0 |
| Derives from | [ARCHITECTURE.md](ARCHITECTURE.md) (authoritative). Torture design: [TORTURE.md](TORTURE.md). Test IDs: [TEST.md](TEST.md). Usage: [README.md](README.md). |
| Contents | 24 required prompts (P1–P24) + 4 supplementary prompts (P25–P28) for architecture components that have no slot in the required list (HTTP API/CLI, Go fault-injection hooks, Python harness, Python oracle/binary validator). All 28 map to the component registry in [ARCHITECTURE.md Appendix B](ARCHITECTURE.md). |

## How to Use
1. Give the agent **Global Preamble G** (Go) or **P-Preamble** (Python), then **one** prompt.
2. Each prompt is independent: it names the interfaces it needs from other components and tells the agent to code against those interfaces and use fakes when the real component does not exist yet.
3. Never ask an agent to change a format, name, or boundary defined in ARCHITECTURE.md. If a prompt seems to conflict with it, ARCHITECTURE.md wins.

## Recommended Build Order
| Step | Prompts | Reason |
|---|---|---|
| 1 | P1 | Skeleton, constants, CI commands |
| 2 | P5, P4, P26 | Leaf packages (CRC, record, fault-injection hooks) |
| 3 | P3 | Codec (needs record, crc) |
| 4 | P2, P11, P7, P13 | Persistence and leaf services |
| 5 | P10, P14 | Transactions; validator |
| 6 | P8, P9, P6 | Retrieval; Record Manager |
| 7 | P12 | Recovery (imports verify) |
| 8 | P25 | HTTP API + CLI |
| 9 | P27, P28, P15, P16 | Python foundation, functional tests |
| 10 | P17, P18, P19, P21, P22, P20, P23, P24 | Stress, concurrency, faults, torture, perf, reports |

## Global Preamble G (prepend to every Go prompt P1–P14, P25, P26)
```
You are implementing ONE component of BinStudentDB, a Go binary student database.
Follow ARCHITECTURE.md exactly: file formats (sections C, D, E.5, F.2), constants (Appendix A),
package layout and import rules (section B.2), error taxonomy (E.8), and write ordering (F.4).
Rules:
- Go >= 1.22, standard library only, no cgo, no third-party modules.
- All integers little-endian. Use constants from the constants package; never hard-code 256/4096/etc.
- Code only against the interfaces named in your prompt (SlotStore, LogWriter, LogReader,
  IndexStore, LockManager, FailpointRegistry). If a dependency is not yet implemented, write a
  minimal in-memory fake inside _test.go files; do NOT implement the other component.
- Return wrapped sentinel errors (errors.Is must work). Never panic on bad input or bad disk data.
- Never log email, phone, or other field values.
- Insert failpoint calls at the named points only if the faultinject package exists; otherwise
  use the no-op stub provided in P26's interface.
- Provide table-driven tests, run clean under `go vet` and `go test -race ./...`.
- Do not add features, flags, endpoints, or file formats that ARCHITECTURE.md does not define.
```

## P-Preamble (prepend to every Python prompt P15–P24, P27, P28)
```
You are implementing ONE component of the BinStudentDB Python test framework (python/studentdb_tests/).
Follow ARCHITECTURE.md section H and TORTURE.md. Python >= 3.11, type hints, dataclasses.
Allowed dependencies: pytest, pytest-timeout, requests, psutil, jinja2, and the standard library.
Rules:
- NEVER reimplement database logic (no WAL replay, no index, no CRUD engine). Interact with the Go
  binary only through the documented HTTP API, CLI, signals, and read-only file inspection (binval).
- All destructive actions only inside data/work/<run_id>/ (use the harness path guard).
- All randomness from an explicit seed (random.Random(seed)); never use global random or wall-clock seeds.
- Emit machine-readable results as JSON Lines rows: {test_id, scenario, status, seed, duration_ms, details}.
- Test code must be runnable by pytest and by the runner CLI: python -m studentdb_tests <subcommand>.
- Do not invent features, endpoints, failpoints, or file formats beyond ARCHITECTURE.md.
```

---

# Go Prompts

## P1 — Go Project Initialization
- **Component name:** GO-01 Project Skeleton
- **Objective:** Create the repository skeleton so every other Go component can be added independently.
- **Technology:** Go ≥ 1.22 modules; `Makefile` or `scripts/`.
- **Required functionality:** Module `studentdb`; the directory tree of ARCHITECTURE §K.1 with empty packages (`doc.go` only); a `constants` package holding Appendix A values (FormatVersion, HeaderSize, BlockSize, RecordSize, SlotsPerBlock, MaxOpsPerTx, MaxSlotGrowthPerTx, WAL magic strings, default config values, exit codes); a `cmd/studentdb` `main` that prints usage and `version`; build scripts for the three builds in §K.2 (production, `-tags faultinject`, `-race`); `.gitignore` for `bin/`, `data/live`, `data/work`, `reports/`; `go vet`/`go test` targets.
- **Inputs:** None.
- **Outputs:** Compilable repository; `bin/studentdb` prints a version string and build tag (`production`).
- **Dependencies:** None.
- **Implementation instructions:** Create packages exactly named in §B.2 layering table. Add a test that parses package imports and fails if an import violates the layer rules (L0 imports stdlib only, etc.). Expose build tag via `internal/buildinfo` (value `production` unless `faultinject` tag set, using two files with build constraints).
- **Error handling:** `main` returns exit 1 on unknown subcommand with usage text to stderr.
- **Expected test cases:** constants match Appendix A (table test); import-layer test passes; `go build` succeeds for all three build modes; `studentdb version --json` shows `build`.
- **Acceptance criteria:** `go vet ./...`, `go test -race ./...` pass; all three builds compile; layer test passes; no third-party modules in `go.mod`.

## P2 — Binary Storage Engine
- **Component name:** GO-02 Storage Engine (`internal/storage`)
- **Objective:** Implement `students.dat` handling per ARCHITECTURE §C.
- **Technology:** Go `os`, `syscall` (pread/pwrite via `File.ReadAt/WriteAt`, `fsync`).
- **Required functionality:** `Create(dir)` writing the 4096-byte header (temp file + rename + fsync dir); `Open` validating magic, version, sizes, header CRC; `SlotCount()`; `ReadSlot(n) ([256]byte)`, `WriteSlot(n, image)`, `Extend(toSlots)` (writes real zero blocks); `Sync()`; chunked sequential read (`ReadSlots(start, count)`); free-list allocator (min-heap), reservation/release, quarantine set; `SlotStore` interface; size-alignment checks; sync mode (`always`/`none`); failpoint hooks FP-05, FP-06, FP-07, FP-08, FP-09, FP-11.
- **Inputs:** data-dir path; slot numbers; 256-byte images.
- **Outputs:** images, counts; errors `ErrCorruptHeader`, `ErrDiskFull`, `ErrFormatVersion`, `ErrEngineFailed` wrapper for I/O errors.
- **Dependencies:** GO-03 codec (header codec, slot-state helpers), GO-05 CRC, GO-17 hooks (interface only).
- **Implementation instructions:** Offset = `4096 + slot×256`. Slot-range checks on every call. The engine never interprets record fields (only all-zero check via codec). fsync errors are never retried; they set a sticky failed flag. `partial:N` failpoint writes the first N bytes then triggers crash. Extension must be block-granular (16 slots).
- **Error handling:** map `ENOSPC` to `ErrDiskFull`; any other I/O error to `ErrEngineFailed`; out-of-range slot ⇒ programming error `ErrInvalidArgument` (not panic).
- **Expected test cases:** create/open round-trip; corrupt header rejected; read/write at slot 0, 15, 16, last; extend growth; non-aligned size detection; ENOSPC via fake file; torn write via fake; allocator reuse lowest-first; quarantine never allocated; concurrent `ReadSlot` safe.
- **Acceptance criteria:** TEST.md ST-001…ST-010 satisfiable; offsets match §C.5; header immutable after create; no use of sparse growth.

## P3 — Binary Serialization
- **Component name:** GO-03 Serialization Codec (`internal/codec`)
- **Objective:** Canonical encode/decode of every on-disk structure.
- **Technology:** `encoding/binary` (little-endian), manual field offsets.
- **Required functionality:** `EncodeStudent`/`DecodeStudent` for the 256-byte slot (§C.3 offsets exactly); all-zero FREE detection; tombstone encoding; data-file header, WAL header, WAL entry (BEGIN/WRITE_SLOT/COMMIT), index file header+entries+trailer codecs; CRC stamping/verification via GO-05; strict canonical rules (§C.4).
- **Inputs:** `record.Student`, status, `RecordVersion`; byte slices.
- **Outputs:** `[]byte`; decoded structs; `ErrInvalidRecord`, `ErrChecksumMismatch`.
- **Dependencies:** GO-04, GO-05.
- **Implementation instructions:** Provide a `SlotState` classifier returning FREE/ACTIVE/DELETED/CORRUPT with reason. Reject non-zero bytes after the first NUL in string fields and non-zero reserved bytes. Provide golden byte vectors in `testdata/` (hex files) that Python's PY-04 also uses. Add Go fuzz tests for `DecodeStudent` and the WAL entry decoder.
- **Error handling:** include slot/offset only in errors (never field values); input of wrong length ⇒ `ErrInvalidRecord`.
- **Expected test cases:** round-trip for min/max/boundary fields; UTF-8 multibyte at limit; NUL in string rejected; flipped bit at each of the 2,048 bit positions ⇒ detected; FREE slot recognition; golden vectors; fuzz no panic.
- **Acceptance criteria:** `encode(decode(x)) == x` for all valid inputs; every single-bit flip in `[0,256)` of an ACTIVE image classifies as CORRUPT; vectors identical to PY-04's expectations (TEST.md SE-xx).

## P4 — Student Record Structure
- **Component name:** GO-04 Student Record Model (`internal/record`)
- **Objective:** Define `Student` and its validation/normalization.
- **Technology:** Go structs, `unicode/utf8`, `time`.
- **Required functionality:** Fields per §I.2; `Validate()` returning all violations; helpers: date ↔ days-since-epoch, GPA ↔ ×100, gender ↔ enum; JSON marshalling matching §E.9 (strict: unknown fields rejected at the HTTP layer, not here).
- **Inputs:** raw field values.
- **Outputs:** validated `Student`; `ErrValidation` with field-level detail list.
- **Dependencies:** none.
- **Implementation instructions:** Byte-length limits (not rune counts). Trim spaces before validating. Email rule: exactly one `@`, no whitespace, 3–64 bytes. Phone charset `0-9+-() ` ≤ 20. DOB range 1900-01-01…today (injectable clock for tests). Server-managed fields (`CreatedAt`, `UpdatedAt`, `RecordVersion`) are not client-settable.
- **Error handling:** accumulate all violations; never panic on nil/empty input.
- **Expected test cases:** each constraint min/max/overflow; multibyte truncation boundary; invalid dates (Feb 30); GPA 4.01; empty names; control characters; clock injection.
- **Acceptance criteria:** table in §I.2 fully enforced; 100% of violations reported in one call; test coverage ≥ 90% for this package.

## P5 — CRC32 Engine
- **Component name:** GO-05 CRC32 Engine (`internal/crc`)
- **Objective:** One CRC32-IEEE implementation used by all packages.
- **Technology:** `hash/crc32` IEEE table.
- **Required functionality:** `Sum(b []byte) uint32`; streaming `Writer`; `Verify(b []byte, stored uint32) bool`; range helpers: `StampSlot`, `VerifySlot` for `[0,252)`/`[252,256)`.
- **Inputs:** byte slices.
- **Outputs:** `uint32`, bool.
- **Dependencies:** stdlib only.
- **Implementation instructions:** No state; thread-safe. Document the limitations of §D.7 in package docs.
- **Error handling:** length errors return a typed error (no panic).
- **Expected test cases:** `"123456789"` ⇒ `0xCBF43926`; empty input ⇒ `0`; streaming equals one-shot; cross-check vectors generated by Python `zlib.crc32` stored in `testdata`; all single-bit flips detected on a 252-byte buffer; burst ≤ 32 bits detected.
- **Acceptance criteria:** TEST.md CR-001…CR-008 satisfiable; benchmark shows ≥ 500 MB/s on reference hardware (target).

## P6 — Student CRUD Operations
- **Component name:** GO-06 Record Manager (`internal/engine`)
- **Objective:** Implement the public CRUD facade of §E.1–E.2.
- **Technology:** Go; interfaces `SlotStore`, `IndexStore`, `TxManager`, `LockManager`.
- **Required functionality:** `Create`, `Get`, `Update`, `Delete`, `Batch(ops)`; staging with duplicate detection against *staged* state; slot allocation through storage allocator; server-managed timestamps; `Open(config)` wiring (calls recovery), `Close`, `Stats`.
- **Inputs:** `Student`, `StudentID`, op lists.
- **Outputs:** `Student`, `{TxID, CommitLSN}`, sentinel errors.
- **Dependencies:** GO-04, GO-07, GO-10, GO-09 (for `Get`), GO-02, GO-13, GO-12 (at open).
- **Implementation instructions:** Single-op methods are one-op transactions. Update keeps `CreatedAt`. Delete writes a tombstone image; allocator frees the slot only after apply. Reject duplicate IDs and unknown IDs before logging anything. Honour `FAILED_READONLY`.
- **Error handling:** §E.8 mapping; never leave reservations after an error.
- **Expected test cases:** create/read/update/delete cycle; duplicate create; update missing; delete then reinsert same ID reuses slot; batch with insert+update of same ID; batch failure leaves no trace; max-ops boundary (1,000/1,001); FAILED_READONLY rejects writes but serves reads.
- **Acceptance criteria:** TEST.md CU-001…CU-014 satisfiable; no state change when validation fails; race detector clean with concurrent calls.

## P7 — Database Indexing
- **Component name:** GO-07 Index Manager (`internal/index`)
- **Objective:** Primary index `StudentID → SlotNumber` plus snapshot persistence.
- **Technology:** In-package B+tree (order ≥ 32), no third-party libraries.
- **Required functionality:** `Insert`, `Delete`, `Lookup`, ordered `Range(from,to,limit)`, `Len`; `Rebuild(iterator)`; `SaveSnapshot(path, checkpointLSN, slotCount)` (tmp → fsync → rename → fsync dir); `LoadSnapshot(path)` validating CRC; `IndexStore` interface; snapshot format §E.5.
- **Inputs:** `(StudentID, Slot)` pairs; file path.
- **Outputs:** slots, iterators, snapshot metadata `{CheckpointLSN, SlotCount, EntryCount}`.
- **Dependencies:** GO-03 (snapshot codec), GO-05; GO-17 hooks FP-14/FP-15 around snapshot write (checkpoint orchestration itself lives in GO-10/GO-12).
- **Implementation instructions:** Duplicate `Insert` returns `ErrDuplicateID`. The index is not goroutine-safe by itself; GO-13 guards it. Snapshot entries sorted ascending.
- **Error handling:** corrupt/short snapshot ⇒ typed `ErrSnapshotInvalid` (caller rebuilds); never panic.
- **Expected test cases:** 100k random inserts/deletes vs reference map; range boundaries; empty tree; rebuild equivalence; snapshot round-trip; flipped bit ⇒ invalid; truncated ⇒ invalid; 0 and max uint32 keys.
- **Acceptance criteria:** TEST.md IX-001…IX-010 satisfiable; point lookup benchmark at 100k keys ≤ 2 µs (target).

## P8 — Sequential Retrieval Engine
- **Component name:** GO-08 Sequential Retrieval Engine (`internal/retrieval`)
- **Objective:** Physical-order scan with CRC verification and equality filters (§E.3).
- **Technology:** Go; chunked reads via `SlotStore.ReadSlots`.
- **Required functionality:** `Scan(cursor, limit, filter, strict)` returning `items`, `errors`, `next_cursor`; chunk size 256; skip FREE/DELETED; filters `department`, `enrollment_year`, `last_name` (exact match); page size cap 1,000; read lock per page.
- **Inputs:** cursor (slot number), limit, filter, strict.
- **Outputs:** `ScanPage{Items, Errors[{Slot, Offset, Kind}], NextCursor}`.
- **Dependencies:** GO-02, GO-03, GO-13.
- **Implementation instructions:** Cursor semantics: next slot to examine; scanning stops at `SlotCount` at call time. In `strict` mode the first corrupt slot returns `ErrChecksumMismatch`. Do not use the index.
- **Error handling:** I/O errors abort the page with `ErrEngineFailed`; corrupt slots are reported, not fatal (non-strict).
- **Expected test cases:** full scan equals inserted set; pagination without loss/duplication; deleted skipped; filter correctness; corrupt slot reported with exact slot/offset; strict aborts; empty DB; cursor beyond end.
- **Acceptance criteria:** TEST.md SQ-001…SQ-009 satisfiable; throughput benchmark recorded by PY-12 (≥ 50 MB/s target).

## P9 — Indexed Retrieval Engine
- **Component name:** GO-09 Indexed Retrieval Engine (`internal/retrieval`)
- **Objective:** Point and range reads via the index with end-to-end verification (§E.4).
- **Technology:** Go.
- **Required functionality:** `GetByID(id)`; `RangeByID(from, to, limit)`; verification chain: index → slot → CRC → decode → Status==ACTIVE → `StudentID==key` else `ErrIndexInconsistent`.
- **Inputs:** IDs, range bounds.
- **Outputs:** `Student` / ordered slice / `next_from`.
- **Dependencies:** GO-07, GO-02, GO-03, GO-13.
- **Implementation instructions:** Hold the read lock only while resolving slot and reading it. Range results ordered by ID; `limit` ≤ 1,000.
- **Error handling:** `ErrNotFound`, `ErrChecksumMismatch`, `ErrIndexInconsistent`.
- **Expected test cases:** hit/miss; range inclusive bounds; index pointing to wrong-ID slot ⇒ `INDEX_INCONSISTENT`; to FREE slot; corrupt slot ⇒ `CHECKSUM_MISMATCH`; equivalence with sequential results.
- **Acceptance criteria:** TEST.md IR-001…IR-008 satisfiable; never returns an unverified record.

## P10 — Transaction Management
- **Component name:** GO-10 Transaction Manager (`internal/tx`)
- **Objective:** Atomic durable transactions with the exact write ordering of §F.4.
- **Technology:** Go; interfaces `LogWriter`, `SlotStore`, `IndexStore`, `LockManager`.
- **Required functionality:** `Begin(ctx)` (acquires `writer`), staging API, `Commit` (W2–W5), `Rollback`, monotonic TxID/LSN allocation, max 1,000 ops, checkpoint procedure K1–K5 (`Checkpoint()`), `FAILED_READONLY` transition per W8.
- **Inputs:** staged ops (slot, image); config (`sync_mode`, `wal_max_bytes`, `tx_lock_timeout`).
- **Outputs:** `{TxID, CommitLSN}`; errors per the W8 response semantics.
- **Dependencies:** GO-11, GO-02, GO-07, GO-13, GO-17 (FP-04, FP-12…FP-17 call sites).
- **Implementation instructions:** No-steal: nothing touches WAL or data before `Commit`. Build the full WAL buffer then a single append + fsync. Apply under exclusive `state` lock. Checkpoint triggers: WAL size, explicit call, graceful close. After any error past W3 return `ErrEngineFailed` (in doubt).
- **Error handling:** `ErrBusy` on lock timeout; `ErrDiskFull` only before commit point; sticky failed state; release locks on every path (defer).
- **Expected test cases:** commit visible only after apply; rollback leaves no trace; 1,001-op rejection; failure at each WAL step with fakes (write error, fsync error); crash simulation by dropping the fake after W3 (redo expected); checkpoint ordering verified by a recording fake; TxID monotonic across checkpoints (NextTxID persisted).
- **Acceptance criteria:** TEST.md TX-001…TX-012 satisfiable; lock order `writer → state` never violated (race + deadlock tests).

## P11 — Write-Ahead Logging
- **Component name:** GO-11 WAL Manager (`internal/wal`)
- **Objective:** Redo-only WAL per §F.2.
- **Technology:** Go `os`; `LogWriter`, `LogReader` interfaces.
- **Required functionality:** `Create`, `Open`; `AppendTx(entries) (commitLSN)` single write; `Sync`; `Rotate(checkpointLSN, nextTxID)` (tmp → fsync → rename → fsync dir); `Reader` that validates magic/length/CRC/LSN continuity and returns entries, `torn_tail_bytes`, and `midlog_corruption` via resync on `WALE`; `TruncateTo(offset)`; failpoints FP-01, FP-02, FP-03, FP-10.
- **Inputs:** tx entries; checkpoint metadata; file path.
- **Outputs:** commit LSN; entry stream; scan summary.
- **Dependencies:** GO-03 (entry codec), GO-05, GO-17.
- **Implementation instructions:** Enforce max entry length (1 MiB) before allocating. Entry LSNs start at `CheckpointLSN+1` and increase by exactly 1. A WRITE_SLOT payload slot number is returned to the caller unmodified; bound checking against `SlotCount` is the caller's job (recovery) but the reader exposes the field. A write failure after partial progress marks the writer failed (no retry).
- **Error handling:** `ErrCorruptWAL` for header problems; `ErrDiskFull` on ENOSPC; fsync error sticky.
- **Expected test cases:** append/read round-trip for 1, 2, 1,000-op txs; truncate at every byte offset of a 3-tx log ⇒ reader returns exactly the fully valid prefix; flipped bit at tail ⇒ torn tail; flipped bit mid-log followed by valid entries ⇒ `midlog_corruption`; LSN gap; oversized length; rotation atomicity with failpoints.
- **Acceptance criteria:** TEST.md WL-001…WL-012 satisfiable; reader never panics on arbitrary bytes (fuzz).

## P12 — Crash Recovery
- **Component name:** GO-12 Recovery Manager (`internal/recovery`)
- **Objective:** Implement R1–R14 of ARCHITECTURE §G.2 exactly.
- **Technology:** Go; uses `SlotStore`, `LogReader`, `IndexStore`, verify package.
- **Required functionality:** Full algorithm; redo with slot bound `slot < SlotCount + MaxSlotGrowthPerTx` (else treat entry as invalid per §G.4); torn-tail truncation; incomplete-tx discard; full scan classification; snapshot witness (`suspected_data_loss`, `unexplained_records`); duplicate-ID resolution; quarantine set; `startup_verify` and `on_corruption` modes; final checkpoint; `recovery_report.json` (fields §G.8) written atomically; exit-code mapping; failpoints FP-18, FP-19.
- **Inputs:** data-dir, config.
- **Outputs:** `RecoveredState{Index, FreeList, Quarantine, Report}`; fatal errors `ErrCorruptHeader`, `ErrCorruptWAL`.
- **Dependencies:** GO-11, GO-02, GO-07, GO-14, GO-03, GO-13 (process lock), GO-17.
- **Implementation instructions:** Be idempotent: a crash anywhere in recovery must be repaired by the next recovery. Never modify a slot except via WAL redo. On mid-log corruption leave all files untouched and fail. Compute `tx_committed_redone`, `uncommitted_discarded`, `torn_tail_bytes`.
- **Error handling:** fatal conditions return typed errors and write a partial report (`fatal` set) if the directory is writable.
- **Expected test cases:** committed-unapplied tx redone; incomplete tx discarded; torn tail truncated; mid-log ⇒ fatal; torn slot repaired by redo; corrupt slot quarantined; zero-filled slot flagged via witness; double recovery identical; crash-during-recovery (fake failpoint) then re-recover; `fast` vs `full` equivalence on clean state.
- **Acceptance criteria:** TEST.md RC-001…RC-016 satisfiable; report schema complete; no file modification on FATAL paths.

## P13 — Concurrency Control
- **Component name:** GO-13 Concurrency Controller (`internal/concurrency`)
- **Objective:** Safe in-process concurrency and single-process directory ownership (§E.7).
- **Technology:** `sync`, `context`, `syscall.Flock`.
- **Required functionality:** `LockManager` with `writer` mutex (context-aware acquire, timeout), `state` RWMutex (`RLock`, `Lock`), fixed ordering helper, process lock on `students.lock` (non-blocking `LOCK_EX|LOCK_NB`), release on `Close`.
- **Inputs:** contexts, timeouts.
- **Outputs:** release funcs; `ErrBusy`, `ErrLocked`.
- **Dependencies:** stdlib.
- **Implementation instructions:** Provide `AcquireWriter(ctx)` then `AcquireState()`; refuse `state` acquisition without `writer` for writers (debug assertion). Keep API tiny and allocation-free on hot paths.
- **Error handling:** timeouts return `ErrBusy`; second process returns `ErrLocked`.
- **Expected test cases:** 100 readers + 1 writer progress; writer starvation bounded; timeout path; double `Close`; two processes (subprocess test) ⇒ second gets `ErrLocked`; lock released on process kill (flock semantics).
- **Acceptance criteria:** TEST.md CC-001…CC-008 satisfiable; `go test -race` clean.

## P14 — Database Consistency Validation
- **Component name:** GO-14 Consistency Validator (`internal/verify`)
- **Objective:** Evaluate invariants I1–I10 (§G.7), online and offline.
- **Technology:** Go.
- **Required functionality:** `VerifyOffline(dir)` (all invariants, no mutation) and `VerifyOnline(engineView)` (I3–I7, I9, I10 under read lock); structured findings `{invariant, severity, slot, id, detail}`; JSON output; chunked scanning.
- **Inputs:** storage, index, WAL reader (read-only views).
- **Outputs:** `Result{OK, Findings}`; CLI exit code mapping to 2.
- **Dependencies:** GO-02, GO-07, GO-11 (reader), GO-03.
- **Implementation instructions:** Never mutate files. Compute I5 as set equality between ACTIVE slots and index entries. I7 reads each indexed slot.
- **Error handling:** I/O error ⇒ finding of severity `error` and abort with `ErrEngineFailed`.
- **Expected test cases:** clean DB ok; each invariant violated individually via crafted fixtures; large DB chunking; online verify while readers run.
- **Acceptance criteria:** TEST.md VA-001…VA-010 satisfiable; each invariant has a failing fixture.

## P25 — HTTP API Server and CLI (supplementary)
- **Component name:** GO-15 HTTP API Server (`internal/httpapi`) and GO-16 CLI (`cmd/studentdb`)
- **Objective:** Provide the documented Go↔Python interface (§B.4, §E.9, §E.10).
- **Technology:** `net/http` (Go 1.22 pattern routing), `encoding/json`, `os/signal`.
- **Required functionality:** All routes in §E.9 with exact status/error codes (§E.8); loopback-only binding check; admin bearer token (`STUDENTDB_ADMIN_TOKEN`); body limit 1 MiB, strict JSON; ready JSON line on stdout; SIGTERM graceful shutdown (checkpoint, exit 0); CLI commands `init`, `import` (CSV, batches of 100), `serve`, `verify`, `recover`, `checkpoint`, `stats`, `dump`, `backup`, `version` and exit codes 0–4; `serve` flags `--sync-mode`, `--startup-verify`, `--on-corruption`, `--wal-max-bytes`, `--log-level`, `--addr`, `--data-dir`.
- **Inputs:** HTTP requests; CLI args; env.
- **Outputs:** JSON responses; stdout ready line; files per CLI.
- **Dependencies:** GO-06, GO-08, GO-09, GO-12, GO-14, GO-17 (failpoints routes only under the build tag).
- **Implementation instructions:** No request is served until recovery finishes. Map engine errors to codes by `errors.Is`. `failpoints` handlers live in a `faultinject`-tagged file. `backup` verifies after copying and writes `manifest.json` with SHA-256.
- **Error handling:** 400 malformed JSON, 401 missing/invalid admin token, 404, 409, 422, 500, 503, 507 per §E.8; structured error body `{code, message}` without field values.
- **Expected test cases:** every route happy/error path (httptest); non-loopback `--addr` rejected; ready line format; SIGTERM leaves WAL header-only; CSV import of 1,000 rows; exit codes; production build lacks failpoint routes (404).
- **Acceptance criteria:** TEST.md CU/FI cases for the API pass; Python harness (P27) can drive every route.

## P26 — Go Fault Injection Hooks (supplementary)
- **Component name:** GO-17 Fault Injection Hooks (`internal/faultinject`)
- **Objective:** Implement failpoints FP-01…FP-19 and the durability ledger (§H.4).
- **Technology:** Go build tags (`faultinject` / `!faultinject`).
- **Required functionality:** `FailpointRegistry` interface: `Hit(name) Action`, `Arm(spec)`, `Disarm`, `Counters`; actions `crash` (self-SIGKILL), `partial:N`, `error:ENOSPC|EIO`, `delay:ms`; triggers `after_hits`, `probability`, `seed`, `once`; parse `STUDENTDB_FAILPOINTS`; a no-op implementation in non-tagged builds; helper `MaybePartialWrite(name, buf, writeFn)`; durability ledger appender.
- **Inputs:** JSON spec; hit calls from other packages.
- **Outputs:** injected behavior; counters; `durability.ledger`.
- **Dependencies:** stdlib only (leaf, L0).
- **Implementation instructions:** The no-op build MUST add zero overhead beyond an inlinable call and MUST not expose any registry state. Unknown failpoint names are rejected at arm time. `crash` uses `syscall.Kill(pid, SIGKILL)` so deferred functions never run.
- **Error handling:** invalid spec ⇒ error (HTTP 400 / exit 1) and nothing armed.
- **Expected test cases:** each action fires as specified; `after_hits` exactness; probability determinism with seed; production build compiles with no registry symbols (build test); partial write writes exactly N bytes.
- **Acceptance criteria:** catalog names identical to ARCHITECTURE §H.4; self-tests FI-001…FI-008 satisfiable.

---

# Python Prompts

## P15 — Python Dataset Generator
- **Component name:** PY-02 Dataset Generator (`datagen/`)
- **Objective:** Deterministic synthetic datasets of 1,000 / 10,000 / 100,000 records.
- **Technology:** Python stdlib (`random`, `csv`, `hashlib`, `json`).
- **Required functionality:** `generate(count, seed)` ⇒ records within §I.2 limits (IDs sequential or sparse per profile), including boundary cases (max-length names, multibyte UTF-8, GPA 0.00/4.00, extreme dates); CSV writer matching the Go `import` format; `manifest.json` (seed, count, SHA-256, generator version); golden-DB builder invoking `studentdb init` + `import` then `checkpoint`, then locking files read-only; oracle snapshot export.
- **Inputs:** count, seed, profile.
- **Outputs:** `students.csv`, `manifest.json`, `data/golden/<count>/`.
- **Dependencies:** PY-01 (process runner), PY-03 (oracle).
- **Implementation instructions:** Name lists embedded in the package (no Faker). Never use the global RNG. Validate every generated record with an independent validator implementing §I.2 rules.
- **Error handling:** refuse to overwrite an existing golden directory unless `--force`; abort if the Go import reports any rejected row.
- **Expected test cases:** same seed ⇒ identical SHA-256; different seeds differ; 100% records valid; unique IDs; boundary coverage present; golden build verified by `studentdb verify`.
- **Acceptance criteria:** HN-001…HN-006 satisfiable; 100,000-record generation completes in ≤ 30 s (target).

## P16 — Python Functional Testing
- **Component name:** PY-05 Functional Suite (`functional/`)
- **Objective:** Verify FR-01…FR-12 through the public interface.
- **Technology:** pytest, PY-01 fixtures.
- **Required functionality:** Tests for CRUD, validation errors (§I.2), batch atomicity, scan and index range, pagination, filters, error-code mapping (§E.8), persistence across graceful restart, `verify` after each scenario, binary check of first/last slot via PY-04; each test decorated with its TEST.md ID marker.
- **Inputs:** golden 1,000-record dataset; production or faultinject binary.
- **Outputs:** pytest results; JSONL rows.
- **Dependencies:** PY-01, PY-03, PY-04.
- **Implementation instructions:** One disposable workspace per test module. Compare via oracle; no sleeping for synchronization (use ready line/health).
- **Error handling:** failure artifacts (server log, workspace path) attached to results.
- **Expected test cases:** the CU, SQ, IR, VA, and FI-self-test ID ranges in TEST.md §B that are API-observable.
- **Acceptance criteria:** all mapped IDs executed and reported; suite runs against both builds.

## P17 — Python Stress Testing
- **Component name:** PY-06 Stress Engine (`stress/`)
- **Objective:** Sustained high-volume workloads with integrity checks.
- **Technology:** `concurrent.futures`, `requests.Session`.
- **Required functionality:** profiles (1k/10k/100k) with configurable op mix; small `wal_max_bytes` to force many checkpoints; growth over many blocks; delete/reinsert churn (slot reuse); periodic `verify`; final oracle comparison and offline PY-04 scan; time-boxed and count-boxed modes.
- **Inputs:** profile, seed, duration/ops.
- **Outputs:** throughput, error counts, verdict.
- **Dependencies:** PY-01, PY-03, PY-04, PY-12.
- **Implementation instructions:** Workload generator from seeded RNG; record sent/acked per op.
- **Error handling:** unexpected HTTP 5xx fails the run with artifacts retained.
- **Expected test cases:** TEST.md ST-S01…ST-S05 (stress cases).
- **Acceptance criteria:** zero oracle mismatches; `verify` ok at every checkpoint interval.

## P18 — Python Concurrency Testing
- **Component name:** PY-07 Concurrency Test Engine (`concurrency/`)
- **Objective:** Validate isolation, atomicity visibility, and lock behavior.
- **Technology:** `threading`, `requests`.
- **Required functionality:** N readers + M writers; same-ID write races (exactly one create wins); concurrent batch-vs-reader visibility (no partial batch ever observed); `BUSY` timeout provocation; scan-while-write consistency per page; run against the `-race` build.
- **Inputs:** thread counts, duration, seed.
- **Outputs:** anomaly list; verdict.
- **Dependencies:** PY-01, PY-03.
- **Implementation instructions:** Assert invariants, never timings. Each batch writes a recognizable marker pair so readers can detect partial visibility.
- **Error handling:** collect per-thread exceptions; fail with all details.
- **Expected test cases:** TEST.md CC-P01…CC-P06.
- **Acceptance criteria:** zero anomalies; race build reports no data race (stderr scan).

## P19 — Python Fault Injection
- **Component name:** PY-08 Fault Injection Engine (`faults/`)
- **Objective:** Arm failpoints, time signals, emulate power loss.
- **Technology:** `signal`, `requests`, `json`, `psutil`.
- **Required functionality:** failpoint spec builder; arming by env and by `POST /v1/admin/failpoints`; `fired` verification; SIGKILL/SIGTERM scheduler with seeded delays; `PowerLossEmulator` reading `durability.ledger` and truncating WAL bytes beyond the synced length (on the stopped copy); expectation derivation from the §B.2 matrix of TORTURE.md.
- **Inputs:** scenario variant, seed, workspace.
- **Outputs:** failpoint spec JSON, expectation record (`must_be_present/absent/in_doubt`).
- **Dependencies:** PY-01; Go GO-17.
- **Implementation instructions:** Reject unknown failpoint names client-side using the catalog. Mark iterations `NOT_TRIGGERED` when `fired == 0`.
- **Error handling:** failure to arm ⇒ iteration error (not pass).
- **Expected test cases:** TEST.md FI-001…FI-008.
- **Acceptance criteria:** each catalog failpoint can be armed and observed firing; ledger-based truncation never cuts below the synced length.

## P20 — Python Torture Testing
- **Component name:** PY-09 Torture Testing Engine (`torture/`)
- **Objective:** Run scenarios T01–T15 per TORTURE.md §B/§D.
- **Technology:** pytest + runner CLI.
- **Required functionality:** scenario registry; profiles `smoke/standard/soak`; iteration loop of §D.2; seed derivation; replay from `repro.json`; per-iteration timeout; retain failures; JSONL results; global abort on golden hash change.
- **Inputs:** scenarios, profile, master seed, binary path.
- **Outputs:** `results.jsonl`, `repro.json` per failing iteration.
- **Dependencies:** PY-01, PY-02, PY-03, PY-04, PY-08, PY-10, PY-11.
- **Implementation instructions:** Scenario = class with `prepare / inject / mutate / expected_outcome`. Never share workspaces between iterations.
- **Error handling:** classify harness exceptions as `HARNESS_ERROR` (never pass); enforce path guard.
- **Expected test cases:** TEST.md TT-001…TT-015 (one per scenario) plus dry-run self-tests.
- **Acceptance criteria:** all scenarios execute under `smoke`; each failing iteration is replayable.

## P21 — Python Binary Corruption Testing
- **Component name:** PY-10 Corruption Simulator (`corruption/`)
- **Objective:** Controlled offline damage to disposable stopped copies.
- **Technology:** `struct`, `zlib`, file I/O.
- **Required functionality:** bit flip, byte overwrite, burst, zero-fill slot, CRC-field corruption, slot swap, truncations (`.dat`, `.wal`, `.idx`), WAL tail garbage, mid-log damage, WAL deletion, index tampering with recomputed CRC, crafted WAL entry with out-of-bound slot (valid CRC); returns a damage manifest `{file, offset, length, kind, affected_slots, affected_ids}`.
- **Inputs:** workspace, mutation spec, seed.
- **Outputs:** mutated files, manifest.
- **Dependencies:** PY-04, PY-01.
- **Implementation instructions:** Mutations target only CRC-covered bytes unless the spec says otherwise; refuse if the DB process is alive.
- **Error handling:** path guard; refuse on `data/golden`.
- **Expected test cases:** TEST.md CO-001…CO-010.
- **Acceptance criteria:** manifests reproduce exactly for the same seed; damaged slots decode as CORRUPT in PY-04 (except zero-fill ⇒ FREE).

## P22 — Python Recovery Validation
- **Component name:** PY-11 Recovery Validator (`recovery/`)
- **Objective:** Judge post-restart state per TORTURE.md §E and §F.
- **Technology:** Python, PY-01/03/04.
- **Required functionality:** checks E1–E14; classification into outcome classes (§F.3); metrics (§F.1); expectation-matrix enforcement; in-doubt atomic resolution; idempotence check (second restart).
- **Inputs:** workspace, oracle, expectation record, damage manifest (optional).
- **Outputs:** verdict list, metrics row.
- **Dependencies:** PY-01, PY-03, PY-04.
- **Implementation instructions:** Never raise on bad DB state; return verdicts. Include a self-test mode that feeds known-bad synthetic states and asserts they are flagged.
- **Error handling:** restart failure ⇒ `RECOVERY_FAILED` verdict (or `FATAL_EXPECTED` if scenario expects exit 3 and files untouched).
- **Expected test cases:** TEST.md RC-P01…RC-P08 (validator self-tests) and the RC-xxx system cases.
- **Acceptance criteria:** each failure verdict reproducible by a synthetic fixture; no false PASS on any fixture.

## P23 — Python Performance Benchmarking
- **Component name:** PY-12 Performance Monitor (`perf/`)
- **Objective:** Measure §E of TEST.md.
- **Technology:** `time.perf_counter_ns`, `psutil`.
- **Required functionality:** sequential-scan latency (full and paged); indexed lookup latency (warm); insert/update/delete throughput (single-op and batch-100); concurrent request throughput; recovery duration; warm-up; percentiles p50/p95/p99/max; environment capture (CPU, RAM, disk type, OS, filesystem, `sync_mode`); datasets 1k/10k/100k.
- **Inputs:** profile, dataset, repeats.
- **Outputs:** `perf.json` with environment block.
- **Dependencies:** PY-01.
- **Implementation instructions:** Discard warm-up; at least 5 repetitions; benchmarks with `sync_mode=none` labelled unsafe; never compare across environments without the block.
- **Error handling:** abort a benchmark on any HTTP error.
- **Expected test cases:** TEST.md PF-001…PF-009 and metric-math unit tests.
- **Acceptance criteria:** complete metrics set produced for each dataset size; targets of TEST.md §G are evaluated but never fabricated.

## P24 — Automated Test Reporting
- **Component name:** PY-13 Reporting Engine (`reporting/`)
- **Objective:** Unified, honest reports.
- **Technology:** `jinja2`, `json`, `xml.etree`.
- **Required functionality:** ingest JSONL from all engines; produce `report.json`, `report.md`, `report.html`, `junit.xml`; fill the TEST.md-format table with actual results (rows never executed stay `Not Executed`); metrics tables (§F.1); failure list with `repro.json` links; environment block; exit non-zero on failures.
- **Inputs:** `reports/<run_id>/`.
- **Outputs:** report files.
- **Dependencies:** all Python engines' result rows.
- **Implementation instructions:** Never synthesize results; unknown IDs reported as `Not Executed`; partial runs flagged `INCOMPLETE`.
- **Error handling:** malformed rows quarantined into `report_errors.json`.
- **Expected test cases:** RP-001…RP-005 (golden-input ⇒ golden-output).
- **Acceptance criteria:** report totals equal the JSONL row counts; HTML renders offline.

## P27 — Python Test Harness (supplementary)
- **Component name:** PY-01 Test Harness (`harness/`)
- **Objective:** Control the Go binary, workspaces, and API.
- **Technology:** `subprocess`, `signal`, `requests`, `psutil`, `hashlib`, pytest fixtures.
- **Required functionality:** `BinaryInfo` (`version --json`, build tag, capability gate); `ProcessManager.start/stop/kill/restart/wait_ready` (parses the ready JSON line, captures stdout/stderr logs); `ApiClient` covering all §E.9 routes, returning typed results with status codes; `WorkspaceManager` (golden→work copy, path guard, SHA-256 manifests, retention policy); ephemeral ports; fixtures (`server`, `workspace`, `client`); config loading.
- **Inputs:** binary path, dataset, profile, seed.
- **Outputs:** handles and fixtures.
- **Dependencies:** Go binary (P1–P14, P25, P26).
- **Implementation instructions:** `kill()` uses SIGKILL and waits for exit; `wait_ready` has a timeout and returns the recovery report path; refuse production binaries for torture mode.
- **Error handling:** timeouts raise `HarnessError` with logs attached; never leave orphan processes (finalizers + `psutil` cleanup).
- **Expected test cases:** HN-007…HN-014 against a stub HTTP server and the real binary.
- **Acceptance criteria:** no orphaned processes after a test run; path guard rejects `..` escapes and symlinks leaving `data/work/`.

## P28 — Python Ground Truth Oracle and Binary Validator (supplementary)
- **Component name:** PY-03 Ground Truth Oracle (`oracle/`) and PY-04 Binary Validator (`binval/`)
- **Objective:** Expected-state model and independent read-only file decoder.
- **Technology:** `struct`, `zlib`, dataclasses.
- **Required functionality (oracle):** records tagged ACKED / IN_DOUBT / NEVER_SENT, transaction grouping, `compare(actual) → findings` with the verdict names of TORTURE §E; **(binval):** parse `students.dat` header + slots (§C.2–C.3), WAL header + entries (§F.2) with CRC verification and resync, `students.idx` (§E.5); report per-slot state (FREE/ACTIVE/DELETED/CORRUPT), alignment, torn tail, index↔data cross-check; read-only API (open files `rb` only).
- **Inputs:** operation logs; files of a stopped DB.
- **Outputs:** findings; structured scan results.
- **Dependencies:** none beyond stdlib.
- **Implementation instructions:** The oracle is a dictionary model, not a database. binval has no write path, no recovery, no index logic beyond decoding. Use the shared golden byte vectors from `testdata/` of GO-03.
- **Error handling:** malformed input yields findings, never exceptions.
- **Expected test cases:** HN-015…HN-024 (golden vectors, each corruption kind, WAL resync, three-state oracle cases).
- **Acceptance criteria:** binval output equals `studentdb verify --json` findings for the same files in all fixture cases; oracle classifies every §E verdict.
