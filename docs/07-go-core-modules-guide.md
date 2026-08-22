# 07 — Go Core Modules Guide & Systems Engineering Reference

This document is an exhaustive, module-by-module technical reference and learning guide for the **Go Systems Core** (`go-core/`) of the Intelligent Storage Optimizer. 

It is designed for systems engineers, developers, and learners who want to understand every algorithm, POSIX syscall, concurrency pattern, data structure, and safety mechanism in the project.

---

## Table of Contents

1. [Architectural Overview & Mental Model](#1-architectural-overview--mental-model)
2. [Domain Entities & Data Contracts (`internal/models`)](#2-domain-entities--data-contracts-internalmodels)
3. [Concurrent Directory Walker & Syscall Engine (`internal/scanner`)](#3-concurrent-directory-walker--syscall-engine-internalscanner)
4. [Database Engine & Single-Writer Channel Funnel (`internal/db`)](#4-database-engine--single-writer-channel-funnel-internaldb)
5. [Two-Pass Deduplication Engine (`internal/dedup`)](#5-two-pass-deduplication-engine-internaldedup)
6. [Mathematical Staleness Scoring Engine (`internal/stale`)](#6-mathematical-staleness-scoring-engine-internalstale)
7. [Action, Safety & FreeDesktop XDG Trash Engine (`internal/action`)](#7-action-safety--freedesktop-xdg-trash-engine-internalaction)
8. [Local HTTP REST API & Static Server (`internal/api`)](#8-local-http-rest-api--static-server-internalapi)
9. [CLI Entrypoint & Process Orchestration (`cmd/storage-optimizer`)](#9-cli-entrypoint--process-orchestration-cmdstorage-optimizer)
10. [End-to-End Walkthrough: Life of a File](#10-end-to-end-walkthrough-life-of-a-file)
11. [Systems Engineering Lessons & Linux Gotchas](#11-systems-engineering-lessons--linux-gotchas)

---

## 1. Architectural Overview & Mental Model

The Go Systems Core is designed around four fundamental principles:

1. **Kernel Resource Safety**: Traverses deep directory hierarchies concurrently without overflowing kernel file descriptors (`EMFILE`) or following infinite cyclic symbolic links.
2. **Zero-Contention Single-Writer Funnel**: Channels all database writes from dozens of concurrent worker goroutines into a single, high-throughput batch writer goroutine, eliminating SQLite `database is locked` errors.
3. **I/O-Optimized Two-Pass Deduplication**: Filters 80–90% of files using fast metadata size buckets before computing streaming cryptographic hashes in flat 64 KB memory buffers.
4. **Human-Confirmed, Non-Destructive Action Layer**: Hard-blocks critical OS directories (`/etc`, `/usr`, etc.), enforces pre-action Inode/Size sanity checks to prevent TOCTOU races, and implements the standard FreeDesktop.org XDG Trash specification.

```
                               ┌───────────────────────────────┐
                               │     CLI & Entrypoint Layer    │
                               │  (cmd/storage-optimizer/main) │
                               └───────────────┬───────────────┘
                                               │
                      ┌────────────────────────┼────────────────────────┐
                      ▼                        ▼                        ▼
       ┌────────────────────────┐┌────────────────────────┐┌────────────────────────┐
       │  Scanner & Walker      ││  Duplicate Engine      ││  Staleness Engine      │
       │  (internal/scanner)    ││  (internal/dedup)      ││  (internal/stale)      │
       │  • os.Lstat / syscall  ││  • Pass 1: Size Filter ││  • Exponential Decay   │
       │  • Worker Pool Queue   ││  • Pass 2: SHA-256     ││  • Path Weight Matrix  │
       │  • Category Classifier ││  • 64 KB Chunk Buffer  ││  • Score Saturation    │
       └───────────┬────────────┘└───────────┬────────────┘└───────────┬────────────┘
                   │                         │                         │
                   ▼                         ▼                         ▼
       ┌────────────────────────────────────────────────────────────────────────────┐
       │                        Models & Domain Entities                            │
       │                           (internal/models)                                │
       │   FileMetadata, DuplicateGroup, StaleFile, ActionLog, ScanSnapshot, etc.   │
       └───────────────────────────────────┬────────────────────────────────────────┘
                                           │
                                           ▼
       ┌────────────────────────────────────────────────────────────────────────────┐
       │                   Database Layer & Single-Writer Funnel                    │
       │                              (internal/db)                                 │
       │  • SQLite WAL Mode & PRAGMA Tuning                                         │
       │  • Channel Funnel (chan FileMetadata) -> BatchWriter Goroutine             │
       │  • 500-Item / 50ms Flush Window with BEGIN IMMEDIATE TRANSACTION           │
       │  • Optimized Table Indices & Read Query Engine                             │
       └───────────────────┬───────────────────────────────────┬────────────────────┘
                           │                                   │
                           ▼                                   ▼
       ┌──────────────────────────────────────┐┌────────────────────────────────────┐
       │        Action & Safety Engine        ││        Local HTTP REST API         │
       │          (internal/action)           ││           (internal/api)           │
       │  • Pre-action Inode/Size Sanity Gate ││  • REST Endpoints & JSON Payloads  │
       │  • Linux Protected Path Blocklist    ││  • Live Scan Progress Feed         │
       │  • FreeDesktop XDG Trash (.trashinfo)││  • Embedded Frontend Static Server │
       │  • Immutable Action Audit Logger     ││  • CORS & Middleware Pipeline      │
       └──────────────────────────────────────┘└────────────────────────────────────┘
```

---

## 2. Domain Entities & Data Contracts (`internal/models`)

The `models` package contains the shared data structures and constants representing all domain concepts.

### 2.1 Struct Definitions

#### `FileMetadata`
The primary representation of an indexed file or directory:

```go
type FileMetadata struct {
    ID             int64        `json:"id"`
    Path           string       `json:"path"`            // Canonical absolute filesystem path
    ParentDir      string       `json:"parent_dir"`      // Immediate parent directory path
    Filename       string       `json:"filename"`        // Base name of the file
    Extension      string       `json:"extension"`       // Lowercase extension (e.g. ".pdf", ".tar.gz")
    Size           int64        `json:"size"`            // Size in bytes
    Mode           uint32       `json:"mode"`            // POSIX file permission bits
    ModTime        int64        `json:"mod_time"`        // Unix epoch timestamp (seconds)
    AccessTime     int64        `json:"access_time"`     // Unix epoch timestamp (seconds)
    ChangeTime     int64        `json:"change_time"`     // Unix epoch timestamp (seconds)
    Inode          uint64       `json:"inode"`           // Linux filesystem Inode number
    DeviceID       uint64       `json:"device_id"`       // St_dev filesystem device ID
    Category       FileCategory `json:"category"`        // Classified category enum
    SHA256Hash     string       `json:"sha256_hash"`     // Hex-encoded SHA-256 hash (populated in Pass 2)
    StalenessScore float64      `json:"staleness_score"` // Inactivity score [0.00, 1.00]
    IsDeleted      bool         `json:"is_deleted"`      // Soft-deletion flag for incremental sync
    CreatedAt      int64        `json:"created_at"`
    UpdatedAt      int64        `json:"updated_at"`
}
```

#### `DuplicateGroup` & `DuplicateFile`
Clusters files sharing identical content:

```go
type DuplicateGroup struct {
    SHA256Hash  string          `json:"sha256_hash"`
    FileSize    int64           `json:"file_size"`
    TotalCount  int             `json:"total_count"`
    WastedBytes int64           `json:"wasted_bytes"` // FileSize * (TotalCount - 1)
    Files       []DuplicateFile `json:"files"`
}

type DuplicateFile struct {
    ID         int64  `json:"id"`
    Path       string `json:"path"`
    ModTime    int64  `json:"mod_time"`
    AccessTime int64  `json:"access_time"`
    IsOriginal bool   `json:"is_original"` // True for oldest file (recommended to keep)
}
```

#### `ActionLog`
Maintains an immutable record of every cleanup, deletion, and restoration event:

```go
type ActionLog struct {
    ID           int64      `json:"id"`
    FileID       int64      `json:"file_id"`
    OriginalPath string     `json:"original_path"`
    TrashedPath  string     `json:"trashed_path"` // Path inside ~/.local/share/Trash/files/
    Inode        uint64     `json:"inode"`
    FileSize     int64      `json:"file_size"`
    ActionType   ActionType `json:"action_type"` // "trash", "permanent_delete", "restore"
    Status       string     `json:"status"`      // "success", "failed", "restored"
    ErrorMessage string     `json:"error_message"`
    Timestamp    int64      `json:"timestamp"`
}
```

#### `ScanSnapshot`
Point-in-time metrics captured after each scan:

```go
type ScanSnapshot struct {
    ID              int64  `json:"id"`
    ScannedAt       int64  `json:"scanned_at"`
    RootPath        string `json:"root_path"`
    TotalFiles      int64  `json:"total_files"`
    TotalBytes      int64  `json:"total_bytes"`
    TotalDuplicates int64  `json:"total_duplicates"`
    WastedBytes     int64  `json:"wasted_bytes"`
    StaleFilesCount int64  `json:"stale_files_count"`
    StaleBytes      int64  `json:"stale_bytes"`
    ScanDurationMs  int64  `json:"scan_duration_ms"`
}
```

### 2.2 Category Classification System

Files are classified using strict path and extension heuristics:

| Category Enum | String Key | Criteria & Examples | Safety & Action Level |
| :--- | :--- | :--- | :--- |
| `CategorySystemProtected` | `system_protected` | `/etc`, `/usr/bin`, `/lib`, `/boot`, `/sys`, `/proc` | **Locked**. Staleness = $0.00$. Deletions blocked. |
| `CategorySystemLog` | `system_log` | `/var/log`, `.log`, `.journal` | Reclaimable. High staleness weighting. |
| `CategoryCrashDump` | `crash_dump` | `/var/crash`, `.core`, `.dmp` | **High Priority Reclamation**. Safe to purge. |
| `CategoryTempFile` | `temp` | `/tmp`, `/var/tmp`, `.tmp`, `.swp`, `~*` | **High Priority Reclamation**. Safe to purge. |
| `CategorySystemCache` | `system_cache` | `~/.cache`, `/var/cache`, `.cache` | Reclaimable application caches. |
| `CategoryUserDocument` | `user_document` | `.pdf`, `.docx`, `.xlsx`, `.md`, `.txt` | Protected user assets. Conservative scoring. |
| `CategoryUserMedia` | `user_media` | `.mp4`, `.mkv`, `.jpg`, `.png`, `.flac` | User media assets. |
| `CategoryUserCode` | `user_code` | `.go`, `.py`, `.ts`, `.rs`, `.cpp`, `.c` | User source code. Protected scoring. |
| `CategoryUserArchive` | `user_archive` | `.tar.gz`, `.zip`, `.7z`, `.iso` | Archive packages. |
| `CategoryUserGeneral` | `user_general` | All other non-system user files | Default user files. |

---

## 3. Concurrent Directory Walker & Syscall Engine (`internal/scanner`)

The `scanner` package traverses directory trees at maximum I/O throughput while remaining safe from file descriptor leaks and symlink loops.

### 3.1 Directory Walker Lifecycle Flowchart

```
                          [ Start Scan(rootPath) ]
                                     │
                    ┌────────────────┴────────────────┐
                    ▼                                 ▼
      [ Discover Root Subdirs ]          [ Spawn N Worker Goroutines ]
                    │                                 │
                    ▼                                 ▼
        ┌───────────────────────┐         ┌───────────────────────┐
        │  Work Channel (Queue) │◄───────►│  Worker Dequeues Dir  │
        │     chan string       │         │    os.ReadDir()       │
        └───────────────────────┘         └───────────┬───────────┘
                                                      │
                    ┌─────────────────────────────────┴──────────────────┐
                    ▼                                                    ▼
            [ Child is a Directory ]                             [ Child is a File ]
                    │                                                    │
            [ Push to Work Queue ]                              [ os.Lstat(fullPath) ]
                                                                         │
                                                                         ▼
                                                            [ Extract *syscall.Stat_t ]
                                                            • Inode = stat.Ino
                                                            • DevID = stat.Dev
                                                            • atime = stat.Atim.Sec
                                                            • mtime = stat.Mtim.Sec
                                                                         │
                                                                         ▼
                                                            [ Classify Category & Ext ]
                                                                         │
                                                                         ▼
                                                            [ Send to Funnel Channel ]
                                                            (chan models.FileMetadata)
```

### 3.2 Key Technical Mechanisms

#### 1. Preventing Cyclic Symlinks with `os.Lstat`
Using `os.Stat()` follows symbolic links to their targets. If a symlink points to a parent directory (e.g. `symlink -> ..`), recursive traversal enters an infinite loop, causing memory exhaustion or stack overflow.

**Implementation**: The scanner exclusively calls `os.Lstat()`, which inspects the symbolic link itself rather than the target:
```go
info, err := os.Lstat(filePath)
if err != nil {
    return // Silently bypass unreadable or permission-restricted files
}
```

#### 2. Direct Linux Inode & Timestamp Extraction
Standard Go `os.FileInfo` only exposes `ModTime()`. To obtain POSIX Inodes and precise access timestamps (`atime`), the scanner casts the underlying system info to Linux `syscall.Stat_t`:

```go
stat, ok := info.Sys().(*syscall.Stat_t)
if ok {
    meta.Inode = stat.Ino
    meta.DeviceID = stat.Dev
    meta.AccessTime = stat.Atim.Sec // Last accessed timestamp
    meta.ChangeTime = stat.Ctim.Sec // Metadata change timestamp
    meta.ModTime = stat.Mtim.Sec    // File content modification timestamp
}
```

#### 3. Incremental Delta Scanning & Soft Pruning
When scanning a path that has been indexed previously:
1. The scanner fetches existing file metadata records for the path into an in-memory map keyed by `path`.
2. As each file is traversed on disk, its current `mtime` and `size` are compared with the database record:
   - If identical, database writes and SHA-256 hash recalculations are skipped.
   - If modified, the existing `sha256_hash` is cleared (`""`), flagging the file for re-hashing during deduplication.
3. After directory traversal completes, any file present in SQLite for that tree but not encountered on disk is updated with `is_deleted = 1`.

---

## 4. Database Engine & Single-Writer Channel Funnel (`internal/db`)

The `db` package provides SQLite connection management, performance tuning, and the core single-writer channel funnel.

### 4.1 The Single-Writer Architecture

SQLite allows multiple concurrent readers in WAL mode, but only **one write transaction** can hold the database lock at any instant. When multiple goroutines attempt simultaneous writes, SQLite throws `sqlite3: database is locked (5)`.

To solve this, the Go core uses the **Funnel-to-Single-Writer Pattern**:

```
[ Worker Goroutine 1 ] ──┐
[ Worker Goroutine 2 ] ──┼──► [ Channel: chan FileMetadata ] ──► [ DB BatchWriter Goroutine ] ──► SQLite (WAL)
[ Worker Goroutine N ] ──┘          (Buffer: 5,000 items)               (Single Writer)
```

### 4.2 SQLite PRAGMA Configuration
Upon database initialization, the engine applies performance PRAGMAs:

```sql
PRAGMA journal_mode = WAL;          -- Enables concurrent reads during active write transactions
PRAGMA synchronous = NORMAL;        -- Minimizes disk fsync() overhead while maintaining WAL integrity
PRAGMA cache_size = -64000;         -- Allocates 64 MB of RAM for database page cache
PRAGMA temp_store = MEMORY;         -- Keeps temporary tables and indices in RAM
PRAGMA foreign_keys = ON;           -- Enforces referential integrity
PRAGMA busy_timeout = 5000;         -- Waits up to 5000ms before returning SQLITE_BUSY
```

### 4.3 BatchWriter Implementation
The `BatchWriter` goroutine runs continuously in the background:

```go
func (d *DB) startBatchWriter() {
    batch := make([]models.FileMetadata, 0, 500)
    ticker := time.NewTicker(50 * time.Millisecond)
    defer ticker.Stop()

    flush := func() {
        if len(batch) == 0 {
            return
        }
        d.insertOrUpdateBatch(batch)
        batch = batch[:0]
    }

    for {
        select {
        case item, ok := <-d.writeChan:
            if !ok {
                flush() // Drain remaining items on channel close
                return
            }
            batch = append(batch, item)
            if len(batch) >= 500 {
                flush() // Flush when capacity threshold reached
            }
        case <-ticker.C:
            flush() // Flush when time threshold reached
        }
    }
}
```

---

## 5. Two-Pass Deduplication Engine (`internal/dedup`)

Deduplication computes cryptographic hashes only when strictly necessary, minimizing disk I/O.

### 5.1 Two-Pass Algorithm Flowchart

```
                      [ Start Deduplication ]
                                 │
                                 ▼
                     [ Pass 1: Size Filtering ]
                     SELECT size, COUNT(*) FROM files 
                     WHERE is_deleted = 0 AND size > 0 
                     GROUP BY size HAVING COUNT(*) > 1;
                                 │
                 ┌───────────────┴───────────────┐
                 ▼                               ▼
       [ Unique Size Files ]           [ Size Collision Groups ]
         (80-90% of files)             (Candidate Duplicate Files)
                 │                               │
                 ▼                               ▼
        [ Skip Hashing ]               [ Pass 2: Streaming Hash ]
        (Zero I/O Cost)                Read in 64 KB Chunk Buffers
                                       io.CopyBuffer(hasher, file, buf)
                                                 │
                                                 ▼
                                       [ Update SHA-256 in DB ]
                                                 │
                                                 ▼
                                       [ Cluster Duplicates ]
                                       • Elect Oldest as Original
                                       • Calculate Wasted Bytes
```

### 5.2 Streaming 64 KB Chunk Buffer Hashing
To prevent memory bloat when hashing multi-gigabyte files (such as ISOs, virtual disks, or raw video footage), hashing uses a fixed 64 KB chunk buffer:

```go
func hashFileStreaming(path string) (string, error) {
    f, err := os.Open(path)
    if err != nil {
        return "", err
    }
    defer f.Close()

    hasher := sha256.New()
    buf := make([]byte, 64*1024) // 64 KB stack buffer

    if _, err := io.CopyBuffer(hasher, f, buf); err != nil {
        return "", err
    }

    return hex.EncodeToString(hasher.Sum(nil)), nil
}
```

---

## 6. Mathematical Staleness Scoring Engine (`internal/stale`)

The `stale` package assigns an objective **Staleness Score** ($0.00$ to $1.00$) to every indexed file.

### 6.1 Mathematical Formulation

$$\text{StalenessScore} = \text{Clamp}\Big(\Big[1 - e^{-\lambda \cdot t_{\text{inactive}}}\Big] \times W_{\text{category}} \times W_{\text{path}} \times W_{\text{size}}, \; 0.0, \; 1.0\Big)$$

Where:
- **$t_{\text{inactive}}$**: Inactivity duration in days:
  $$t_{\text{inactive}} = \frac{\text{Now} - \max(\text{atime}, \text{mtime})}{86400}$$
- **$\lambda = 0.015$**: Exponential decay constant.
  - 30 days $\rightarrow 1 - e^{-0.45} \approx 0.36$
  - 60 days $\rightarrow 1 - e^{-0.90} \approx 0.59$
  - 180 days $\rightarrow 1 - e^{-2.70} \approx 0.93$
  - 365 days $\rightarrow 1 - e^{-5.47} \approx 0.99$

### 6.2 Weighting Multipliers

```
Category Weights (W_category):
├── CategoryCrashDump:       1.50 (Highest priority junk)
├── CategoryTempFile:        1.40 (Temporary scratch files)
├── CategorySystemCache:     1.25 (Caches)
├── CategorySystemLog:       1.15 (Logs)
├── CategoryUserGeneral:     1.00 (Baseline)
├── CategoryUserDocument:    0.85 (Conservative protection)
├── CategoryUserCode:        0.85 (Protected developer files)
└── CategorySystemProtected: 0.00 (Hard locked)

Path Multipliers (W_path):
├── Paths containing "/tmp" or "/var/tmp":   1.40
├── Paths containing "node_modules":         1.20
├── Paths containing "Downloads":            1.10
└── Paths containing ".git" or "src":        0.80

Size Booster (W_size):
└── For files > 100 MB: 1.0 + 0.1 * log10(SizeMB) (Favoring large space wins)
```

---

## 7. Action, Safety & FreeDesktop XDG Trash Engine (`internal/action`)

The `action` package handles file deletions, trashing, and restorations with mandatory safety checks and an immutable audit trail.

### 7.1 Pre-Action Safety Verification

Before any file is touched on disk, three validation gates must pass:

1. **System Directory Blocklist**: Target path is compared against:
   ```
   /etc, /usr, /boot, /lib, /lib64, /sys, /proc, /dev, /sbin, /bin, /root
   ```
   If the target path starts with any of these roots, the operation is immediately rejected.
2. **Category Lock**: Files with `CategorySystemProtected` are rejected.
3. **TOCTOU Race Prevention**: The file is stat-checked on disk immediately before execution. Its current Inode and byte size must match the database record. If the file was swapped or modified, execution halts.

### 7.2 FreeDesktop.org XDG Trash Standard

In `trash` mode, the action engine adheres to the standard Linux desktop trash specification:
- **Payload File**: Moved to `~/.local/share/Trash/files/<filename>`.
- **Metadata File**: Created at `~/.local/share/Trash/info/<filename>.trashinfo` containing:
  ```ini
  [Trash Info]
  Path=/home/user/Documents/old_backup.tar.gz
  DeletionDate=2026-08-19T23:15:00
  ```
- **Native OS Compatibility**: Trashed files immediately appear in GNOME Files (Nautilus), KDE Dolphin, and XFCE Thunar.

### 7.3 Restoration Workflow

```
[ Restore Request (ActionID) ]
              │
              ▼
[ Query actions_log in DB ]
Extract OriginalPath & TrashedPath
              │
              ▼
[ Verify Trashed File Exists ]
              │
              ▼
[ Create Parent Dirs for OriginalPath ]
              │
              ▼
[ os.Rename(TrashedPath, OriginalPath) ]
              │
              ▼
[ Remove .trashinfo Metadata File ]
              │
              ▼
[ Re-index File in SQLite (is_deleted = 0) ]
              │
              ▼
[ Update actions_log status = 'restored' ]
```

---

## 8. Local HTTP REST API & Static Server (`internal/api`)

The `api` package exposes standard REST endpoints over HTTP on `127.0.0.1:8080`.

### 8.1 Endpoint Reference

| Method | Endpoint | Handler | Description |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/health` | `handleHealth` | Returns service status and uptime |
| `GET` | `/api/v1/stats` | `handleStats` | Global storage breakdown and category counts |
| `POST` | `/api/v1/scan` | `handleScan` | Triggers background filesystem scan |
| `GET` | `/api/v1/scan/status` | `handleScanStatus` | Live scan progress feed (file count, path, ETA) |
| `GET` | `/api/v1/files/duplicates` | `handleDuplicates` | Paginated duplicate file clusters |
| `GET` | `/api/v1/files/duplicates/breakdown` | `handleDuplicateBreakdown` | Top duplicate file extension breakdown analytics |
| `GET` | `/api/v1/files/stale` | `handleStale` | Ranked stale/inactive files list |
| `GET` | `/api/v1/files/stale/breakdown` | `handleStaleBreakdown` | Top stale file extension breakdown analytics |
| `GET` | `/api/v1/browse` | `handleBrowse` | Lazy directory hierarchy navigation |
| `GET` | `/api/v1/snapshots` | `handleSnapshots` | Time-series historical scan snapshots |
| `POST` | `/api/v1/actions` | `handleActions` | Executes batch trash or permanent deletion |
| `POST` | `/api/v1/actions/restore` | `handleRestore` | Restores a previously trashed file |
| `GET` | `/api/v1/actions/history` | `handleActionHistory` | Returns audit log records |
| `GET` | `/` | `http.FileServer` | Serves embedded frontend UI assets |

---

## 9. CLI Entrypoint & Process Orchestration (`cmd/storage-optimizer`)

The CLI entrypoint in `main.go` parses terminal commands and orchestrates execution:

```bash
# Scan a directory tree with 16 parallel worker goroutines
storage-optimizer scan /home/user/Projects --workers 16

# Find duplicate files and compute wasted space
storage-optimizer duplicates --limit 25

# Identify stale files untouched for 90+ days
storage-optimizer stale --days 90 --limit 50

# View historical time-series snapshots
storage-optimizer snapshots

# Launch local HTTP REST API server on port 8080
storage-optimizer serve --port 8080

# Move files to FreeDesktop XDG Trash
storage-optimizer delete --ids 104,105 --mode trash

# Restore a trashed file back to its original location
storage-optimizer restore --id 1

# View immutable audit log of cleanup actions
storage-optimizer actions
```

### Signal Handling & Graceful Drain
The process intercepts `os.Interrupt` (`SIGINT`) and `syscall.SIGTERM`. Upon receiving a shutdown signal, it stops accepting new scan tasks, drains the single-writer channel funnel to commit active in-flight database batches, and closes the SQLite database cleanly before exiting.

---

## 10. End-to-End Walkthrough: Life of a File

Follow `/home/user/Downloads/archive.zip` through every Go module:

```
1. Discovery (scanner)
   ├── Worker goroutine reads directory /home/user/Downloads
   ├── Calls os.Lstat("/home/user/Downloads/archive.zip")
   └── Extracts Inode 4198201, Size 150 MB, mtime 1700000000 via *syscall.Stat_t

2. Classification (scanner)
   ├── Matches extension ".zip" -> CategoryUserArchive
   └── Sets initial StalenessScore = 0.0

3. Ingestion (db single-writer funnel)
   ├── Pushed into writeChan (chan models.FileMetadata)
   ├── Collected by BatchWriter goroutine into active batch slice
   └── Written to SQLite data/optimizer.db via INSERT INTO files ... ON CONFLICT

4. Deduplication Analysis (dedup)
   ├── Pass 1: Discovers 3 files sharing exact size 150,000,000 bytes
   ├── Pass 2: Opens file, streams through 64 KB buffer into sha256.New()
   ├── Computed hash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
   └── Stored in SQLite. Elected as DuplicateCopy because another copy is older

5. Staleness Scoring (stale)
   ├── atime is 120 days old
   ├── Decay formula: 1 - e^(-0.015 * 120) = 0.834
   ├── CategoryUserArchive weight = 1.0
   └── StalenessScore = 0.834 stored in database

6. User Trashing (action)
   ├── User clicks "Move to Trash" in GUI -> POST /api/v1/actions {"action":"trash", "file_ids":[42]}
   ├── Action engine verifies Inode 4198201 matches current disk stat
   ├── Moves file to ~/.local/share/Trash/files/archive.zip
   ├── Writes metadata to ~/.local/share/Trash/info/archive.zip.trashinfo
   ├── Marks file is_deleted = 1 in SQLite
   └── Writes immutable record to actions_log table with status "success"
```

---

## 11. Systems Engineering Lessons & Linux Gotchas

### 1. The `relatime` Mount Option & Access Times
Most modern Linux distributions mount filesystems with `relatime` instead of `strictatime` for performance reasons. Under `relatime`, the kernel only updates `atime` if the previous `atime` is older than `mtime` or `ctime`, or if more than 24 hours have elapsed since the last access.
- **Impact**: Do not rely exclusively on `atime` for high-frequency access detection. The staleness engine computes inactivity from $\max(\text{atime}, \text{mtime})$ to remain accurate under `relatime`.

### 2. File Descriptor Exhaustion (`EMFILE`)
Opening too many files or directories simultaneously across hundreds of unbounded goroutines exceeds the Linux process limit (`ulimit -n`, typically 1024).
- **Solution**: Bounded worker pools (`runtime.NumCPU() * 2`) strictly limit the maximum number of simultaneously open directory descriptors.

### 3. Cross-Device Link Errors (`EXDEV`)
Using `os.Rename()` across different mount points or disk partitions fails with `invalid cross-device link (EXDEV)`.
- **Solution**: The action engine checks if the target trash directory resides on a different mount point. If an `EXDEV` error occurs, it transparently falls back to `io.Copy()` followed by `os.Remove()`.
