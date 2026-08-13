# 03 — Concurrency & Data Flow

This document details the goroutine lifecycle, channel buffering, backpressure, incremental synchronization, and data synchronization patterns.

---

## 1. Scanner Concurrency Model

The directory scanning pipeline uses a **Bounded Worker Pool with Dynamic Work Discovery**:

```
                          ┌───────────────────────┐
                          │   Root Path Enqueued  │
                          └───────────┬───────────┘
                                      │
                                      ▼
                        ┌───────────────────────────┐
                        │   dirChan (Work Queue)    │
                        │   chan string, cap: 10000 │
                        └─────────────┬─────────────┘
                                      │
                ┌─────────────────────┼─────────────────────┐
                ▼                     ▼                     ▼
         ┌──────────────┐      ┌──────────────┐      ┌──────────────┐
         │ Worker #1    │      │ Worker #2    │      │ Worker #N    │
         └──────┬───────┘      └──────┬───────┘      └──────┬───────┘
                │                     │                     │
                ├─► Discovered Subdirs ┴─► Enqueue to dirChan (atomic.Add(&active, +1))
                │
                └─► Discovered Regular Files (Classified & Tagged)
                                      │
                                      ▼
                        ┌───────────────────────────┐
                        │   metaChan (Backpressure) │
                        │   chan models.FileMetadata│
                        │   cap: 4096               │
                        └─────────────┬─────────────┘
                                      │
                                      ▼
                        ┌───────────────────────────┐
                        │ DB BatchWriter Goroutine  │
                        └─────────────┬─────────────┘
                                      │ (500 items / 50ms per tx)
                                      ▼
                        ┌───────────────────────────┐
                        │    SQLite WAL Database    │
                        └─────────────┬─────────────┘
                                      │
                                      ▼ (Post-Scan Stage)
                        ┌───────────────────────────┐
                        │   PruneDeletedFiles()     │ -> Purges removed files
                        └───────────────────────────┘
```

### Dynamic Work Discovery & Atomic In-Flight Counter
Because directory trees are discovered dynamically as workers traverse subfolders, standard static `sync.WaitGroup` patterns can cause deadlocks if workers finish their current item before child items are queued.

To guarantee deadlock-free termination:
1. An atomic counter `activeTasks` tracks all pending and active directory scans.
2. When the root directory is enqueued: `activeTasks = 1`.
3. When a worker discovers $K$ subdirectories: `atomic.AddInt64(&activeTasks, K)` before sending them to `dirChan`.
4. When a worker finishes processing a directory: `remaining := atomic.AddInt64(&activeTasks, -1)`.
5. When `remaining == 0`, all directories have been fully processed. The scanner safely closes `dirChan`, causing all workers to cleanly exit their `for dir := range dirChan` loops.

---

## 2. Channel Backpressure

If filesystem reads are faster than disk persistence (or vice versa), unconstrained buffering can cause unbounded RAM growth.

- `metaChan` is initialized with a bounded capacity of `4096` items.
- If the SQLite `BatchWriter` slows down (e.g., during disk write spikes), `metaChan` fills up.
- Workers trying to send to a full channel naturally block on `metaChan <- meta`.
- This applies **instant backpressure** upstream, throttling directory traversal without consuming additional heap memory.

---

## 3. Incremental Re-Scanning & Deletion Pruning (Phase 4)

In long-running operations and recurring scans, re-scanning unchanged files is redundant:

```
[Incoming FileMetadata]
          │
          ▼
   [UPSERT In DB] ─── Has (mtime, size) Changed? ───┐
          │                                         │
          ├─► NO  (Unchanged)                       ├─► YES (Modified)
          │   Preserve content_hash                 │   Invalidate content_hash = NULL
          │   Preserve staleness_score              │   Invalidate staleness_score = NULL
          │   Update last_scanned_at                │   Update last_scanned_at
```

### Post-Scan Deletion Reconciliation:
- After `metaChan` drains and the transaction closes, `database.PruneDeletedFiles()` queries all records within the scan root where `last_scanned_at < scanStartTime`.
- Each candidate is checked with `os.Lstat()`. If the file no longer exists, it is deleted from the SQLite database in an atomic batch.

---

## 4. Two-Pass Duplicate Detection Pipeline

```
  [All Files in DB]
          │
          ▼
  ┌────────────────────────────────────────────────────────┐
  │ Pass 1: Size-Bucket Query                              │
  │ SELECT size FROM files GROUP BY size HAVING COUNT(*) > 1│
  └────────────────────────┬───────────────────────────────┘
                           │
                           ▼ (Candidate Files Only)
  ┌────────────────────────────────────────────────────────┐
  │ Pass 2: Bounded Worker Pool (crypto/sha256)            │
  │ • Fixed 64 KB streaming buffer per worker              │
  │ • Concurrent SHA-256 computation                       │
  │ • Batch update content_hash into SQLite                │
  └────────────────────────┬───────────────────────────────┘
                           │
                           ▼
  ┌────────────────────────────────────────────────────────┐
  │ Duplicates Query & Space Savings Aggregation           │
  │ SELECT content_hash, COUNT(*), SUM(size)               │
  │ GROUP BY content_hash, size HAVING COUNT(*) > 1        │
  └────────────────────────────────────────────────────────┘
```
