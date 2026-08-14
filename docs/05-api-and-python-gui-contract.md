# 05 — Local HTTP API & Integration Contract

The Go systems core exposes a lightweight, concurrency-safe local HTTP REST API (`http://127.0.0.1:8080/api/v1`) built with Go's standard `net/http` package.

Both the **Python Forecasting Layer** (Sahil - Day 7) and the **Wails GUI Shell** interact exclusively through this API, completely preventing SQLite file locking conflicts while maintaining maximum throughput.

---

## 1. REST API Endpoints Overview

| Method | Path | Description | Consumers |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/health` | Health check & service ping | Python / GUI / Watchdog |
| `GET` | `/api/v1/stats` | High-level storage overview & category breakdown | GUI Dashboard / Python |
| `POST` | `/api/v1/scan` | Trigger asynchronous directory scan | GUI / CLI |
| `GET` | `/api/v1/scan/status` | Poll live or last-completed scan status | GUI Progress Bar |
| `GET` | `/api/v1/files/duplicates` | Get duplicate file clusters and wasted bytes | Python / GUI |
| `GET` | `/api/v1/files/stale` | Get stale files (`days`, `min_score`, `limit`) | Python / GUI |
| `GET` | `/api/v1/snapshots` | Time-series scan snapshots | Python Forecasting Layer |
| `GET` | `/api/v1/actions/history` | Immutable audit log of past cleanup actions | GUI Audit View |
| `POST` | `/api/v1/actions` | Execute user-confirmed file cleanup (Phase 6) | GUI Action Modals |

---

## 2. Detailed Endpoint Specifications

### 2.1 Health & Service Info
- **Endpoint**: `GET /api/v1/health`
- **Response**: `200 OK`
```json
{
  "service": "storage-optimizer-core",
  "status": "healthy",
  "version": "1.0.0",
  "timestamp": "2026-08-14T10:56:24.833256437Z"
}
```

---

### 2.2 Storage Overview & Category Breakdown
- **Endpoint**: `GET /api/v1/stats`
- **Response**: `200 OK`
```json
{
  "total_files": 109251,
  "total_bytes": 1582977845,
  "total_duplicates": 21810,
  "total_wasted_bytes": 166054115,
  "total_snapshots": 12,
  "categories": [
    {
      "category": "user",
      "total_files": 82923,
      "total_bytes": 1236734723
    },
    {
      "category": "system_protected",
      "total_files": 26119,
      "total_bytes": 269385025
    },
    {
      "category": "temp",
      "total_files": 201,
      "total_bytes": 44627777
    },
    {
      "category": "system_log",
      "total_files": 8,
      "total_bytes": 32230320
    }
  ]
}
```

---

### 2.3 Trigger Directory Scan
- **Endpoint**: `POST /api/v1/scan`
- **Request Body**:
```json
{
  "path": "/home/user/projects",
  "workers": 8,
  "no_prune": false
}
```
- **Response**: `202 Accepted`
```json
{
  "message": "Scan started in background. Poll /api/v1/scan/status for progress.",
  "started_at": "2026-08-14T16:28:16.306620934+05:30",
  "status": "scanning",
  "target_path": "/home/user/projects",
  "workers": 8
}
```

---

### 2.4 Poll Scan Status
- **Endpoint**: `GET /api/v1/scan/status`
- **Response**: `200 OK`
```json
{
  "status": "completed",
  "target_path": "/home/user/projects",
  "started_at": "2026-08-14T16:28:16.306620934+05:30",
  "completed_at": "2026-08-14T16:28:16.352093816+05:30",
  "files_scanned": 14820,
  "dirs_scanned": 1940,
  "total_bytes": 5175829104,
  "pruned_rows": 4,
  "snapshot_id": 13
}
```

---

### 2.5 Get Duplicate File Clusters
- **Endpoint**: `GET /api/v1/files/duplicates?full=false&workers=8`
- **Response**: `200 OK`
```json
{
  "total_groups": 2,
  "total_duplicate_files": 4,
  "total_wasted_bytes": 104857600,
  "duration": 421000000,
  "groups": [
    {
      "content_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "file_size": 52428800,
      "duplicate_count": 2,
      "wasted_bytes": 52428800,
      "files": [
        {
          "id": 42,
          "path": "/home/user/Downloads/video_final.mp4",
          "size": 52428800,
          "mtime": "2026-08-10T12:00:00Z",
          "atime": "2026-08-12T15:30:00Z",
          "inode": 849120,
          "category": "user"
        },
        {
          "id": 98,
          "path": "/home/user/Desktop/video_final_copy.mp4",
          "size": 52428800,
          "mtime": "2026-08-10T12:00:00Z",
          "atime": "2026-08-12T15:30:00Z",
          "inode": 918231,
          "category": "user"
        }
      ]
    }
  ]
}
```

---

### 2.6 Get Stale & Inactive Files
- **Endpoint**: `GET /api/v1/files/stale?days=30&min_score=0.05&limit=100`
- **Response**: `200 OK`
```json
{
  "threshold_days": 30,
  "total_files": 1,
  "total_bytes": 11140,
  "duration": 150000000,
  "files": [
    {
      "id": 176,
      "path": "/home/user/projects/old_app/__pycache__/service.pyc",
      "size": 11140,
      "mtime": "2026-03-04T22:16:08Z",
      "atime": "2026-03-29T17:10:17Z",
      "inode": 3158701,
      "extension": ".pyc",
      "staleness_score": 0.7515,
      "category": "user"
    }
  ]
}
```

---

### 2.7 Historical Snapshots (Time-Series for Python Layer)
- **Endpoint**: `GET /api/v1/snapshots?limit=50&root=/optional/root`
- **Response**: `200 OK`
```json
{
  "total_count": 2,
  "snapshots": [
    {
      "id": 1,
      "scanned_at": "2026-08-13T11:33:10Z",
      "root_path": "/home/user/projects",
      "total_files": 45100,
      "total_bytes": 107374182400
    },
    {
      "id": 2,
      "scanned_at": "2026-08-14T11:33:10Z",
      "root_path": "/home/user/projects",
      "total_files": 48200,
      "total_bytes": 118111600640
    }
  ]
}
```

---

### 2.8 Execute Cleanup Action (Trash or Permanent Delete)
- **Endpoint**: `POST /api/v1/actions`
- **Request Body**:
```json
{
  "ids": [42, 98],
  "mode": "trash"
}
```
*(Options for `mode`: `"trash"` [moves to FreeDesktop XDG Trash] or `"permanent"` [`os.Remove`])*
- **Response**: `200 OK`
```json
{
  "success": true,
  "mode": "trash",
  "processed_count": 2,
  "freed_bytes": 104857600,
  "actions": [
    {
      "id": 101,
      "file_path": "/home/user/Downloads/video_final.mp4",
      "action_mode": "trash",
      "trashed_to_path": "/home/user/.local/share/Trash/files/video_final.mp4",
      "file_size": 52428800,
      "performed_at": "2026-08-14T23:00:00Z"
    }
  ]
}
```

---

### 2.9 Restore File from Trash
- **Endpoint**: `POST /api/v1/actions/restore?id=101` or payload `{"action_id": 101}`
- **Response**: `200 OK`
```json
{
  "success": true,
  "message": "Successfully restored /home/user/Downloads/video_final.mp4",
  "restored": {
    "id": 101,
    "file_path": "/home/user/Downloads/video_final.mp4",
    "action_mode": "trash",
    "trashed_to_path": "/home/user/.local/share/Trash/files/video_final.mp4",
    "file_size": 52428800,
    "performed_at": "2026-08-14T23:00:00Z"
  }
}
```

---

### 2.10 Cleanup Action Audit History
- **Endpoint**: `GET /api/v1/actions/history?limit=50`
- **Response**: `200 OK`
```json
{
  "total_count": 1,
  "actions": [
    {
      "id": 101,
      "file_path": "/home/user/Downloads/video_final.mp4",
      "action_mode": "trash",
      "trashed_to_path": "/home/user/.local/share/Trash/files/video_final.mp4",
      "file_size": 52428800,
      "performed_at": "2026-08-14T23:00:00Z"
    }
  ]
}
```

---

## 3. Guide for Python Forecasting Layer (Sahil & AI Agent)

### 3.1 Backend Startup
To spin up the Go REST API backend for model development and testing:
```bash
./go-core/bin/storage-optimizer serve --port 8080
```

### 3.2 Target Deliverables for Python ML Layer
1. **Time-Series Growth Regression (`python-layer/forecast/`)**:
   - Query `GET /api/v1/snapshots`.
   - Fit regression / ARIMA models on `(scanned_at, total_bytes)` time-series.
   - Forecast: daily growth rate ($MB/\text{day}$), estimated days until disk capacity exhaustion, and 30-day forecast projections.
2. **Plain-Language Recommendation Engine (`python-layer/recommend/`)**:
   - Query `GET /api/v1/files/duplicates`, `GET /api/v1/files/stale?days=30`, and `GET /api/v1/stats`.
   - Synthesize high-value cleanup recommendations for the desktop GUI.
3. **Local Service (`python-layer/service.py`)**:
   - Optional FastAPI / CLI interface to expose ML outputs for the GUI frontend.

---

## 4. Guide for Wails Desktop GUI & Web Dashboard (Phase 7)

### 4.1 Architecture
The GUI frontend (`gui/frontend`) is a high-performance Vanilla JS / HTML5 / CSS3 single-page application that consumes the Go REST API directly.

It operates in two interchangeable modes:
1. **Native Desktop Window**: Built with Wails v2, packaging the app into an 8.3 MB native Linux executable via `webkit2gtk-4.1`.
2. **Embedded Web Dashboard**: Automatically served directly from the Go core server (`http://127.0.0.1:8080/`) with zero external runtime dependencies.

### 4.2 Integration Workflow
- **Dashboard**: Polls `/stats` and `/health` to render real-time category breakdown donuts and space utilization meters.
- **Scanner**: Dispatches `POST /scan` and polls `GET /scan/status` for real-time progress indicators.
- **Duplicate Hunter**: Ingests `/files/duplicates` and dispatches `POST /actions` with user-selected file IDs.
- **Stale Files**: Queries `/files/stale?days=N` with dynamic interactive age chips and category filtering.
- **AI Forecasting**: Ingests `/snapshots` time-series data to render interactive HTML5 Canvas forecast curves with linear regression baseline (ready for Sahil's advanced ML service).
- **Trash & Audit**: Ingests `/actions/history` and dispatches `POST /actions/restore?id=N` for instant file recovery.




