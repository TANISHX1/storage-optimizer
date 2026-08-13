# Intelligent Storage Optimizer (Linux)

A high-performance, concurrency-safe desktop systems tool for Linux that concurrently scans filesystem trees, indexes metadata locally with SQLite, classifies system vs user assets, identifies duplicate and stale files, forecasts disk usage growth, and executes human-confirmed cleanup actions safely.

---

## System Architecture

```
storage-optimizer/
├── go-core/              # Systems Core: concurrent scanner, dedup, staleness, action layer, local HTTP API
│   ├── cmd/storage-optimizer/ # CLI entrypoint & server orchestrator
│   ├── internal/
│   │   ├── models/       # Shared domain structs (FileMetadata, DuplicateGroup, ActionLog)
│   │   ├── db/           # SQLite connection, WAL management, Single-Writer funnel
│   │   ├── scanner/      # Concurrent directory walker, VFS stat extractor, category classifier
│   │   ├── dedup/        # Two-pass duplicate detector (Size Buckets -> Streaming SHA-256)
│   │   ├── stale/        # Staleness scoring engine (exponential age decay + path heuristics)
│   │   ├── action/       # User-confirmed action layer & audit logger (Phase 6)
│   │   └── api/          # Local HTTP REST API (Phase 5)
│   └── go.mod
├── gui/                  # Desktop GUI shell (Wails + Web Frontend)
│   ├── frontend/         # UI navigation, action dialogs, and Sahil's data visualizations
│   └── wails.json
├── python-layer/         # Forecast & recommendation engine (Sahil - Day 7)
│   ├── forecast/         # Time-series regression from scan_snapshots
│   └── recommend/        # Plain-language cleanup recommendation generator
│   └── service.py        # Python FastAPI / requests microservice consuming Go HTTP REST API
├── shared/
│   └── schema.sql        # Canonical SQLite schema (Single Source of Truth)
├── data/
│   └── optimizer.db      # Runtime SQLite database (Single-writer owned by Go)
├── docs/                 # Full technical documentation suite (7 guides)
└── README.md
```

---

## Project Status & Implementation Roadmap

| Phase | Description | Status | Key Deliverables |
| :--- | :--- | :---: | :--- |
| **Phase 0** | **Project Scaffolding & Architecture** | ✅ Completed | Repo layout, canonical `schema.sql`, complete `docs/` suite, Go module setup. |
| **Phase 1** | **Concurrent Walker & Metadata Capture** | ✅ Completed | Bounded worker pool (`NumCPU`), `os.Lstat` symlink safety, `syscall.Stat_t` Linux Inode/atime/mtime extraction, Single-Writer channel funnel, SQLite WAL batching (500 items/50ms), `storage-optimizer scan <path>` CLI. |
| **Phase 2** | **Duplicate Detection Engine** | ✅ Completed | Two-pass strategy: Pass 1 size-bucket filter + Pass 2 parallel streaming SHA-256 (64 KB chunk buffers), atomic hash batch updater, `storage-optimizer duplicates` CLI reporting reclaimable wasted bytes. |
| **Phase 3** | **Unused / Stale File Scoring & System Classification** | ✅ Completed | `mtime`/`atime` exponential decay scoring, path/extension weighting matrix, 6-tier system file classification (`system_protected`, `system_log`, `crash_dump`, `temp`, `system_cache`, `user`), CLI `storage-optimizer stale --days N`. |
| **Phase 4** | **Incremental Re-Scan & Deletion Pruning** | ✅ Completed | `mtime`/`size` diffing with automatic hash invalidation on modification, stale row pruning for deleted files, time-series `scan_snapshots` logging, CLI `storage-optimizer snapshots`. |
| **Phase 5** | **Local HTTP REST API** | ⏳ **Next Up** | Standard Go `net/http` REST endpoints (`/api/v1/...`) unblocking Sahil on Day 7. |
| **Phase 6** | **Action Layer (Trash & Delete)** | ⏳ Pending | User-confirmed `trash` (relocate to `<app_data>/trash/` with restore) and `permanent` (`os.Remove`), pre-action existence validation, and mandatory `actions_log` audit trail. |
| **Phase 7** | **GUI Application Shell (Wails)** | ⏳ Pending | Wails desktop window, sidebar navigation, action confirmation dialogs, wiring to Go HTTP API, placeholder views for Sahil's charts. |
| **Phase 8** | **Benchmarking & Hardening** | ⏳ Pending | Synthetic directory stress testing (100k+ files), memory leak auditing, SQLite contention verification. |

---

## Key Architectural Principles

1. **Go Owns All SQLite Writes**:
   - SQLite concurrent writes easily hit lock contention (`database is locked`).
   - The Go core is the **sole writer** to `data/optimizer.db` via a funnel-to-single-writer pattern.
2. **Local HTTP API as the Common Bridge**:
   - Both the **GUI frontend** and Sahil's **Python Layer** communicate via local HTTP REST endpoints exposed by Go (`127.0.0.1:8080`).
   - Python consumes `/api/v1/snapshots`, `/api/v1/files/duplicates`, and `/api/v1/files/stale` as a regular HTTP client, avoiding direct SQLite file locking conflicts.
3. **Safety First in Actions & OS Directory Protection**:
   - No automatic deletions.
   - Critical Linux system paths (`/etc`, `/usr`, `/boot`, `/lib`) are tagged as `system_protected` with a safety penalty ($0.01$) to shield them from cleanup, while junk logs and crash dumps are flagged for cleaning.
   - Every cleanup requires explicit user confirmation (`trash` or `permanent`).
   - Every action is logged to `actions_log` before execution.
4. **Performance & Systems Integrity**:
   - `os.Lstat` prevents circular symlink loops.
   - Streaming SHA-256 chunk buffers (64 KB) prevent RAM bloat on multi-gigabyte files.
   - Bounded goroutine pools prevent file descriptor exhaustion (`EMFILE`).

---

## Quickstart & CLI Commands

### 1. Build the Binary
```bash
cd /home/blazex/Documents/git/storage-optimizer/go-core
go build -o bin/storage-optimizer cmd/storage-optimizer/main.go
```

### 2. Scan a Directory (with Incremental Sync & Pruning)
```bash
./bin/storage-optimizer scan /path/to/scan
```
*Benchmark: Scanned 109,041 real files (1.43 GB) in 4.004s at 27,230.5 files/sec.*

### 3. Find Duplicates & Calculate Wasted Space
```bash
./bin/storage-optimizer duplicates
```

### 4. Find Stale & Inactive Files
```bash
# Find files untouched for 60+ days
./bin/storage-optimizer stale --days 60
```

### 5. View Scan Snapshots History (Time-Series for Python)
```bash
./bin/storage-optimizer snapshots
```

---

## Documentation Links

Complete in-depth documentation is located in the [`docs/`](docs/README.md) directory:
- [01. Architecture & Design](docs/01-architecture-and-design.md)
- [02. Systems Programming & Linux Internals](docs/02-systems-programming-and-linux.md)
- [03. Concurrency & Data Flow](docs/03-concurrency-and-data-flow.md)
- [04. Database & Schema Contract](docs/04-database-and-schema.md)
- [05. Local HTTP API & Integration Contract](docs/05-api-and-python-gui-contract.md)
- [06. Operations & CLI Reference](docs/06-operations-and-cli.md)
