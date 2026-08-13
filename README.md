# Intelligent Storage Optimizer (Linux)

A high-performance desktop tool for Linux that scans filesystem trees, indexes metadata locally with SQLite, identifies duplicate and stale files, forecasts disk usage growth, and executes human-confirmed cleanup actions safely.

---

## System Architecture

```
storage-optimizer/
├── go-core/              # Systems-level core: concurrent scanner, dedup, staleness, action layer, local HTTP API
│   ├── cmd/storage-optimizer/ # CLI & Server entrypoint
│   ├── internal/
│   │   ├── db/           # SQLite indexer & migrations (single-writer funnel)
│   │   ├── scanner/      # Concurrent directory walker & metadata extractor
│   │   ├── dedup/        # Two-pass duplicate detector (size-bucket -> SHA256 streaming hash)
│   │   ├── stale/        # Staleness scoring engine with path/extension weighting
│   │   ├── action/       # User-confirmed action layer (trash with audit trail & permanent deletion)
│   │   └── api/          # Local HTTP REST API (net/http)
│   └── go.mod
├── gui/                  # Desktop GUI shell (Wails + Web Frontend)
│   ├── frontend/         # React/HTML/JS UI components and Sahil's data-driven visualizations
│   └── wails.json
├── python-layer/         # Forecast & recommendation engine (Sahil - Day 7)
│   ├── forecast/         # Time-series regression from scan_snapshots
│   └── recommend/        # Plain-language cleanup recommendation generation
├── shared/
│   └── schema.sql        # Canonical SQLite schema (Single Source of Truth)
├── data/
│   └── optimizer.db      # Runtime SQLite database
└── README.md
```

---

## Key Architectural Principles

1. **Go Owns All SQLite Writes**:
   - SQLite concurrent writes easily hit lock contention (`database is locked`).
   - The Go core is the **sole writer** to `data/optimizer.db` via a funnel-to-single-writer pattern.
2. **Local HTTP API as the Common Bridge**:
   - Both the **GUI frontend** and Sahil's **Python Layer** communicate via local HTTP REST endpoints exposed by Go.
   - Python consumes `/snapshots`, `/files/duplicates`, and `/files/stale` as a regular HTTP client (`httpx` or `requests`), avoiding direct SQLite file locking conflicts.
3. **Safety First in Actions**:
   - No automatic deletions.
   - Every cleanup requires explicit confirmation (`trash` or `permanent`).
   - Every action is logged to `actions_log` before execution.
   - Path and existence sanity checks are verified immediately prior to executing any filesystem change.
4. **Incremental Re-Scans**:
   - Compares file modification times (`mtime`) against indexed values to skip redundant I/O and hashing.

---

## Local HTTP API Specification

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `POST` | `/api/v1/scan` | Trigger filesystem scan (`{"path": "/path/to/scan", "full": false}`) |
| `GET` | `/api/v1/scan/status` | Current scan progress / status |
| `GET` | `/api/v1/files/duplicates` | Duplicate groups grouped by SHA-256 hash |
| `GET` | `/api/v1/files/stale?days=N` | Files untouched for $N+$ days with calculated staleness score |
| `GET` | `/api/v1/snapshots` | Historical `scan_snapshots` time-series for Python forecasting |
| `POST` | `/api/v1/actions` | Execute confirmed cleanup (`{"ids": [1, 2], "mode": "trash" \| "permanent"}`) |
| `POST` | `/api/v1/actions/restore` | Restore previously trashed file (`{"action_id": 10}`) |
| `GET` | `/api/v1/actions/history` | Audit log of past actions |

---

## Handoff Notes for Sahil (Python Layer — Day 7)

- See [`python-layer/README.md`](file:///home/blazex/Documents/git/storage-optimizer/python-layer/README.md) for how to consume the HTTP API and build forecasting/recommendations.
- The schema in [`shared/schema.sql`](file:///home/blazex/Documents/git/storage-optimizer/shared/schema.sql) is the single source of truth.
