# Python ML Forecasting & Recommendation Layer

Welcome **Sahil** and AI Assistant! This document outlines everything you need to know to build, train, and run the Python Machine Learning & Forecasting layer.

---

## 1. Quick Start: Connecting to the Go Core Backend

The Go core backend provides high-performance file crawling, streaming hashing, and metadata tracking. All data is exposed via a local HTTP REST API on `127.0.0.1:8080`.

### Step 1: Start the Go Core API Server
In your terminal, navigate to the project root and run:
```bash
# Start API server on 127.0.0.1:8080
./go-core/bin/storage-optimizer serve --port 8080
```
*(If you need to rebuild the Go binary: `cd go-core && go build -o bin/storage-optimizer cmd/storage-optimizer/main.go`)*

### Step 2: Test API Health
```bash
curl http://127.0.0.1:8080/api/v1/health
# Response: {"service":"storage-optimizer-core","status":"healthy","version":"1.0.0"}
```

---

## 2. Architectural Rule: Zero SQLite Direct Contention

> [!IMPORTANT]
> **Do NOT execute direct write transactions on `data/optimizer.db` from Python.**
> To avoid SQLite database locking contention (`database is locked`), Python acts as a standard HTTP client that reads data from `http://127.0.0.1:8080/api/v1`.

---

## 3. Data Inputs Available to Python (API Endpoints)

| Endpoint | Target Data | Purpose in ML / Forecasting |
| :--- | :--- | :--- |
| **`GET /api/v1/snapshots`** | Historical `{id, scanned_at, root_path, total_files, total_bytes}` | **Time-series regression / growth forecasting / days-until-full estimation** |
| **`GET /api/v1/stats`** | Category breakdowns (`user`, `temp`, `system_log`, `crash_dump`, `system_cache`) | **Disk distribution analysis & category surge detection** |
| **`GET /api/v1/files/stale?days=30&limit=100`** | Inactive files ranked with `staleness_score` | **Stale / junk cleanup recommendations** |
| **`GET /api/v1/files/duplicates`** | Duplicate file clusters & total wasted bytes | **Deduplication savings potential analysis** |
| **`GET /api/v1/actions/history`** | Past trashed/deleted file logs | **Historical cleanup volume analytics** |

---

## 4. Deliverables for Sahil & AI Agent

Sahil's layer consists of 3 core deliverables:

### Task 1: Time-Series Storage Growth Forecasting (`forecast/`)
- **Objective**: Analyze historical scan snapshots to model disk growth over time and predict when the storage disk will reach 90% or 100% capacity.
- **Model Approaches**:
  - Linear / Polynomial Regression (scikit-learn) for fast trend estimation.
  - Exponential Smoothing (Holt-Winters) or ARIMA / Prophet (statsmodels/prophet) for seasonal / burst patterns.
- **Expected Output Contract**:
  ```json
  {
    "current_bytes": 1582977845,
    "daily_growth_rate_bytes": 45120000,
    "projected_full_date": "2026-11-20",
    "days_until_full": 98,
    "forecast_points": [
      {"date": "2026-08-15", "predicted_bytes": 1628097845, "lower_bound": 1600000000, "upper_bound": 1650000000},
      {"date": "2026-08-22", "predicted_bytes": 1898817845, "lower_bound": 1820000000, "upper_bound": 1970000000}
    ]
  }
  ```

### Task 2: Smart Plain-Language Recommendation Engine (`recommend/`)
- **Objective**: Synthesize file statistics into natural language, actionable suggestions for the desktop GUI.
- **Heuristics & Advice Generation**:
  - Example 1: *"You have **1.2 GB** in duplicate video and document files. Review duplicate groups to reclaim space."*
  - Example 2: *"**450 MB** of build artifacts and cache (`node_modules`, `__pycache__`) haven't been accessed in over 60 days."*
  - Example 3: *"Storage growth spiked by **+15%** over the past 48 hours, driven by temp log files."*

### Task 3: Local Forecasting Microservice (`service.py` / FastAPI or CLI)
- **Objective**: Expose the forecast & recommendations via a simple FastAPI service (e.g. `http://127.0.0.1:8081/forecast`) or CLI script so the Wails GUI frontend can fetch and render dynamic charts.

---

## 5. Suggested Project Directory Structure

```
python-layer/
├── README.md               # This guide
├── requirements.txt        # Python dependencies
├── service.py              # FastAPI / Flask runner or entrypoint
├── forecast/
│   ├── __init__.py
│   ├── model.py            # Regression / ARIMA / Growth forecasting models
│   └── pipeline.py         # Ingestion from Go API & preprocessing
├── recommend/
│   ├── __init__.py
│   ├── engine.py           # Natural language recommendation synthesis
│   └── rules.py            # Thresholds and heuristics
└── tests/
    └── test_forecast.py    # Unit tests for models & endpoints
```

---

## 6. Starter Code Snippet (Connecting to Go Core)

```python
import requests
import pandas as pd

API_BASE = "http://127.0.0.1:8080/api/v1"

def fetch_snapshots() -> pd.DataFrame:
    """Fetches time-series snapshot data from Go Core API."""
    resp = requests.get(f"{API_BASE}/snapshots?limit=100")
    resp.raise_for_status()
    data = resp.json().get("snapshots", [])
    
    df = pd.DataFrame(data)
    if not df.empty:
        df["scanned_at"] = pd.to_datetime(df["scanned_at"])
        df = df.sort_values("scanned_at")
    return df

def fetch_category_stats() -> dict:
    """Fetches category distribution stats."""
    resp = requests.get(f"{API_BASE}/stats")
    resp.raise_for_status()
    return resp.json()

if __name__ == "__main__":
    df = fetch_snapshots()
    print("Fetched Snapshots:\n", df.head())
```
