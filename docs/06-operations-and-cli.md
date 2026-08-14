# 06 — Operations & CLI Reference

This document provides instructions on building, running, testing, and troubleshooting the Storage Optimizer core and its HTTP REST API service.

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

### 2.1 Running the HTTP REST API Server (`serve` / `api`)
Starts the high-performance local HTTP server on `127.0.0.1:8080` (or custom port). Unblocks GUI frontend and Sahil's Python forecasting microservice.

```bash
# Start API server on default port 8080
./bin/storage-optimizer serve

# Start API server on custom port 8085 with custom database
./bin/storage-optimizer serve --port 8085 --db ../data/optimizer.db
```

#### API Curl Quick Reference:
```bash
# Health check
curl -s http://127.0.0.1:8080/api/v1/health | jq .

# High-level storage stats & category breakdowns
curl -s http://127.0.0.1:8080/api/v1/stats | jq .

# Trigger background directory scan
curl -s -X POST http://127.0.0.1:8080/api/v1/scan \
  -H "Content-Type: application/json" \
  -d '{"path": "/home/user/projects", "workers": 8}' | jq .

# Poll scan status
curl -s http://127.0.0.1:8080/api/v1/scan/status | jq .

# Duplicate file groups & reclaimable space
curl -s http://127.0.0.1:8080/api/v1/files/duplicates | jq .

# Stale files untouched for 60+ days
curl -s "http://127.0.0.1:8080/api/v1/files/stale?days=60&limit=50" | jq .

# Historical scan snapshots
curl -s "http://127.0.0.1:8080/api/v1/snapshots?limit=20" | jq .
```

---

### 2.2 Scanning a Directory Tree (`scan`)
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

### 2.3 Finding Duplicate Files (`duplicates`)
Executes an I/O-optimized two-pass deduplication algorithm (Size Bucketing $\rightarrow$ Streaming SHA-256 in 64 KB buffers):

```bash
./bin/storage-optimizer duplicates

# Force full re-hashing of candidate files
./bin/storage-optimizer duplicates --full
```

---

### 2.4 Ranking Stale & Unused Files (`stale`)
Ranks inactive files based on exponential age decay, access/modification dynamics, and system path heuristic weights:

```bash
# Find files untouched for 60+ days
./bin/storage-optimizer stale --days 60

# Inspect top 100 stale candidates with a minimum score of 0.40
./bin/storage-optimizer stale --days 30 --min-score 0.40 --limit 100
```

---

### 2.5 Time-Series Snapshots (`snapshots`)
Displays historical scan snapshots recorded across scan runs:

```bash
./bin/storage-optimizer snapshots
```

---

### 2.6 Executing Safe File Cleanup (`delete`)
Executes user-confirmed cleanup actions with strict pre-mutation safety gating (category protection, path blocks, disk inode verification):

```bash
# Move files to FreeDesktop.org OS Native Trash (Default & Recommended)
./bin/storage-optimizer delete --ids 104,105 --mode trash

# Permanently destroy files (logs audit record before os.Remove)
./bin/storage-optimizer delete --ids 106 --mode permanent
```

---

### 2.7 Restoring Trashed Files (`restore`)
Restores a previously trashed file back to its original filesystem path and re-indexes it into SQLite:

```bash
# Restore file associated with Action Log #1
./bin/storage-optimizer restore --id 1
```

---

### 2.8 Viewing Cleanup Audit History (`actions`)
Displays an immutable audit log of all cleanup operations performed by the engine:

```bash
./bin/storage-optimizer actions
./bin/storage-optimizer actions --limit 50
```

---

## 3. SQLite Direct Inspection

You can inspect the database directly using the `sqlite3` CLI:

```bash
# Inspect top 10 largest files
sqlite3 data/optimizer.db "SELECT id, path, size, category FROM files ORDER BY size DESC LIMIT 10;"

# Count files by category
sqlite3 data/optimizer.db "SELECT category, COUNT(*), SUM(size) FROM files GROUP BY category;"

# Inspect cleanup audit logs
sqlite3 data/optimizer.db "SELECT id, file_path, action_mode, trashed_to_path, file_size, datetime(performed_at, 'unixepoch') FROM actions_log;"

# Inspect historical snapshots
sqlite3 data/optimizer.db "SELECT id, datetime(scanned_at, 'unixepoch'), root_path, total_files, total_bytes FROM scan_snapshots;"
```

---

## 4. GUI Desktop & Web Operations (Phase 7)

### 4.1 Running the Built-in Web Dashboard
The Go core binary embeds the production GUI and serves it over HTTP:
```bash
./bin/storage-optimizer serve --port 8080
```
Open **`http://127.0.0.1:8080/`** in any web browser.

### 4.2 Running the Native Desktop Application (Wails v2)
To launch the native Linux desktop window:
```bash
# Launch pre-compiled native desktop binary:
./gui/build/bin/storage-optimizer-gui

# Or run live development mode with hot reload:
cd gui && wails dev
```



