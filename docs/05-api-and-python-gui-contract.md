# 05 — Local HTTP API & Integration Contract

The Go systems core exposes a lightweight local HTTP REST API (`http://127.0.0.1:8080/api/v1`) using Go's standard `net/http` package.

Both the **Python Forecasting Layer** (Sahil - Day 7) and the **Wails GUI Shell** interact exclusively through this API, completely preventing SQLite file locking conflicts.

---

## 1. REST API Endpoints Overview

| Method | Path | Description | Consumers |
| :--- | :--- | :--- | :--- |
| `POST` | `/api/v1/scan` | Trigger filesystem scan on a path | GUI / CLI |
| `GET` | `/api/v1/scan/status` | Current scan progress / stats | GUI |
| `GET` | `/api/v1/files/duplicates` | Get duplicate file groups | Python / GUI |
| `GET` | `/api/v1/files/stale?days=N` | Get stale files untouched for $N+$ days | Python / GUI |
| `GET` | `/api/v1/snapshots` | Historical snapshot time-series | Python Forecasting |
| `POST` | `/api/v1/actions` | Execute user-confirmed file cleanup | GUI Action Modals |
| `POST` | `/api/v1/actions/restore` | Restore previously trashed file | GUI Trash View |
| `GET` | `/api/v1/actions/history` | Audit log of past actions | GUI Audit View |

---

## 2. Endpoint Specifications

### 2.1 Trigger Scan
- **Request**: `POST /api/v1/scan`
```json
{
  "path": "/home/user/projects",
  "full": false,
  "workers": 8
}
```
- **Response**: `202 Accepted`
```json
{
  "status": "scanning",
  "target_path": "/home/user/projects",
  "started_at": "2026-08-13T06:00:00Z"
}
```

---

### 2.2 Get Duplicate File Groups
- **Request**: `GET /api/v1/files/duplicates`
- **Response**: `200 OK`
```json
{
  "total_groups": 2,
  "wasted_bytes": 104857600,
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
          "mtime": 1755060000,
          "inode": 849120
        },
        {
          "id": 98,
          "path": "/home/user/Desktop/video_final_copy.mp4",
          "mtime": 1755060000,
          "inode": 918231
        }
      ]
    }
  ]
}
```

---

### 2.3 Get Historical Snapshots (For Sahil's Python Layer)
- **Request**: `GET /api/v1/snapshots`
- **Response**: `200 OK`
```json
{
  "snapshots": [
    {
      "id": 1,
      "scanned_at": 1754000000,
      "root_path": "/home/user",
      "total_files": 45100,
      "total_bytes": 107374182400
    },
    {
      "id": 2,
      "scanned_at": 1754600000,
      "root_path": "/home/user",
      "total_files": 48200,
      "total_bytes": 118111600640
    }
  ]
}
```

---

### 2.4 Execute Cleanup Action
- **Request**: `POST /api/v1/actions`
```json
{
  "ids": [42, 98],
  "mode": "trash"
}
```
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
      "trashed_to_path": "/home/user/.storage-optimizer/trash/files/uuid_video_final.mp4",
      "file_size": 52428800,
      "performed_at": "2026-08-13T06:05:00Z"
    }
  ]
}
```
