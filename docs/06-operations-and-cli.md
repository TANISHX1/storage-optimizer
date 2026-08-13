# 06 — Operations & CLI Reference

This document provides instructions on building, running, testing, and troubleshooting the Storage Optimizer core.

---

## 1. Building the Go Core

### Prerequisites
- Go 1.21+ (Installed: Go 1.25)
- GCC compiler (for SQLite CGO compilation)

### Build Command
```bash
cd /home/blazex/Documents/git/storage-optimizer/go-core
go build -o bin/storage-optimizer cmd/storage-optimizer/main.go
```

The compiled binary will be placed at `go-core/bin/storage-optimizer`.

---

## 2. CLI Command Reference

### 2.1 Scanning a Directory Tree (`scan`)
Recursively traverses filesystem paths using bounded worker pools, extracts Linux Inode/mtime/atime stats, classifies system vs user assets, and prunes records for deleted files.

```bash
# Scan a directory with default worker count (runtime.NumCPU)
./bin/storage-optimizer scan /path/to/directory

# Scan with 16 workers and a custom SQLite database location
./bin/storage-optimizer scan /home/user/projects --workers 16 --db ../data/optimizer.db

# Incremental scan (automatically purges deleted files and updates modified ones)
./bin/storage-optimizer scan /home/blazex/Documents/git/seat-allocation-sys
```

#### Real-World Benchmark Performance:
```text
==> Initializing SQLite Database: ../data/optimizer.db
==> Starting Concurrent Scan: /home/blazex/Documents/git/seat-allocation-sys (Workers: 12)

=== Scan & Incremental Sync Completed ===
• Root Directory:   /home/blazex/Documents/git/seat-allocation-sys
• Files Indexed:    109,041
• Dirs Traversed:   12,607
• Total Size:       1.43 GB (1530769555 bytes)
• Permission Skips: 0
• Time Elapsed:     4.004s
• Throughput:       27,230.5 files/sec | 364.57 MB/sec
• Snapshot ID:      #11 (recorded for Python forecasting)
```

---

### 2.2 Finding Duplicate Files (`duplicates`)
Executes an I/O-optimized two-pass deduplication algorithm (Size Bucketing $\rightarrow$ Streaming SHA-256 in 64 KB buffers):

```bash
./bin/storage-optimizer duplicates

# Force full re-hashing of candidate files
./bin/storage-optimizer duplicates --full
```

---

### 2.3 Ranking Stale & Unused Files (`stale`)
Ranks inactive files based on exponential age decay, access/modification dynamics, and system path heuristic weights:

```bash
# Find files untouched for 60+ days
./bin/storage-optimizer stale --days 60

# Inspect top 100 stale candidates with a minimum score of 0.40
./bin/storage-optimizer stale --days 30 --min-score 0.40 --limit 100
```

---

### 2.4 Time-Series Snapshots (`snapshots`)
Displays historical scan snapshots recorded across scan runs:

```bash
./bin/storage-optimizer snapshots
```

#### Example Output:
```text
=== Historical Scan Snapshots (Time-Series for Python Layer) ===
• Total Snapshots Recorded: 12

ID      TIMESTAMP             FILES         STORAGE         ROOT PATH
----------------------------------------------------------------------------------------
#11     2026-08-13 23:09:42   109041        1.43 GB         /home/blazex/Documents/git/seat-allocation-sys
#12     2026-08-13 23:10:04   201           42.56 MB        /tmp
----------------------------------------------------------------------------------------
```

---

## 3. SQLite Direct Inspection

You can inspect the database directly using the `sqlite3` CLI:

```bash
# Inspect top 10 largest files
sqlite3 data/optimizer.db "SELECT id, path, size, category FROM files ORDER BY size DESC LIMIT 10;"

# Count files by category
sqlite3 data/optimizer.db "SELECT category, COUNT(*), SUM(size) FROM files GROUP BY category;"

# Inspect historical snapshots
sqlite3 data/optimizer.db "SELECT id, datetime(scanned_at, 'unixepoch'), root_path, total_files, total_bytes FROM scan_snapshots;"
```
