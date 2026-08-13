# 04 — Database & Schema Contract

This document specifies the SQLite database configuration, PRAGMA tuning, and the canonical database schema shared across Go and Python.

---

## 1. SQLite PRAGMA Configuration

Upon connection initialization, the Go database driver configures the SQLite database engine with high-performance PRAGMAs:

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA cache_size = -64000;
PRAGMA foreign_keys = ON;
PRAGMA temp_store = MEMORY;
```

### Rationale:
- **`journal_mode = WAL`**: Write-Ahead Logging replaces the traditional rollback journal. Readers do not block writers, and writers do not block readers. Reads and writes can proceed concurrently.
- **`synchronous = NORMAL`**: In WAL mode, `NORMAL` syncs disk writes at WAL checkpoint intervals rather than on every single commit, yielding massive throughput gains without sacrificing database integrity.
- **`busy_timeout = 5000`**: Automatically waits up to 5,000 milliseconds when encountering locked resources before throwing an error.
- **`cache_size = -64000`**: Allocates 64 megabytes of in-process RAM cache for frequently accessed B-Tree pages.

---

## 2. Canonical Schema Specification (`shared/schema.sql`)

### Table: `files`
Stores file metadata, computed content hashes, and staleness scores.

```sql
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    size INTEGER NOT NULL,
    mtime INTEGER NOT NULL,
    atime INTEGER,
    inode INTEGER,
    extension TEXT,
    content_hash TEXT,
    staleness_score REAL,
    last_scanned_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(content_hash);
CREATE INDEX IF NOT EXISTS idx_files_mtime ON files(mtime);
CREATE INDEX IF NOT EXISTS idx_files_staleness ON files(staleness_score);
```

| Column | Type | Description |
| :--- | :--- | :--- |
| `id` | `INTEGER PK` | Unique row identifier |
| `path` | `TEXT UNIQUE` | Canonical absolute file path |
| `size` | `INTEGER` | File size in bytes |
| `mtime` | `INTEGER` | Unix timestamp of last modification |
| `atime` | `INTEGER` | Unix timestamp of last access |
| `inode` | `INTEGER` | Linux filesystem Inode number |
| `extension` | `TEXT` | File extension (e.g. `.log`, `.mp4`) |
| `content_hash` | `TEXT` | SHA-256 hex digest (populated during Phase 2) |
| `staleness_score` | `REAL` | Staleness metric from $0.0$ to $1.0$ (populated in Phase 3) |
| `last_scanned_at` | `INTEGER` | Unix timestamp of most recent scan |

---

### Table: `scan_snapshots`
Stores historical snapshots of disk usage for Python time-series regression.

```sql
CREATE TABLE IF NOT EXISTS scan_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scanned_at INTEGER NOT NULL,
    root_path TEXT NOT NULL,
    total_files INTEGER,
    total_bytes INTEGER
);

CREATE INDEX IF NOT EXISTS idx_snapshots_scanned_at ON scan_snapshots(scanned_at);
```

---

### Table: `actions_log`
Provides an immutable audit log of all human-confirmed cleanup actions.

```sql
CREATE TABLE IF NOT EXISTS actions_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL,
    action_mode TEXT NOT NULL,       -- 'trash' or 'permanent'
    trashed_to_path TEXT,             -- NULL for permanent deletes
    file_size INTEGER,
    performed_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_actions_performed_at ON actions_log(performed_at);
```
