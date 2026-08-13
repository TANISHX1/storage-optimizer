# 01 — System Architecture & Design

## 1. System Vision & Problem Statement

Modern developer workstations, servers, and build environments suffer from unmonitored disk storage bloat. Heavy dependencies (`node_modules`, `vendor/`, `venv/`), duplicate assets, unrotated logs (`/var/log`), core dumps (`/var/crash`), and stale temporary files (`/tmp`) silently consume gigabytes of valuable drive capacity.

The **Intelligent Storage Optimizer** is a high-performance, concurrency-safe systems tool built in Go for Linux that:
1. **Concurrently indexes filesystem hierarchies** without kernel resource exhaustion using bounded worker pools.
2. **Classifies files into system and user categories** (`system_protected`, `system_log`, `crash_dump`, `temp`, `system_cache`, `user`) to protect critical OS assets while highlighting cleanable junk.
3. **Identifies exact duplicates** using an I/O-optimized two-pass algorithm (size buckets $\rightarrow$ streaming SHA-256 in 64 KB buffers).
4. **Calculates intelligent staleness scores** ($0.0$ to $1.0$) combining exponential age decay with system and user path heuristics.
5. **Performs incremental re-scanning** via `mtime` diffing, automatically invalidating stale hashes on modification and purging deleted rows.
6. **Maintains historical time-series snapshots** (`scan_snapshots`) for Python growth forecasting and regression.
7. **Executes human-approved cleanup actions** (`trash` or `permanent`) with pre-execution safety validation, OS path protection gates, and mandatory audit logging.

---

## 2. Component Topology

```
storage-optimizer/
├── go-core/              # Systems Core (Go 1.25+)
│   ├── cmd/storage-optimizer/ # CLI entrypoint & server orchestrator
│   ├── internal/
│   │   ├── models/       # Domain data transfer objects & entity structs
│   │   ├── db/           # SQLite connection, WAL management, Single-Writer funnel
│   │   ├── scanner/      # Concurrent directory walker, VFS stat extractor, category classifier
│   │   ├── dedup/        # Two-pass duplicate detector (streaming SHA-256)
│   │   ├── stale/        # Staleness scoring engine (mtime/atime saturation curve)
│   │   ├── action/       # Audit-logged cleanup action executor (Phase 6)
│   │   └── api/          # Local HTTP REST API server (Phase 5)
│   └── go.mod
├── gui/                  # Wails Desktop Shell (Go + Web Frontend)
├── python-layer/         # Analytics & Forecasting Microservice (Sahil - Day 7)
├── shared/
│   └── schema.sql        # Canonical SQLite schema (Single Source of Truth)
├── data/
│   └── optimizer.db      # Runtime SQLite database
└── docs/                 # Complete system documentation
```

---

## 3. Core Architectural Decisions

### 3.1 Go as the Single Writer to SQLite
**Problem**: SQLite supports multiple simultaneous readers in WAL mode, but only **one write transaction** can hold the database lock. If multiple worker goroutines or external Python scripts attempt concurrent writes, SQLite throws `sqlite3: database is locked (5)`.

**Solution**:
- All write operations are channeled through a dedicated Go background goroutine (`BatchWriter`).
- External components (Python analytics, GUI frontend) never write to the `.db` file directly; they make HTTP REST calls to the Go core.

### 3.2 System Directory Safety & Classification Gating
**Problem**: Naive storage cleanups can delete essential OS configuration files (`/etc`), binaries (`/bin`, `/usr/lib`), or critical packages, bricking the Linux operating system.

**Solution**:
- Every indexed file is classified into structured categories during scanning.
- Files categorized under `CategorySystemProtected` are given a staleness score penalty ($0.01$), keeping them completely off cleanup suggestion lists, and are hard-blocked from deletion in the action execution engine.
- Cleanable system junk like unrotated `/var/log` files, `/tmp` locks, and `/var/crash` dumps are highlighted with priority boosts.

### 3.3 Canonical Schema as Contract
- `shared/schema.sql` is the canonical schema source of truth.
- Both Go and Python share this contract without schema drift.
- Go automatically applies this schema during initialization with idempotency (`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`).

### 3.4 Two-Tier Action Safety Model
- **No Automatic Deletion**: Neither duplicate detection nor staleness analysis ever deletes files automatically.
- **Explicit Confirmation**: Every destructive request must declare a mode (`trash` or `permanent`).
- **Audit Logging**: An entry in `actions_log` is written *before* any filesystem mutation.
- **Freshness Validation**: The file path, inode, and existence are verified immediately prior to execution to prevent acting on stale indices.
