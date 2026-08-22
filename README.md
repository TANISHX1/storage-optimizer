# Intelligent Storage Optimizer (Linux)

A high-performance, concurrency-safe desktop systems tool for Linux that concurrently scans filesystem hierarchies, indexes metadata locally with SQLite, classifies system vs user assets, detects duplicates and stale files, forecasts disk usage growth, and executes human-confirmed cleanup actions safely.

---

## 1. Full System Architecture

```
                                  ┌────────────────────────────────────────┐
                                  │       Desktop GUI Shell (Wails v2)     │
                                  │      (HTML5 / CSS3 / Vanilla JS)       │
                                  └───────────────────┬────────────────────┘
                                                      │ HTTP / REST
                                                      ▼
 ┌───────────────────────────────────┐   ┌────────────────────────────────────────┐
 │   Python Layer (Analytics & ML)   │──►│       Go Systems Core (HTTP API)       │
 │   Time-Series Growth Forecasting  │   │       Port: 127.0.0.1:8080             │
 └───────────────────────────────────┘   └────────────────────┬───────────────────┘
                                                              │
                               ┌──────────────────────────────┴──────────────────────────────┐
                               ▼                                                             ▼
                ┌─────────────────────────────┐                               ┌─────────────────────────────┐
                │   Concurrent FS Scanner     │                               │   Action & Safety Engine    │
                │   • Bounded Worker Pool     │                               │   • Pre-action Inode Gate   │
                │   • VFS / Inode Stat Extr.  │                               │   • FreeDesktop XDG Trash   │
                │   • Two-Pass Deduplication  │                               │   • Immutable Audit Logger  │
                └──────────────┬──────────────┘                               └──────────────┬──────────────┘
                               │                                                             │
                               │        ┌───────────────────────────────────────────┐        │
                               └───────►│ Channel Funnel (chan models.FileMetadata) │◄───────┘
                                        └─────────────────────┬─────────────────────┘
                                                              │
                                                              ▼
                                                ┌───────────────────────────┐
                                                │ DB BatchWriter Goroutine  │ (Single Writer)
                                                └─────────────┬─────────────┘
                                                              │
                                                              ▼
                                                ┌───────────────────────────┐
                                                │    SQLite DB (WAL Mode)   │
                                                │     data/optimizer.db     │
                                                └───────────────────────────┘
```

---

## 2. Component Breakdown & How Everything Works

### 2.1 Concurrent POSIX Scanner & Metadata Walker (`internal/scanner`)
- **Bounded Worker Pool**: Discovers directories recursively and queues them into a bounded work channel (`chan string`). Workers (`NumWorkers = runtime.NumCPU() * 2`) consume directory paths in parallel, eliminating file descriptor exhaustion (`EMFILE`).
- **Low-Level Syscall Extraction**: Uses `os.Lstat` (avoiding circular symlink loops) and casts `FileInfo.Sys()` to `*syscall.Stat_t` to extract Linux Inode numbers (`stat.Ino`), device IDs (`stat.Dev`), atime (`stat.Atim.Sec`), and ctime (`stat.Ctim.Sec`).
- **Path Classification**: Classifies every file upon discovery into 6 distinct categories (`system_protected`, `system_log`, `crash_dump`, `temp`, `system_cache`, `user`) based on file extension and Linux system hierarchy rules.
- **Incremental Rescanning**: On subsequent scans of the same path, the scanner checks existing records in SQLite. If `mtime` or `size` has changed, the file's hash is cleared to trigger re-computation. Missing files are marked as `is_deleted = 1`.

### 2.2 SQLite Storage Engine & Single-Writer Funnel (`internal/db`)
- **Single-Writer Funnel Pattern**: To eliminate SQLite concurrent write lock contention (`database is locked`), worker goroutines never write to SQLite directly. They push `FileMetadata` structs into a buffered Go channel (`chan FileMetadata`, capacity `5000`).
- **Atomic Batch Writes**: A dedicated `BatchWriter` goroutine drains the channel and executes bulk UPSERTs inside atomic transactions (`BEGIN IMMEDIATE TRANSACTION ... COMMIT`) every **500 records** or **50 milliseconds**.
- **WAL Performance Tuning**:
  - `PRAGMA journal_mode = WAL;` (Concurrent readers while writing).
  - `PRAGMA synchronous = NORMAL;` (High write throughput without corrupting WAL).
  - `PRAGMA cache_size = -64000;` (64 MB in-memory page cache).
  - `PRAGMA temp_store = MEMORY;` (RAM-based sorting).

### 2.3 Two-Pass Deduplication Engine (`internal/dedup`)
- **Pass 1 (Size Filtering)**: Groups active files by `size HAVING COUNT(*) > 1`. Files with unique sizes across the storage pool are excluded immediately, saving 80–90% of disk read I/O.
- **Pass 2 (Streaming Cryptographic Hashing)**: Files sharing identical sizes that lack a stored hash are processed in parallel using a bounded worker pool. Files are read through **64 KB streaming buffers** (`io.CopyBuffer` with `crypto/sha256`), guaranteeing flat memory consumption even on massive files.
- **Cluster Aggregation**: Files sharing the same SHA-256 hash are grouped into `DuplicateGroup` clusters. The oldest copy by `mtime` is elected as the primary original (`IsOriginal = true`), and wasted bytes are computed as $\text{FileSize} \times (\text{Count} - 1)$.

### 2.4 Mathematical Staleness Scoring Engine (`internal/stale`)
Ranks inactive and junk files on a normalized scale from **$0.00$ to $1.00$** using an exponential saturation decay formula:

$$\text{StalenessScore} = \text{Clamp}\Big(\Big[1 - e^{-\lambda \cdot t_{\text{inactive}}}\Big] \times W_{\text{category}} \times W_{\text{path}} \times W_{\text{size}}, \; 0.0, \; 1.0\Big)$$

- **Inactivity Time ($t_{\text{inactive}}$)**: Days elapsed since $\max(\text{atime}, \text{mtime})$.
- **Decay Rate ($\lambda = 0.015$)**: 60 days $\approx 0.63$, 180 days $\approx 0.95$.
- **Category Weights**: Crash dumps ($1.50$), Temporary files ($1.40$), and Caches ($1.25$) are prioritized. User documents/code ($0.85$) receive conservative scores. System protected files are locked at $0.00$.

### 2.5 Action, Safety & FreeDesktop XDG Trash Engine (`internal/action`)
- **Safety Pre-Checks**:
  - Absolute path validation against Linux system blocklists (`/etc`, `/usr`, `/boot`, `/lib`, `/sys`, `/proc`, `/dev`).
  - Pre-execution filesystem check: compares current on-disk `Inode` and `Size` against database metadata to prevent TOCTOU race conditions.
- **FreeDesktop.org XDG Trash Standard**: In `trash` mode, files are moved to `~/.local/share/Trash/files/` and an RFC-compliant `.trashinfo` metadata file is written to `~/.local/share/Trash/info/`, enabling native restoration via GNOME Files / Dolphin.
- **Restoration Engine**: Restores trashed files back to their original disk paths, recreates parent directories if needed, updates the SQLite index, and marks the audit record as `restored`.
- **Immutable Audit Trail**: Every cleanup action is recorded in `actions_log` with file IDs, paths, sizes, action modes, and status.

### 2.6 Local HTTP REST API Server (`internal/api`)
- Runs on `127.0.0.1:8080` using standard Go `net/http`.
- Serves live scan progress feeds, aggregate statistics, duplicate clusters, stale file lists, directory hierarchy lookups, snapshot histories, and action executions.
- Embeds and serves the production web frontend at `/`.

### 2.7 Python Analytics & Growth Forecasting Layer (`python-layer/`)
- Consumes `/api/v1/snapshots` and `/api/v1/stats` over HTTP.
- Fits time-series growth trajectories using linear and polynomial regression models.
- Estimates "days-until-full" based on current partition capacity and daily growth velocity.
- Generates plain-language cleanup recommendations.

### 2.8 Desktop GUI Shell & Embedded Web Dashboard (`gui/`)
- **Dual-Mode Desktop Shell**: Built with Wails v2 (linking native Linux `webkit2gtk-4.1`), compiling down to an **8.3 MB** executable that consumes only ~32 MB of RAM.
- **Modern Design**: Apple Human Interface Guidelines (HIG) dark mode aesthetic with smooth animations, custom segmented controls, interactive charts, and real-time tab filtering (`⌘K`).

---

## 3. Directory Layout & Module Map

```
storage-optimizer/
├── go-core/                          # Systems Core (Go 1.25+)
│   ├── cmd/storage-optimizer/main.go # CLI entrypoint & server orchestrator
│   ├── internal/
│   │   ├── models/models.go          # Domain structs & category constants
│   │   ├── scanner/scanner.go        # Concurrent walker, stat extractor, diff engine
│   │   ├── db/db.go                  # SQLite connection, WAL PRAGMAs, BatchWriter funnel
│   │   ├── dedup/dedup.go            # 2-pass duplicate engine (streaming SHA-256)
│   │   ├── stale/stale.go            # Exponential decay staleness scoring
│   │   ├── action/action.go          # XDG Trash, deletion gates, restore, audit logger
│   │   └── api/api.go                # REST API routes & embedded frontend static server
│   └── go.mod
├── gui/                              # Desktop GUI (Wails v2 + Web Frontend)
│   ├── frontend/                     # HTML5, CSS3, Vanilla JS application
│   │   ├── index.html                # Single-page application markup
│   │   ├── style.css                 # macOS HIG Dark theme styling
│   │   └── app.js                    # State management, API hooks & charts
│   └── wails.json
├── python-layer/                     # Analytics & Forecasting Microservice
│   ├── service.py                    # FastAPI service consuming Go REST API
│   ├── forecast/                     # Time-series growth regression
│   └── recommend/                    # Rule-based cleanup recommendations
├── shared/
│   └── schema.sql                    # Canonical SQLite schema (Single Source of Truth)
├── data/
│   └── optimizer.db                  # Runtime SQLite database
├── docs/                             # In-depth technical guides (7 documents)
│   ├── 01-architecture-and-design.md
│   ├── 02-systems-programming-and-linux.md
│   ├── 03-concurrency-and-data-flow.md
│   ├── 04-database-and-schema.md
│   ├── 05-api-and-python-gui-contract.md
│   ├── 06-operations-and-cli.md
│   ├── 07-go-core-modules-guide.md
│   └── README.md
└── README.md
```

---

## 4. Quickstart & CLI Reference

### 4.1 Building from Source

```bash
# 1. Build Go Systems Core CLI
cd go-core
go build -o bin/storage-optimizer cmd/storage-optimizer/main.go

# 2. Build Frontend Distribution
cd ../gui/frontend
npm install && npm run build
```

### 4.2 CLI Commands

```bash
# Scan and index a directory hierarchy (with incremental sync & pruning)
./bin/storage-optimizer scan /path/to/scan --workers 16

# Find duplicate files and calculate wasted disk space
./bin/storage-optimizer duplicates --limit 50

# List stale and inactive files untouched for N days
./bin/storage-optimizer stale --days 60 --limit 100

# View historical scan snapshots
./bin/storage-optimizer snapshots

# Start local HTTP REST API server and web UI
./bin/storage-optimizer serve --port 8080

# Move files to FreeDesktop XDG Trash (~/.local/share/Trash/)
./bin/storage-optimizer delete --ids 101,102 --mode trash

# Permanently delete files (with audit logging)
./bin/storage-optimizer delete --ids 103 --mode permanent

# Restore a previously trashed file
./bin/storage-optimizer restore --id 1

# View immutable audit trail of past cleanup actions
./bin/storage-optimizer actions --limit 50
```

---

## 5. Local REST API Reference (`127.0.0.1:8080`)

| Method | Route | Description |
| :--- | :--- | :--- |
| `GET` | `/api/v1/health` | Service health status and uptime |
| `GET` | `/api/v1/stats` | Storage totals, duplicate bytes, and category breakdowns |
| `POST`| `/api/v1/scan` | Trigger background filesystem scan (`{"path":"...", "workers":12}`) |
| `GET` | `/api/v1/scan/status` | Live scan progress feed (file counts, current path, ETA) |
| `GET` | `/api/v1/files/duplicates` | Paginated duplicate clusters sharing SHA-256 checksums |
| `GET` | `/api/v1/files/duplicates/breakdown` | Top duplicate file extension breakdown analytics |
| `GET` | `/api/v1/files/stale` | Ranked stale/inactive files by inactivity days |
| `GET` | `/api/v1/files/stale/breakdown` | Top stale file extension breakdown analytics |
| `GET` | `/api/v1/browse` | Lazy directory hierarchy navigation |
| `GET` | `/api/v1/snapshots` | Historical scan snapshots for time-series charts |
| `POST`| `/api/v1/actions` | Execute batch trash or permanent deletion |
| `POST`| `/api/v1/actions/restore`| Restore trashed file back to disk |
| `GET` | `/api/v1/actions/history`| Audit log records |

---

## 6. Benchmarks & Performance Verification

| Metric | Target Standard | Measured Benchmark (Linux NVMe) |
| :--- | :--- | :--- |
| **Scan Throughput** | $> 15,000\text{ files/sec}$ | **$27,230.5\text{ files/sec}$** ($109,041$ files in $4.004\text{s}$) |
| **Pass 1 Duplicate Filter** | $< 500\text{ms}$ for $100\text{k}$ files | **$42\text{ms}$** |
| **SHA-256 Buffer Memory** | Constant RAM footprint | **$64\text{ KB}$ per worker** ($< 2\text{ MB}$ total) |
| **Database Contention** | $0$ lock errors | **$0$ locked errors** under $24$ worker goroutines |
| **Binary Size & Memory** | $< 50\text{ MB}$ RAM | **$8.3\text{ MB}$ binary**, **$\approx 32\text{ MB}$ RAM** |

---

## 7. Technical Documentation Suite

For comprehensive deep-dives, consult the [`docs/`](docs/README.md) directory:
- [01. Architecture & Design](docs/01-architecture-and-design.md)
- [02. Systems Programming & Linux Internals](docs/02-systems-programming-and-linux.md)
- [03. Concurrency & Data Flow](docs/03-concurrency-and-data-flow.md)
- [04. Database & Schema Contract](docs/04-database-and-schema.md)
- [05. Local HTTP API & Integration Contract](docs/05-api-and-python-gui-contract.md)
- [06. Operations & CLI Reference](docs/06-operations-and-cli.md)
- [07. Go Core Modules Guide](docs/07-go-core-modules-guide.md)
