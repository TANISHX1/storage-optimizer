from typing import Any, Optional
from datetime import datetime

from fastapi import FastAPI, HTTPException, Query
from fastapi.middleware.cors import CORSMiddleware
from forecast.capacity import calculate_capacity_prediction
from core.api_provider import GoCoreAPIError, GoCoreProvider
from forecast.forecast import (
    get_unified_forecast,
    ForecastNotReadyError,
    forecast_storage_from_provider
)
from recommend.pipeline import run_recommendation_pipeline

# =========================================================
# Configuration & Defaults
# =========================================================

GO_CORE_URL = "http://127.0.0.1:8080/api/v1"
DEFAULT_TOTAL_CAPACITY_BYTES = 256 * (1024 ** 3)
FORECAST_DAYS = 30
CAPACITY_FORECAST_DAYS = 365
STALE_DAYS = 30

# =========================================================
# FastAPI Application
# =========================================================

app = FastAPI(
    title="Storage Optimizer ML Service",
    description="Adaptive Python ML forecasting and smart recommendation service.",
    version="2.1.0",
)

# Enable CORS for desktop/browser clients
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


def load_provider() -> GoCoreProvider:
    return GoCoreProvider(
        base_url=GO_CORE_URL,
        timeout=15.0,
    )


# =========================================================
# Serialization Helpers
# =========================================================

def serialize_forecast_points(points) -> list[dict[str, Any]]:
    return [
        {
            "date": point.date.isoformat(),
            "predicted_bytes": float(point.predicted_bytes),
            "lower_bound": (
                float(point.lower_bound)
                if point.lower_bound is not None
                else None
            ),
            "upper_bound": (
                float(point.upper_bound)
                if point.upper_bound is not None
                else None
            ),
        }
        for point in points
    ]


def serialize_recommendation(recommendation) -> dict[str, Any]:
    return {
        "type": recommendation.type,
        "severity": recommendation.severity,
        "title": recommendation.title,
        "message": recommendation.message,
        "potential_savings_bytes": recommendation.potential_savings_bytes,
        "days_until_action": recommendation.days_until_action,
        "metadata": getattr(recommendation, "metadata", {}),
    }


# =========================================================
# Forecast Engine Helper
# =========================================================


def build_forecast(
    provider: GoCoreProvider,
    root: Optional[str] = None,
    forecast_days: int = FORECAST_DAYS,
) -> dict[str, Any]:
    try:
        result = get_unified_forecast(provider, root=root)
    except ForecastNotReadyError as exc:
        status = exc.status
        return {
            "status": status.status,
            "root": status.root,
            "snapshots_available": status.snapshots_available,
            "snapshots_required": status.snapshots_required,
            "history_days": status.history_days,
            "message": status.message,
            "models": None,
            "selected_model": None,
        }

    # forecast_days is a display window into the same unified
    # result — it never triggers a separate model fit.
    display_points = result.forecast_points[:forecast_days]

    return {
        "status": "ready",
        "root": root,
        "snapshots_available": result.snapshots_used,
        "snapshots_required": result.snapshots_required,
        "history_days": result.history_days,
        "message": f"Forecast generated using {result.model_name}.",
        "selected_model": result.model_name,
        "validation": {
            "mae_bytes": result.mae_bytes,
            "rmse_bytes": result.rmse_bytes,
        },
        "models": {
            "selected": result.model_name,
            "points": serialize_forecast_points(display_points),
        },
    }

# =========================================================
# API Endpoints
# =========================================================

@app.get("/")
def root():
    return {
        "service": "storage-optimizer-ml",
        "status": "running",
        "version": "2.1.0",
        "go_core": GO_CORE_URL,
        "endpoints": [
            "/health",
            "/forecast",
            "/capacity",
            "/recommendations",
            "/analysis",
        ],
    }


@app.get("/health")
def health():
    try:
        provider = load_provider()
        go_health = provider.health()
        snapshots = provider.get_snapshots(limit=10)
        return {
            "service": "storage-optimizer-ml",
            "status": "healthy",
            "version": "2.1.0",
            "go_core": go_health,
            "total_snapshots_in_db": len(snapshots),
        }
    except Exception as exc:
        raise HTTPException(
            status_code=503,
            detail={
                "service": "storage-optimizer-ml",
                "status": "unhealthy",
                "go_core": "unavailable",
                "error": str(exc),
            },
        )


@app.get("/forecast")
def forecast(
    root: Optional[str] = Query(default=None, description="Root directory to forecast"),
    days: int = Query(default=30, ge=1, le=365, description="Days to forecast"),
):
    try:
        provider = load_provider()
        snapshots = provider.get_snapshots(root=root)

        if not snapshots:
            return {
                "root": root,
                "current": None,
                "forecast": {
                    "status": "warming_up",
                    "root": root,
                    "snapshots_available": 0,
                    "snapshots_required": 2,
                    "history_days": 0.0,
                    "message": "No scan snapshots found in database. Perform a filesystem scan to begin forecasting.",
                    "models": None,
                    "selected_model": None,
                },
            }

        ordered = sorted(snapshots, key=lambda s: s.scanned_at)
        current = ordered[-1]
        forecast_data = build_forecast(provider, root=root, forecast_days=days)

        return {
            "root": root or current.root_path,
            "current": {
                "date": current.scanned_at.isoformat(),
                "bytes": current.total_bytes,
                "files": current.total_files,
            },
            "forecast": forecast_data,
        }
    except GoCoreAPIError as exc:
        raise HTTPException(status_code=503, detail=str(exc))
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc))

@app.get("/capacity")
def capacity(
    root: Optional[str] = Query(default=None),
    total_capacity: Optional[int] = Query(default=None),
):
    try:
        provider = load_provider()
        capacity_bytes = total_capacity if (total_capacity and total_capacity > 0) else DEFAULT_TOTAL_CAPACITY_BYTES
        snapshots = provider.get_snapshots(root=root)

        if not snapshots:
            return {
                "status": "warming_up",
                "root": root,
                "current": None,
                "capacity": {
                    "total_bytes": capacity_bytes,
                    "total_gb": capacity_bytes / (1024 ** 3),
                    "utilization_percent": 0.0,
                },
                "capacity_prediction": None,
                "message": "No snapshots found in database.",
            }

        current = max(snapshots, key=lambda s: s.scanned_at)
        target_root = root or current.root_path

        base = {
            "status": "warming_up",
            "root": target_root,
            "current": {
                "date": current.scanned_at.isoformat(),
                "bytes": current.total_bytes,
                "files": current.total_files,
            },
            "capacity": {
                "total_bytes": capacity_bytes,
                "total_gb": capacity_bytes / (1024 ** 3),
                "utilization_percent": (current.total_bytes / capacity_bytes * 100) if capacity_bytes > 0 else 0,
            },
            "capacity_prediction": None,
        }

        try:
            result = get_unified_forecast(provider, root=root)
        except ForecastNotReadyError as exc:
            base["message"] = exc.status.message
            return base

        cap_pred = calculate_capacity_prediction(
            current_bytes=current.total_bytes,
            current_date=current.scanned_at,
            total_capacity_bytes=capacity_bytes,
            forecast_points=result.forecast_points,
        )

        base["status"] = "ready"
        base["model"] = result.model_name
        base["capacity_prediction"] = {
            "current_bytes": cap_pred.current_bytes,
            "utilization_percent": cap_pred.current_utilization_percent,
            "90_percent": {
                "threshold_bytes": cap_pred.threshold_90_bytes,
                "date": cap_pred.date_at_90_percent.isoformat() if cap_pred.date_at_90_percent else None,
                "days_until": cap_pred.days_until_90_percent,
            },
            "100_percent": {
                "threshold_bytes": cap_pred.threshold_100_bytes,
                "date": cap_pred.date_at_100_percent.isoformat() if cap_pred.date_at_100_percent else None,
                "days_until": cap_pred.days_until_100_percent,
            },
        }
        return base
    except GoCoreAPIError as exc:
        raise HTTPException(status_code=503, detail=str(exc))
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc))
    
@app.get("/recommendations")
def recommendations(
    root: Optional[str] = Query(default=None, description="Root directory"),
    total_capacity: Optional[int] = Query(default=None, description="Total capacity in bytes"),
    stale_days: int = Query(default=STALE_DAYS, ge=1, description="Threshold for stale files"),
):
    try:
        provider = load_provider()
        capacity_bytes = total_capacity if (total_capacity and total_capacity > 0) else DEFAULT_TOTAL_CAPACITY_BYTES

        result = run_recommendation_pipeline(
            provider=provider,
            total_capacity_bytes=capacity_bytes,
            forecast_days=CAPACITY_FORECAST_DAYS,
            stale_days=stale_days,
            root=root,
        )

        capacity_info = result["capacity"]
        recommendations_data = [
            serialize_recommendation(r)
            for r in result["recommendations"]
        ]

        latest_snap = result["snapshots"][-1] if result["snapshots"] else None

        response = {
            "root": root or (latest_snap.root_path if latest_snap else None),
            "current": {
                "bytes": latest_snap.total_bytes if latest_snap else 0,
                "files": latest_snap.total_files if latest_snap else 0,
            },
            "growth": {
                "daily_growth_bytes": result["daily_growth_bytes"],
            },
            "storage_analysis": {
                "duplicate_waste_bytes": result["duplicate_bytes"],
                "stale_storage_bytes": result["stale_bytes"],
            },
            "forecast_status": result["forecast_status"],
            "recommendations": recommendations_data,
            "recommendation_count": len(recommendations_data),
        }

        if capacity_info is not None:
            response["capacity"] = {
                "total_bytes": capacity_info.total_capacity_bytes,
                "current_bytes": capacity_info.current_bytes,
                "utilization_percent": capacity_info.current_utilization_percent,
                "90_percent": {
                    "date": capacity_info.date_at_90_percent.isoformat() if capacity_info.date_at_90_percent else None,
                    "days_until": capacity_info.days_until_90_percent,
                },
                "100_percent": {
                    "date": capacity_info.date_at_100_percent.isoformat() if capacity_info.date_at_100_percent else None,
                    "days_until": capacity_info.days_until_100_percent,
                },
            }
        else:
            response["capacity"] = None

        return response
    except GoCoreAPIError as exc:
        raise HTTPException(status_code=503, detail=str(exc))
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc))


@app.get("/analysis")
def analysis(
    root: Optional[str] = Query(default=None, description="Root directory"),
    total_capacity: Optional[int] = Query(default=None, description="Total capacity in bytes"),
):
    try:
        provider = load_provider()
        capacity_bytes = total_capacity if (total_capacity and total_capacity > 0) else DEFAULT_TOTAL_CAPACITY_BYTES

        result = run_recommendation_pipeline(
            provider=provider,
            total_capacity_bytes=capacity_bytes,
            forecast_days=CAPACITY_FORECAST_DAYS,
            stale_days=STALE_DAYS,
            root=root,
        )

        snapshots = result["snapshots"]
        latest_snap = snapshots[-1] if snapshots else None

        recommendations_data = [
            serialize_recommendation(r)
            for r in result["recommendations"]
        ]

        history_days = 0.0
        if len(snapshots) >= 2:
            ordered = sorted(snapshots, key=lambda s: s.scanned_at)
            history_days = (ordered[-1].scanned_at - ordered[0].scanned_at).total_seconds() / 86400

        forecast_section = {
            "status": result["forecast_status"],
            "root": root or (latest_snap.root_path if latest_snap else None),
            "snapshots_available": len(snapshots),
            "snapshots_required": 7,
            "history_days": history_days,
            "selected_model": None,
            "models": None,
        }

        if result["forecast"] is not None:
            f_res = result["forecast"]
            forecast_section["selected_model"] = f_res.model_name
            forecast_section["validation"] = {
                "mae_bytes": f_res.mae_bytes,
                "rmse_bytes": f_res.rmse_bytes,
            }
            forecast_section["models"] = {
                "selected": f_res.model_name,
                "points": serialize_forecast_points(f_res.forecast_points),
            }

        capacity_section = None
        cap = result["capacity"]
        if cap is not None:
            capacity_section = {
                "total_bytes": cap.total_capacity_bytes,
                "total_gb": cap.total_capacity_bytes / (1024 ** 3),
                "current_bytes": cap.current_bytes,
                "utilization_percent": cap.current_utilization_percent,
                "90_percent": {
                    "threshold_bytes": cap.threshold_90_bytes,
                    "date": cap.date_at_90_percent.isoformat() if cap.date_at_90_percent else None,
                    "days_until": cap.days_until_90_percent,
                },
                "100_percent": {
                    "threshold_bytes": cap.threshold_100_bytes,
                    "date": cap.date_at_100_percent.isoformat() if cap.date_at_100_percent else None,
                    "days_until": cap.days_until_100_percent,
                },
            }

        return {
            "service": "storage-optimizer-ml",
            "version": "2.1.0",
            "root": root or (latest_snap.root_path if latest_snap else None),
            "current": {
                "date": latest_snap.scanned_at.isoformat() if latest_snap else datetime.utcnow().isoformat(),
                "bytes": latest_snap.total_bytes if latest_snap else 0,
                "files": latest_snap.total_files if latest_snap else 0,
            },
            "growth": {
                "daily_growth_bytes": result["daily_growth_bytes"],
            },
            "storage_analysis": {
                "duplicate_waste_bytes": result["duplicate_bytes"],
                "stale_storage_bytes": result["stale_bytes"],
            },
            "categories": result["category_stats"],
            "forecast": forecast_section,
            "capacity": capacity_section,
            "recommendations": recommendations_data,
            "recommendation_count": len(recommendations_data),
        }
    except GoCoreAPIError as exc:
        raise HTTPException(status_code=503, detail=str(exc))
    except Exception as exc:
        raise HTTPException(status_code=500, detail=str(exc))