# How I would break this design

## Crash points

| Point | Bytes possibly on disk | Next `open` action |
|---|---|---|
| `Database._append`, before `os.open` | Prior complete log only | Replay prior frames |
| `_append`, after opening/positioning but before first write | Prior log only | Replay prior frames |
| `_append`, inside the frame write loop | Partial final frame | Stop at incomplete/invalid tail; truncate to last valid frame |
| `_append`, after full write but before `os.fsync` | Complete, partial, or not-yet-stable final frame | Replay only if full frame and CRC validate; otherwise truncate |
| `_append`, after `os.fsync` and before `batch` updates index | Durable complete frame, no ACK yet | Replay it; an unacknowledged operation is allowed to be present |
| `batch`, while updating in-memory state | Durable frame; no other reader can enter due to lock | Process death loses memory; replay entire frame |
| `_recover`, while reading a frame | Original log untouched | Scan the same file again from byte zero |
| `_recover`, after detecting a torn tail but before `ftruncate` | Original log including torn suffix | Detect and retry the same repair |
| `_recover`, during/after `ftruncate` before its `fsync` | Original or shorter prefix | Re-scan and truncate again if needed |
| `_compact_locked`, while writing temp snapshot | Old database plus incomplete temp | Ignore/delete temp; old file remains authoritative |
| `_compact_locked`, after temp fsync and before `os.replace` | Old database plus complete temp | Ignore/delete temp; old file remains authoritative |
| `_compact_locked`, during/after `os.replace` | Old or new complete name binding (process-kill model) | Scan whichever complete file is at the database path |
| `_compact_locked`, after replace before POSIX directory fsync | New complete file visible; directory metadata not power-loss-guaranteed yet | Process-kill model sees new file; power-loss behavior is not claimed on Windows |

## Ordering argument

Each successful write is one append-only frame. `os.write` completes before `os.fsync`; `batch` changes the index only after the fsync returns; `serve` emits ACK only after `batch` returns. A batch has one frame, not one frame per member, so a validated frame is all-or-nothing. `open` accepts only complete frames with matching CRC and truncates an invalid tail to the last valid offset. Compaction writes a sibling temp, flushes and `os.fsync`s it, then calls `os.replace`; on POSIX it also fsyncs the parent directory. The sidecar lock serializes writers and replacement against reads. This is a process-termination argument, not a universal power-loss proof.

## Known weaknesses

1. Recovery is O(log bytes) and the index is not persisted. A 1,000,000-key log replay measured 1.122 seconds on this Windows host using 100 batches; this is not a cross-machine guarantee.
2. A very large live snapshot is encoded in memory as one frame during compaction. The supported operation limits do not imply a safe memory bound for extreme 10,000-operation batches containing 1 MB values.
3. The cross-process Windows lock uses `msvcrt.locking`; behavior has been exercised on this Windows host, not on every filesystem or network share. Network filesystems are out of scope.
4. On Windows, the code does not fsync the containing directory after `os.replace`; process-kill consistency is the target, not power-loss consistency.
5. CRC-32 detects torn/accidentally changed frames but is not cryptographic and does not repair interior bit rot. Interior corruption intentionally fails `open` rather than silently skipping data.
6. The 500-iteration random run passed with one seed, but random scheduling does not prove exact crash-boundary coverage; the targeted modes should be repeated on the judging laptop.

## What failed during development

The first concurrent-write torture pass exposed corrupted frame bytes on Windows. Raw file inspection showed newline translation in a file descriptor opened without `O_BINARY`; the CRC and payload no longer matched. A four-thread batch test reproduced the failure. Adding `O_BINARY` to data-file descriptors made the test pass and is retained as a regression test. This was an implementation bug, not a failed storage design.

The first torture workload killed the child before it had acknowledged any operation because process startup dominated its short random delay. A durable warmup request now precedes the randomized workload, and the oracle logs each ACK separately. The random crash still does not prove a kill at any exact instruction boundary.

## If we had 24 more hours

1. Add deterministic failpoints around each write, fsync, recovery truncate, and compaction rename; run each kill point on Windows and POSIX.
2. Add a memory-bounded streaming encoder/decoder and benchmark recovery, compaction, and 1,000,000-key workloads on the actual judging laptop.
3. Extend tests for multiple long-running writer processes, concurrent readers during a 10,000-op batch, capped compaction followed by delete/compact/rewrite, and automatic preservation of failing oracle logs.
