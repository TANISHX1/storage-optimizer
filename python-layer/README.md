# Python Forecasting & Recommendation Layer

Welcome Sahil! This directory is dedicated to the Python forecasting, disk-growth modeling, and recommendation service starting on Day 7.

## Communication with Core

**Important Architecture Rule**: Do not connect directly to `data/optimizer.db` with write transactions.
All scan results and historical snapshots are provided by the Go core via the local HTTP REST API at `http://127.0.0.1:8080/api/v1`.

### Endpoints Available to Python

1. **`GET /api/v1/snapshots`**
   - Returns time-series list of `{id, scanned_at, root_path, total_files, total_bytes}`.
   - Use this to train / run your disk growth regression / forecasting models.

2. **`GET /api/v1/files/duplicates`**
   - Returns clusters of identical files grouped by hash with total wasted bytes.

3. **`GET /api/v1/files/stale?days=90`**
   - Returns candidate stale files sorted by computed staleness score and size.

4. **`GET /api/v1/actions/history`**
   - Returns history of trashed / deleted files.

## Suggested Directory Layout

```
python-layer/
├── requirements.txt
├── forecast/
│   ├── model.py           # Linear/polynomial or Prophet/ARIMA forecasting
│   └── pipeline.py
├── recommend/
│   ├── engine.py          # Plain-language cleanup recommendation generator
│   └── rules.py           # Heuristics for cleanup advice
└── service.py             # Optional local microservice / CLI helper
```
