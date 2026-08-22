# 04 — Database & Schema Specification

This document details the SQLite database architecture, connection pragmas, and the table schemas stored in `shared/schema.sql`.

---

## 1. SQLite Engine Configuration

To achieve high concurrency and maximum throughput, the database connection is initialized with the following PRAGMAs:

```sql
PRAGMA journal_mode = WAL;         -- Write-Ahead Logging for concurrent readers
PRAGMA synchronous = NORMAL;       -- Safe on Linux ext4/xfs with power-safe sync
PRAGMA foreign_keys = ON;          -- Strict referential integrity
PRAGMA busy_timeout = 5000;        -- 5s wait before throwing 'database is locked'
PRAGMA temp_store = MEMORY;        -- Store temporary tables and indices in RAM
PRAGMA cache_size = -64000;        -- 64MB memory cache for index queries
```

---

## 2. Table Specifications

### 2.1 `files` Table
The primary metadata index for all scanned filesystem objects.

| Column | Type | Constraints | Description |
| :--- | :--- | :--- | :--- |
| `id` | `INTEGER` | `PRIMARY KEY AUTOINCREMENT` | Unique identifier for internal references |
| `path` | `TEXT` | `NOT NULL UNIQUE` | Absolute canonical file path |
| `size` | `INTEGER` | `NOT NULL` | File size in bytes |
| `mtime` | `INTEGER` | `NOT NULL` | Last modified timestamp (Unix seconds) |
| `atime` | `INTEGER` | `NOT NULL` | Last accessed timestamp (Unix seconds) |
| `inode` | `INTEGER` | `NOT NULL` | POSIX Inode number (for hardlink deduplication) |
| `extension` | `TEXT` | `NOT NULL` | Lowercase file extension (e.g., `.log`, `.tmp`) |
| `category` | `TEXT` | `NOT NULL DEFAULT 'user'` | Classification: `system_protected`, `system_log`, `crash_dump`, `temp`, `system_cache`, `user` |
| `is_system` | `INTEGER` | `NOT NULL DEFAULT 0` | Boolean flag (`1` for OS/root directories, `0` for user files) |
| `content_hash` | `TEXT` | `NULLABLE` | Hex SHA-256 digest (computed during Pass 2) |
| `staleness_score` | `REAL` | `NOT NULL DEFAULT 0.0` | Inactivity score $[0.0, 1.0]$ |
| `last_scanned_at` | `INTEGER` | `NOT NULL` | Timestamp of the most recent scan |

### 2.2 `scan_snapshots` Table
Time-series growth metrics used by the Python regression and forecasting layer.

| Column | Type | Constraints | Description |
| :--- | :--- | :--- | :--- |
| `id` | `INTEGER` | `PRIMARY KEY AUTOINCREMENT` | Snapshot sequence number |
| `scanned_at` | `INTEGER` | `NOT NULL` | Timestamp of scan completion |
| `total_files` | `INTEGER` | `NOT NULL` | Total count of indexed files |
| `total_bytes` | `INTEGER` | `NOT NULL` | Total cumulative size on disk |
| `root_path` | `TEXT` | `NOT NULL` | Target root path scanned |

### 2.3 `actions_log` Table
Immutable audit trail for file cleanup actions (FreeDesktop.org XDG Trash & Permanent Delete).

| Column | Type | Constraints | Description |
| :--- | :--- | :--- | :--- |
| `id` | `INTEGER` | `PRIMARY KEY AUTOINCREMENT` | Action audit record ID |
| `file_path` | `TEXT` | `NOT NULL` | Path of file at action execution |
| `action_mode` | `TEXT` | `NOT NULL` | Action mode: `'trash'` or `'permanent'` |
| `trashed_to_path` | `TEXT` | `NULLABLE` | Destination path in `~/.local/share/Trash/files/` (NULL for permanent) |
| `file_size` | `INTEGER` | `NOT NULL` | Size in bytes of freed storage |
| `performed_at` | `INTEGER` | `NOT NULL` | Timestamp of action execution (Unix seconds) |

---

## 3. Performance Indexes & Aggregation Queries

### 3.1 Composite Indexes
To ensure sub-millisecond query execution across large datasets (~200k+ records), composite indexes accelerate duplicate and stale file breakdown analytics:

```sql
-- Composite index for duplicate file extension breakdown
CREATE INDEX IF NOT EXISTS idx_files_dup_ext ON files(duplicate_group_id, extension);

-- Composite index for stale file extension breakdown
CREATE INDEX IF NOT EXISTS idx_files_stale_score_ext ON files(staleness_score, extension);
```

### 3.2 SQL Breakdown Aggregations

#### Duplicate Extension Breakdown
```sql
SELECT 
    CASE WHEN extension = '' THEN 'no extension' ELSE extension END AS ext,
    COUNT(*) AS count,
    COALESCE(SUM(size), 0) AS total_bytes
FROM files
WHERE duplicate_group_id IS NOT NULL AND duplicate_group_id != ''
GROUP BY ext
ORDER BY total_bytes DESC
LIMIT 10;
```

#### Stale Extension Breakdown
```sql
SELECT 
    CASE WHEN extension = '' THEN 'no extension' ELSE extension END AS ext,
    COUNT(*) AS count,
    COALESCE(SUM(size), 0) AS total_bytes
FROM files
WHERE staleness_score >= 0.01
GROUP BY ext
ORDER BY total_bytes DESC
LIMIT 10;
```


