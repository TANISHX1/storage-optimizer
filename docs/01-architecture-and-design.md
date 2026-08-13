# 01 — System Architecture & Design

## 1. System Vision & Problem Statement

Modern developer environments, build servers, and desktop workstations suffer from unmonitored disk bloat. Build artifacts (`node_modules`, `target/`, `bin/`), duplicated video/asset files, and obsolete cache archives consume gigabytes of storage silently.

The **Intelligent Storage Optimizer** is a high-performance, concurrency-safe systems tool built in Go for Linux that:
1. Concurrently indexes filesystem hierarchies without kernel resource exhaustion.
2. Identifies exact duplicates using an I/O-optimized two-pass algorithm (size buckets $\rightarrow$ streaming SHA-256).
3. Computes intelligent staleness scores based on access/modification dynamics and path heuristics.
4. Provides time-series disk snapshots for Python regression and growth forecasting.
5. Executes human-approved cleanup actions (`trash` or `permanent`) with pre-execution safety validation and audit logging.

---

## 2. Component Topology

```
storage-optimizer/
├── go-core/              # Systems Core (Go 1.25+)
│   ├── cmd/storage-optimizer/ # CLI entrypoint & server orchestrator
│   ├── internal/
│   │   ├── models/       # Domain data transfer objects & entity structs
│   │   ├── db/           # SQLite connection, WAL management, Single-Writer funnel
│   │   ├── scanner/      # Concurrent directory walker, VFS stat extractor
│   │   ├── dedup/        # Two-pass duplicate detector
│   │   ├── stale/        # Staleness scoring engine
│   │   ├── action/       # Audit-logged cleanup action executor
│   │   └── api/          # Local HTTP REST API server
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

### 3.2 Canonical Schema as Contract
- `shared/schema.sql` is the canonical schema source of truth.
- Both Go and Python share this contract without schema drift.
- Go automatically applies this schema during initialization with idempotency (`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`).

### 3.3 Two-Tier Action Safety Model
- **No Automatic Deletion**: Neither duplicate detection nor staleness analysis ever deletes files automatically.
- **Explicit Confirmation**: Every destructive request must declare a mode (`trash` or `permanent`).
- **Audit Logging**: An entry in `actions_log` is written *before* any filesystem mutation.
- **Freshness Validation**: The file path, inode, and existence are verified immediately prior to execution to prevent acting on stale indices.
