# 06 — Operations & CLI Reference

This document provides instructions on building, running, testing, and troubleshooting the Storage Optimizer core.

---

## 1. Building the Go Core

### Prerequisites
- Go 1.21+ (Installed: Go 1.25)
- GCC compiler (for SQLite CGO compilation)

### Build Command
```bash
cd go-core
go build -o bin/storage-optimizer cmd/storage-optimizer/main.go
```

The compiled binary will be placed at `go-core/bin/storage-optimizer`.

---

## 2. CLI Usage Reference

### 2.1 Scanning a Directory Tree
```bash
# Scan a directory with default worker count (runtime.NumCPU)
./bin/storage-optimizer scan /path/to/directory

# Scan with 16 workers and a custom SQLite database location
./bin/storage-optimizer scan /home/user/projects --workers 16 --db ../data/optimizer.db
```

#### Example Output:
```text
==> Initializing SQLite Database: ../data/optimizer.db
==> Starting Concurrent Scan: /home/user/projects (Workers: 12)

=== Scan Completed Successfully ===
• Root Directory:   /home/user/projects
• Files Indexed:    14,820
• Dirs Traversed:   1,940
• Total Size:       4.82 GB (5175829104 bytes)
• Permission Skips: 0
• Time Elapsed:     842ms
• Throughput:       17596.2 files/sec | 5724.11 MB/sec
• Snapshot ID:      #1 (ready for Python forecasting layer)
```

---

## 3. SQLite Direct Inspection

You can inspect the database directly using the `sqlite3` CLI:

```bash
# Inspect top 10 largest files
sqlite3 data/optimizer.db "SELECT id, path, size, extension FROM files ORDER BY size DESC LIMIT 10;"

# Inspect historical snapshots
sqlite3 data/optimizer.db "SELECT id, datetime(scanned_at, 'unixepoch'), root_path, total_files, total_bytes FROM scan_snapshots;"

# Inspect audit log of actions
sqlite3 data/optimizer.db "SELECT * FROM actions_log ORDER BY performed_at DESC;"
```

---

## 4. Troubleshooting & Performance Tuning

### `database is locked` Error
- **Cause**: An external program opened the SQLite file with an uncommitted write lock.
- **Resolution**: Ensure all writes go through the Go core's single-writer funnel. Check `PRAGMA busy_timeout = 5000;` is active.

### Symlink Loops & Duplicate Counting
- **Protection**: The scanner uses `os.Lstat()` instead of `os.Stat()`. Any entry with `info.Mode() & os.ModeSymlink != 0` is bypassed automatically.
