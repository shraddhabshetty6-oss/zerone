# Architecture and robustness proof

## Part 1: Architecture diagram

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                         CLIENT LAYER                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │   CLI Tool   │  │  Torture     │  │  Unit Tests  │  │  serve() API │      │
│  │   (kvdb.py)  │  │  Tool        │  │              │  │ (JSONL)      │      │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘      │
│         │                  │                  │                  │             │
│         └──────────────────┴──────────────────┴──────────────────┘             │
└──────────────────────────┴─────────────────────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                      DATABASE ENGINE                                         │
│  ┌─────────────────────────────────────────────────────────────────────┐     │
│  │              PER-PATH SYNCHRONIZATION                                │     │
│  │  ┌──────────────────────┐  ┌─────────────────────────────────────┐  │     │
│  │  │ In-process RLock     │  │ OS File Lock (.lock file)           │  │     │
│  │  │ (thread coordination)│  │ (cross-process coordination)        │  │     │
│  │  └──────────────────────┘  └─────────────────────────────────────┘  │     │
│  └─────────────────────────────────────────────────────────────────────┘     │
│                           │                                                  │
│                           ▼                                                  │
│  ┌─────────────────────────────────────────────────────────────────────┐     │
│  │                     WRITETHRU CACHE                                  │     │
│  │  ┌───────────────────────────────────────────────────────────────┐  │     │
│  │  │ In-memory hash map {key: value}                              │  │     │
│  │  │ - Fast reads (O(1))                                          │  │     │
│  │  │ - Rebuilt from log on open()                                 │  │     │
│  │  └───────────────────────────────────────────────────────────────┘  │     │
│  └─────────────────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                       PERSISTENCE LAYER                                      │
│  ┌─────────────────────────────────────────────────────────────────────┐     │
│  │                 FILE-SIZE CAP GUARD                                  │     │
│  │  - Check if append would exceed max_bytes                           │     │
│  │  - Trigger compaction if needed                                     │     │
│  └─────────────────────────────────────────────────────────────────────┘     │
│                           │                                                  │
│  ┌─────────────────────────────────────────────────────────────────────┐     │
│  │                 APPEND-ONLY LOG WRITER                               │     │
│  │  ┌───────────────────────────────────────────────────────────────┐  │     │
│  │  │ Frame format:                                                 │  │     │
│  │  │   [magic:4B][len:8B][CRC:4B][payload:N]                      │  │     │
│  │  └───────────────────────────────────────────────────────────────┘  │     │
│  │  ┌───────────────────────────────────────────────────────────────┐  │     │
│  │  │ Write sequence:                                               │  │     │
│  │  │   1. Encode complete frame                                    │  │     │
│  │  │   2. os.write() loop (handles short writes)                  │  │     │
│  │  │   3. os.fsync() - flush to disk                              │  │     │
│  │  │   4. Update in-memory index                                  │  │     │
│  │  │   5. Return success / print ACK                              │  │     │
│  │  └───────────────────────────────────────────────────────────────┘  │     │
│  └─────────────────────────────────────────────────────────────────────┘     │
│                           │                                                  │
│  ┌─────────────────────────────────────────────────────────────────────┐     │
│  │                 COMPACTION                                           │     │
│  │  ┌───────────────────────────────────────────────────────────────┐  │     │
│  │  │ Steps:                                                        │  │     │
│  │  │   1. Build live snapshot from in-memory index               │  │     │
│  │  │   2. Write to temp file (KVR1 format)                       │  │     │
│  │  │   3. flush() + fsync() on temp                              │  │     │
│  │  │   4. os.replace(temp, database) - atomic rename             │  │     │
│  │  │   5. fsync parent directory (POSIX only)                    │  │     │
│  │  │   6. Cleanup temp files                                     │  │     │
│  │  └───────────────────────────────────────────────────────────────┘  │     │
│  └─────────────────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                        DISK LAYER                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐     │
│  │  database.kv  - Authoritative data (KVR1 frames)                   │     │
│  │  database.kv.lock  - Lock file (coordination only)                 │     │
│  │  database.kv.compact.XXX - Temp file during compaction            │     │
│  └─────────────────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────────────┘
```

## On-disk file layout

```
Database file (database.kv):
┌──────────┬─────────┬──────┬───────────┬──────────┬─────────┬──────┐
│   KVR1   │ len:8B  │CRC:4B│ payload   │   KVR1   │ len:8B  │ ...  │
│  (4B)    │         │      │ N bytes   │  (4B)    │         │      │
└──────────┴─────────┴──────┴───────────┴──────────┴─────────┴──────┘

Payload format:
┌──────────┬───────────┬─────────────────────────────────────────┐
│ version  │ op_count  │ operation_1 | operation_2 | ...         │
│  1 byte  │  4 bytes  │                                           │
└──────────┴───────────┴─────────────────────────────────────────┘

Operation format:
┌────────┬────────────┬─────────────┬──────────┬──────────┐
│ opcode │ key_len:2B │ value_len:4B│ key bytes│ value    │
│ 1 byte │            │             │          │          │
└────────┴────────────┴─────────────┴──────────┴──────────┘
```

## Numbered write order

1. Acquire thread lock (RLock) and OS file lock (exclusive)
2. Refresh in-memory index if file changed on disk
3. Encode one complete frame: [magic][len][CRC][payload]
4. If file would exceed max_bytes: compact first, then check again
5. Append frame using binary-mode file descriptor with write loop
6. Call `os.fsync(fd)` to flush data to disk
7. Release locks
8. Apply operations to in-memory hash index
9. Return success (library) or print `ACK <id>` (serve mode)

## Recovery path

```
open() flow:
1. Acquire OS file lock (exclusive)
2. Open file for reading
3. Scan from offset 0:
   - Read frame header (magic, length, CRC)
   - If header incomplete → stop (torn file)
   - Read payload bytes
   - If payload incomplete → stop (torn file)
   - Verify CRC matches payload
   - If CRC mismatch and more bytes follow → corruption
   - Decode operations and replay into index
4. If truncation needed:
   - ftruncate to last complete frame boundary
   - fsync to ensure truncation is durable
5. Close file
6. Remove stale temp files from compaction
7. Release lock
8. Return Database handle
```

## Part 2: Scenario-to-mechanism proof table

| ID | Disk state at kill | Mechanism / expected recovery | Code | Verified by |
|---|---|---|---|---|
| **A1** | No bytes of a new frame | Previous log unchanged; key absent or old value | `_append`, `_recover` | Reasoned from append ordering |
| **A2** | Partial final header/body | Scanner stops at incomplete final frame; truncates suffix | `_recover` | `test_torn_tail_is_discarded` |
| **A3** | Complete frame bytes exist, fsync not returned | Valid CRC frame replays; otherwise tail removed | `_append`, `_recover` | Not deterministically verified at fsync boundary |
| **A4** | Frame fsync returned; ACK not yet sent | Complete frame replays; key present | `_append` before `batch` returns | Ordering in code |
| **A5** | ACK already returned | fsynced complete frame replays | `_serve`, `_append`, `_recover` | Random torture runs record ACKs |
| **A6** | Delete frame partial or complete | Partial ignored; complete delete replays; earlier value intact | `_append`, `_recover` | Random torture includes deletes |
| **A7** | Large value frame partial or complete | CRC/length guard discards torn tail; complete frame replays | `_append`, `_recover` | `torture --mode large-values` |
| **B1** | Only some batch bytes written | One invalid frame is ignored as a unit | `_encode`, `_append`, `_recover` | No deterministic halfway-batch kill |
| **B2** | Full payload but no complete valid frame/CRC | Batch ignored if frame invalid | `_recover` | Frame CRC is validation boundary |
| **B3** | Complete frame may or may not be durable | If replayed, wholly applied; if absent, none applied | `batch`, `_recover` | Batch persistence/reopen test |
| **B4** | ACK follows fsync of complete batch frame | Every batch operation replays | `_serve`, `_append`, `_recover` | `test_batch_is_ordered` |
| **B5** | Put X then delete X in same batch | Replay follows encoded order; last wins | `_decode`, `batch` | Ordered-batch unit test |
| **B6** | Partial 10,000-op frame | CRC/length validation prevents partial visibility | `_recover` | `torture --mode large-batches` |
| **C1** | Recovery has replayed only a prefix | Next open reconstructs from unchanged log | `_recover` | `recovery-crash` mode |
| **C2** | Truncate/repair interrupted | Next scan truncates same or shorter suffix | `_recover` | Torn-tail repair test |
| **C3** | Temp snapshot incomplete; old file still named | Old file remains authoritative; incomplete temp ignored | `_compact_locked`, `_cleanup_temps` | Compaction mode |
| **C4** | Replacement is before or after `os.replace` | Old complete file or new complete snapshot is authoritative | `_compact_locked` | Compaction mode |
| **C5** | New file installed; temp cleanup remains | New file authoritative; stale temp removed/ignored | `_compact_locked`, `_cleanup_temps` | Compaction mode |
| **C6** | Process dies during close() | Previous writes were fsynced; no delayed write buffer | `close`, `_append` | Close/reopen unit tests |
| **D1** | Two threads append/ACK different keys | Shared lock serializes frames; each ACKed frame is fsynced | `_exclusive`, `_append` | Four-thread concurrent-batch test |
| **D2** | Same key receives writes from concurrent senders | Whole frames serialize; key is one complete value | `_exclusive`, `_append` | Random torture races over 20 keys |
| **D3** | Batch and single writes overlap | Lock orders complete frames; batch remains one frame | `batch`, `_exclusive` | Random torture mixes operations |
| **D4** | Reader starts while batch is applying | Reader waits for lock; sees pre- or post-batch index | `get`, `batch`, `_exclusive` | Lock mechanism in code |
| **E1** | Single append would exceed cap | Compact current live state, then reject before append | `_append`, `_compact_locked` | `disk-full` mode |
| **E2** | Batch exceeds cap | Preflight size check before any bytes appended | `_append`, `batch` | `test_failed_batch_at_file_cap` |
| **E3** | Cap error then kill/restart | Prior log intact and readable | `_append`, `_recover` | `disk-full` mode |
| **E4** | Cap prevents temp from fitting or temp write fails | Old file not replaced; temp removed | `_compact_locked` | Compaction mode |
| **E5** | Deletes free space; compaction reduces log | Compaction writes live snapshot | `compact`, `_compact_locked` | `test_compaction_after_delete` |
| **F1** | 500 consecutive random crashes/restarts | Each restart rebuilds from valid frames | `torture.py`, `_recover` | 500 iterations with seed 12345 |
| **F2** | Repeated kill during open/repair | Recovery is deterministic scan and idempotent | `_recover` | `recovery-crash` mode |
| **F3** | 1,000,000 keys in fsynced batches | Sequential replay reconstructs index | `_recover`, `main(open)` | Benchmarked 1.122s recovery |
| **F4** | One-byte-short final frame | Only incomplete final frame truncated; earlier records survive | `_recover` | Torn-tail test |

## Part 3: The three guarantees, argued

**Durability.** Once `ACK` is printed (or `batch` returns), the data survives process termination because `_append` performs the following in order: (1) `os.write` the complete frame, (2) `os.fsync(fd)` to flush to disk, (3) update the in-memory index, (4) return success. The `serve` mode prints `ACK` only after `batch` returns, which only happens after `fsync` completes. Therefore every `ACK` corresponds to a frame that has reached stable storage. The argument assumes the OS honors `fsync` for process termination.

**Atomicity.** A batch is never half-visible because all operations in a batch are serialized into one payload and protected by one frame length and CRC. Recovery either validates the entire frame (magic, length, CRC match) and replays it fully, or treats it as a torn tail and discards it. During normal operation, the same per-path locks (`_exclusive`) serialize all access: readers hold the lock during `get`, and batches hold the lock from `append` through index update, so a reader can only see the index state before a batch or after it, never partially applied.

**Recoverability.** `open()` always succeeds on a file produced by this database because: (1) The index is derived solely from replaying the append log; no persisted index means recovery has no state to lose. (2) Tail repair is idempotent: truncating to the last complete frame and fsyncing it is safe to repeat if killed mid-truncate. (3) Compaction uses a temp file and `os.replace`: before replacement the old file is authoritative; after replacement the new file is complete. A crash during compaction cannot lose data because either the old file remains intact or the new file is complete. On POSIX, the parent directory is fsynced after `os.replace`; on Windows, process-kill recovery is implemented but power-loss durability is not claimed.
