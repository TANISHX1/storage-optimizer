# Intelligent Storage Optimizer (Linux)

A high-performance, concurrency-safe desktop systems tool for Linux that concurrently scans filesystem trees, indexes metadata locally with SQLite, identifies duplicate and stale files, forecasts disk usage growth, and executes human-confirmed cleanup actions safely.

---

## System Architecture

```
storage-optimizer/
├── go-core/              # Systems Core: concurrent scanner, dedup, staleness, action layer, local HTTP API
│   ├── cmd/storage-optimizer/ # CLI entrypoint & server orchestrator
│   ├── internal/
│   │   ├── models/       # Shared domain structs (FileMetadata, DuplicateGroup, ActionLog)
│   │   ├── db/           # SQLite connection, WAL management, Single-Writer funnel
│   │   ├── scanner/      # Concurrent directory walker, VFS stat extractor
│   │   ├── dedup/        # Two-pass duplicate detector (Size Buckets -> Streaming SHA-256)
│   │   ├── stale/        # Staleness scoring engine (Phase 3)
│   │   ├── action/       # User-confirmed action layer & audit logger (Phase 6)
│   │   └── api/          # Local HTTP REST API (Phase 5)
│   └── go.mod
├── gui/                  # Desktop GUI shell (Wails + Web Frontend)
│   ├── frontend/         # UI navigation, action dialogs, and Sahil's data visualizations
│   └── wails.json
├── python-layer/         # Forecast & recommendation engine (Sahil - Day 7)
│   ├── forecast/         # Time-series regression from scan_snapshots
│   └── recommend/        # Plain-language cleanup recommendation generator
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
| **Phase 3** | **Unused / Stale File Scoring** | ⏳ **Next Up** | `mtime`/`atime` decay scoring, path/extension weighting penalties (`.git`, `node_modules`, dotfiles), CLI `storage-optimizer stale --days N`. |
| **Phase 4** | **Incremental Re-Scan Logic** | ⏳ Pending | `mtime` diffing (skip hashing unchanged files), stale row cleanup for deleted files, time-series `scan_snapshots` population. |
| **Phase 5** | **Local HTTP REST API** | ⏳ Pending | Standard Go `net/http` REST endpoints (`/scan`, `/files/duplicates`, `/files/stale`, `/snapshots`, `/actions`) unblocking Sahil on Day 7. |
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
   - Python consumes `/snapshots`, `/files/duplicates`, and `/files/stale` as a regular HTTP client (`httpx` or `requests`), avoiding direct SQLite file locking conflicts.
3. **Safety First in Actions**:
   - No automatic deletions.
   - Every cleanup requires explicit confirmation (`trash` or `permanent`).
   - Every action is logged to `actions_log` before execution.
   - Path and existence sanity checks are verified immediately prior to executing any filesystem change.
4. **Performance & Systems Integrity**:
   - `os.Lstat` prevents circular symlink loops.
   - Streaming SHA-256 chunk buffers prevent RAM bloat on multi-gigabyte files.
   - Bounded goroutine pools prevent file descriptor exhaustion (`EMFILE`).

---

## Quickstart & CLI Reference

### 1. Build the Binary
```bash
cd go-core
go build -o bin/storage-optimizer cmd/storage-optimizer/main.go
```

### 2. Scan a Directory
```bash
# Concurrently index metadata for any Linux folder
./bin/storage-optimizer scan /path/to/scan
```

### 3. Find Duplicates & Calculate Wasted Space
```bash
# Identify duplicate clusters and calculate reclaimable storage
./bin/storage-optimizer duplicates
```

---

## Documentation Links

Complete in-depth documentation is located in the [`docs/`](file:///home/blazex/Documents/git/storage-optimizer/docs/README.md) directory:
- [01. Architecture & Design](file:///home/blazex/Documents/git/storage-optimizer/docs/01-architecture-and-design.md)
- [02. Systems Programming & Linux Internals](file:///home/blazex/Documents/git/storage-optimizer/docs/02-systems-programming-and-linux.md)
- [03. Concurrency & Data Flow](file:///home/blazex/Documents/git/storage-optimizer/docs/03-concurrency-and-data-flow.md)
- [04. Database & Schema Contract](file:///home/blazex/Documents/git/storage-optimizer/docs/04-database-and-schema.md)
- [05. Local HTTP API & Integration Contract](file:///home/blazex/Documents/git/storage-optimizer/docs/05-api-and-python-gui-contract.md)
- [06. Operations & CLI Reference](file:///home/blazex/Documents/git/storage-optimizer/docs/06-operations-and-cli.md)
