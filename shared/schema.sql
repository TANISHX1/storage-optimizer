-- Canonical SQLite Schema for Intelligent Storage Optimizer
-- Single source of truth across Go Core, Python Analytics/Forecasting, and GUI Shell.

CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    size INTEGER NOT NULL,
    mtime INTEGER NOT NULL,
    atime INTEGER,
    inode INTEGER,
    extension TEXT,
    content_hash TEXT,
    duplicate_group_id TEXT,             -- Identifies confirmed duplicate cluster (Fix 4)
    parent_path TEXT,                    -- Immediate parent directory for fast tree browsing (Fix 6)
    staleness_score REAL,
    is_system INTEGER DEFAULT 0,          -- 1 if file resides in OS system paths, 0 for user files
    category TEXT DEFAULT 'user',         -- 'user', 'system_protected', 'system_log', 'system_cache', 'crash_dump', 'temp'
    last_scanned_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(content_hash);
CREATE INDEX IF NOT EXISTS idx_files_dup_group ON files(duplicate_group_id);
CREATE INDEX IF NOT EXISTS idx_files_parent ON files(parent_path);
CREATE INDEX IF NOT EXISTS idx_files_mtime ON files(mtime);
CREATE INDEX IF NOT EXISTS idx_files_staleness ON files(staleness_score);
CREATE INDEX IF NOT EXISTS idx_files_category ON files(category);

CREATE TABLE IF NOT EXISTS scan_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scanned_at INTEGER NOT NULL,
    root_path TEXT NOT NULL,
    total_files INTEGER,
    total_bytes INTEGER
);

CREATE INDEX IF NOT EXISTS idx_snapshots_scanned_at ON scan_snapshots(scanned_at);
CREATE INDEX IF NOT EXISTS idx_snapshots_root ON scan_snapshots(root_path);

CREATE TABLE IF NOT EXISTS actions_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL,
    action_mode TEXT NOT NULL,       -- 'trash' or 'permanent'
    trashed_to_path TEXT,             -- NULL for permanent deletes
    file_size INTEGER,
    performed_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_actions_performed_at ON actions_log(performed_at);
