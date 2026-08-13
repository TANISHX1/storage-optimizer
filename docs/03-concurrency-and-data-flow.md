# 03 — Concurrency & Data Flow

This document details the goroutine lifecycle, channel buffering, backpressure, and data synchronization patterns.

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
                └─► Discovered Regular Files
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

## 3. The Funnel-to-Single-Writer Pattern

SQLite uses file-level locking. Multiple concurrent goroutines opening transactions simultaneously produce `database is locked` errors.

### The Solution:
- Only **one dedicated goroutine** (`BatchWriter`) executes write transactions (`INSERT`, `UPDATE`, `DELETE`) against the database.
- Scanner workers, duplicate detectors, and action handlers act purely as producers, sending structs across Go channels.
- The `BatchWriter` collects items into memory slices and commits them in batches of 500 items (or on a 50ms ticker timeout), turning thousands of single disk writes into a single batched WAL commit.
