from typing import Any

from fastapi import FastAPI, HTTPException

from core.api_provider import GoCoreAPIError, GoCoreProvider

from forecast.forecast import (
    get_forecast_status,
    forecast_storage_from_provider,
)

from recommend.pipeline import run_recommendation_pipeline


# =========================================================
# Configuration
# =========================================================

GO_CORE_URL = "http://127.0.0.1:8080/api/v1"

MONITORED_ROOT = (
    "/home/vanshpratapsinghjadon/Desktop/storage-optimizer"
)

TOTAL_CAPACITY_BYTES = 256 * 1024**3

FORECAST_DAYS = 30

CAPACITY_FORECAST_DAYS = 365

STALE_DAYS = 30


# =========================================================
# FastAPI
# =========================================================

app = FastAPI(
    title="Storage Optimizer ML Service",
    description=(
        "Live Python ML, forecasting and recommendation "
        "service backed by the Go Core REST API."
    ),
    version="2.0.0",
)


# =========================================================
# Provider
# =========================================================

def load_provider() -> GoCoreProvider:
    return GoCoreProvider(
        base_url=GO_CORE_URL,
        timeout=15.0,
    )


# =========================================================
# Serialization Helpers
# =========================================================

def serialize_forecast_points(
    points,
) -> list[dict[str, Any]]:

    return [
        {
            "date": point.date.isoformat(),
            "predicted_bytes": float(
                point.predicted_bytes
            ),
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


def serialize_recommendation(
    recommendation,
) -> dict[str, Any]:

    return {
        "type": recommendation.type,
        "severity": recommendation.severity,
        "title": recommendation.title,
        "message": recommendation.message,
        "potential_savings_bytes": (
            recommendation.potential_savings_bytes
        ),
        "days_until_action": (
            recommendation.days_until_action
        ),
        "metadata": recommendation.metadata,
    }


# =========================================================
# Forecast State
# =========================================================

def build_forecast(
    provider: GoCoreProvider,
):
    """
    Determine whether the live root has enough history.

    Returns a normalized forecast response whether the system
    is warming up or ready.
    """

    snapshots = provider.get_snapshots(
        root=MONITORED_ROOT,
        limit=1000,
    )

    status = get_forecast_status(
        snapshots,
        root=MONITORED_ROOT,
    )

    ordered_snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    current_snapshot = (
        ordered_snapshots[-1]
        if ordered_snapshots
        else None
    )

    history_days = 0.0

    if len(ordered_snapshots) >= 2:
        history_days = (
            ordered_snapshots[-1].scanned_at
            - ordered_snapshots[0].scanned_at
        ).total_seconds() / 86400

    base = {
        "status": status.status,
        "root": MONITORED_ROOT,
        "snapshots_available": (
            status.snapshots_available
        ),
        "snapshots_required": (
            status.snapshots_required
        ),
        "history_days": history_days,
    }

    if status.status != "ready":
        base["message"] = status.message
        base["models"] = None
        base["selected_model"] = None

        return base

    try:
        result = forecast_storage_from_provider(
            provider=provider,
            root=MONITORED_ROOT,
            forecast_days=FORECAST_DAYS,
            validation_size=3,
        )
    except ValueError as exc:
        return {
            **base,
            "status": "warming_up",
            "message": str(exc),
            "models": None,
            "selected_model": None,
        }

    return {
        **base,
        "status": "ready",
        "message": (
            "Enough historical data is available "
            "for forecasting."
        ),
        "selected_model": result.model_name,
        "validation": {
            "mae_bytes": result.mae_bytes,
            "rmse_bytes": result.rmse_bytes,
        },
        "models": {
            "selected": result.model_name,
            "points": serialize_forecast_points(
                result.forecast_points
            ),
        },
    }


# =========================================================
# Root
# =========================================================

@app.get("/")
def root():

    return {
        "service": "storage-optimizer-ml",
        "status": "running",
        "version": "2.0.0",
        "go_core": GO_CORE_URL,
        "monitored_root": MONITORED_ROOT,
        "endpoints": [
            "/health",
            "/forecast",
            "/capacity",
            "/recommendations",
            "/analysis",
        ],
    }


# =========================================================
# Health
# =========================================================

@app.get("/health")
def health():

    try:
        provider = load_provider()

        go_health = provider.health()

        return {
            "service": "storage-optimizer-ml",
            "status": "healthy",
            "version": "2.0.0",
            "go_core": go_health,
            "monitored_root": MONITORED_ROOT,
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


# =========================================================
# Forecast
# =========================================================

@app.get("/forecast")
def forecast():

    try:

        provider = load_provider()

        current_snapshots = provider.get_snapshots(
            root=MONITORED_ROOT,
            limit=1000,
        )

        if not current_snapshots:
            raise HTTPException(
                status_code=404,
                detail=(
                    "No snapshots found for monitored root: "
                    f"{MONITORED_ROOT}"
                ),
            )

        ordered = sorted(
            current_snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        current = ordered[-1]

        forecast_data = build_forecast(
            provider
        )

        return {
            "root": MONITORED_ROOT,

            "current": {
                "date": current.scanned_at.isoformat(),
                "bytes": current.total_bytes,
                "files": current.total_files,
            },

            "forecast": forecast_data,
        }

    except HTTPException:
        raise

    except GoCoreAPIError as exc:

        raise HTTPException(
            status_code=503,
            detail=str(exc),
        )

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )


# =========================================================
# Capacity
# =========================================================

@app.get("/capacity")
def capacity():

    try:

        provider = load_provider()

        snapshots = provider.get_snapshots(
            root=MONITORED_ROOT,
            limit=1000,
        )

        if not snapshots:
            raise HTTPException(
                status_code=404,
                detail=(
                    "No snapshots found for monitored root: "
                    f"{MONITORED_ROOT}"
                ),
            )

        forecast_status = get_forecast_status(
            snapshots,
            root=MONITORED_ROOT,
        )

        ordered = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        current = ordered[-1]

        base = {
            "status": forecast_status.status,

            "root": MONITORED_ROOT,

            "current": {
                "date": current.scanned_at.isoformat(),
                "bytes": current.total_bytes,
                "files": current.total_files,
            },

            "capacity": {
                "total_bytes": TOTAL_CAPACITY_BYTES,
                "total_gb": (
                    TOTAL_CAPACITY_BYTES
                    / (1024 ** 3)
                ),
                "utilization_percent": (
                    current.total_bytes
                    / TOTAL_CAPACITY_BYTES
                    * 100
                ),
            },
        }

        if forecast_status.status != "ready":

            base["capacity_prediction"] = None

            base["message"] = forecast_status.message

            return base

        result = forecast_storage_from_provider(
            provider=provider,
            root=MONITORED_ROOT,
            forecast_days=CAPACITY_FORECAST_DAYS,
            validation_size=3,
        )

        from forecast.capacity import (
            calculate_capacity_prediction,
        )

        capacity_prediction = (
            calculate_capacity_prediction(
                current_bytes=current.total_bytes,
                current_date=current.scanned_at,
                total_capacity_bytes=(
                    TOTAL_CAPACITY_BYTES
                ),
                forecast_points=(
                    result.forecast_points
                ),
            )
        )

        base["model"] = result.model_name

        base["capacity_prediction"] = {
            "current_bytes": (
                capacity_prediction.current_bytes
            ),
            "utilization_percent": (
                capacity_prediction
                .current_utilization_percent
            ),
            "90_percent": {
                "threshold_bytes": (
                    capacity_prediction
                    .threshold_90_bytes
                ),
                "date": (
                    capacity_prediction
                    .date_at_90_percent.isoformat()
                    if capacity_prediction
                    .date_at_90_percent
                    else None
                ),
                "days_until": (
                    capacity_prediction
                    .days_until_90_percent
                ),
            },
            "100_percent": {
                "threshold_bytes": (
                    capacity_prediction
                    .threshold_100_bytes
                ),
                "date": (
                    capacity_prediction
                    .date_at_100_percent.isoformat()
                    if capacity_prediction
                    .date_at_100_percent
                    else None
                ),
                "days_until": (
                    capacity_prediction
                    .days_until_100_percent
                ),
            },
        }

        return base

    except HTTPException:
        raise

    except GoCoreAPIError as exc:

        raise HTTPException(
            status_code=503,
            detail=str(exc),
        )

    except ValueError as exc:

        raise HTTPException(
            status_code=422,
            detail=str(exc),
        )

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )


# =========================================================
# Recommendations
# =========================================================

@app.get("/recommendations")
def recommendations():

    try:

        provider = load_provider()

        result = run_recommendation_pipeline(
            provider=provider,
            total_capacity_bytes=(
                TOTAL_CAPACITY_BYTES
            ),
            forecast_days=CAPACITY_FORECAST_DAYS,
            stale_days=STALE_DAYS,
            root=MONITORED_ROOT,
        )

        capacity = result["capacity"]

        recommendations_data = [
            serialize_recommendation(
                recommendation
            )
            for recommendation
            in result["recommendations"]
        ]

        response = {
            "root": MONITORED_ROOT,

            "current": {
                "bytes": (
                    result["snapshots"][-1].total_bytes
                    if result["snapshots"]
                    else 0
                ),
                "files": (
                    result["snapshots"][-1].total_files
                    if result["snapshots"]
                    else 0
                ),
            },

            "growth": {
                "daily_growth_bytes": (
                    result["daily_growth_bytes"]
                ),
            },

            "storage_analysis": {
                "duplicate_waste_bytes": (
                    result["duplicate_bytes"]
                ),
                "stale_storage_bytes": (
                    result["stale_bytes"]
                ),
            },

            "forecast_status": (
                result["forecast_status"]
            ),

            "recommendations": (
                recommendations_data
            ),

            "recommendation_count": (
                len(recommendations_data)
            ),
        }

        if capacity is not None:

            response["capacity"] = {
                "total_bytes": (
                    capacity.total_capacity_bytes
                ),
                "current_bytes": (
                    capacity.current_bytes
                ),
                "utilization_percent": (
                    capacity.current_utilization_percent
                ),
                "90_percent": {
                    "date": (
                        capacity.date_at_90_percent
                        .isoformat()
                        if capacity.date_at_90_percent
                        else None
                    ),
                    "days_until": (
                        capacity.days_until_90_percent
                    ),
                },
                "100_percent": {
                    "date": (
                        capacity.date_at_100_percent
                        .isoformat()
                        if capacity.date_at_100_percent
                        else None
                    ),
                    "days_until": (
                        capacity.days_until_100_percent
                    ),
                },
            }

        else:

            response["capacity"] = None

        return response

    except GoCoreAPIError as exc:

        raise HTTPException(
            status_code=503,
            detail=str(exc),
        )

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )


# =========================================================
# Complete Analysis
# =========================================================

@app.get("/analysis")
def analysis():

    try:

        provider = load_provider()

        result = run_recommendation_pipeline(
            provider=provider,
            total_capacity_bytes=(
                TOTAL_CAPACITY_BYTES
            ),
            forecast_days=CAPACITY_FORECAST_DAYS,
            stale_days=STALE_DAYS,
            root=MONITORED_ROOT,
        )

        snapshots = result["snapshots"]

        if not snapshots:

            raise HTTPException(
                status_code=404,
                detail=(
                    "No snapshots found for monitored root: "
                    f"{MONITORED_ROOT}"
                ),
            )

        current = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )[-1]

        recommendations = [
            serialize_recommendation(
                recommendation
            )
            for recommendation
            in result["recommendations"]
        ]

        # -----------------------------------------
        # Forecast section
        # -----------------------------------------

        forecast_section = {
            "status": result["forecast_status"],
            "root": MONITORED_ROOT,
            "snapshots_available": len(snapshots),
            "selected_model": None,
            "history_days": 0.0,
            "models": None,
        }

        history_status = get_forecast_status(
            snapshots,
            root=MONITORED_ROOT,
        )

        forecast_section[
            "snapshots_required"
        ] = history_status.snapshots_required

        if len(snapshots) >= 2:

            ordered = sorted(
                snapshots,
                key=lambda snapshot: snapshot.scanned_at,
            )

            forecast_section[
                "history_days"
            ] = (
                ordered[-1].scanned_at
                - ordered[0].scanned_at
            ).total_seconds() / 86400

        if result["forecast"] is not None:

            forecast_result = result["forecast"]

            forecast_section[
                "selected_model"
            ] = forecast_result.model_name

            forecast_section["validation"] = {
                "mae_bytes": (
                    forecast_result.mae_bytes
                ),
                "rmse_bytes": (
                    forecast_result.rmse_bytes
                ),
            }

            forecast_section["models"] = {
                "selected": (
                    forecast_result.model_name
                ),
                "points": serialize_forecast_points(
                    forecast_result.forecast_points
                ),
            }

        else:

            forecast_section[
                "message"
            ] = history_status.message

        # -----------------------------------------
        # Capacity
        # -----------------------------------------

        capacity_section = None

        capacity = result["capacity"]

        if capacity is not None:

            capacity_section = {
                "total_bytes": (
                    capacity.total_capacity_bytes
                ),
                "total_gb": (
                    capacity.total_capacity_bytes
                    / (1024 ** 3)
                ),
                "current_bytes": (
                    capacity.current_bytes
                ),
                "utilization_percent": (
                    capacity.current_utilization_percent
                ),
                "90_percent": {
                    "threshold_bytes": (
                        capacity.threshold_90_bytes
                    ),
                    "date": (
                        capacity.date_at_90_percent
                        .isoformat()
                        if capacity.date_at_90_percent
                        else None
                    ),
                    "days_until": (
                        capacity.days_until_90_percent
                    ),
                },
                "100_percent": {
                    "threshold_bytes": (
                        capacity.threshold_100_bytes
                    ),
                    "date": (
                        capacity.date_at_100_percent
                        .isoformat()
                        if capacity.date_at_100_percent
                        else None
                    ),
                    "days_until": (
                        capacity.days_until_100_percent
                    ),
                },
            }

        # -----------------------------------------
        # Final unified response
        # -----------------------------------------

        return {
            "service": "storage-optimizer-ml",
            "version": "2.0.0",

            "root": MONITORED_ROOT,

            "current": {
                "date": current.scanned_at.isoformat(),
                "bytes": current.total_bytes,
                "files": current.total_files,
            },

            "growth": {
                "daily_growth_bytes": (
                    result["daily_growth_bytes"]
                ),
            },

            "storage_analysis": {
                "duplicate_waste_bytes": (
                    result["duplicate_bytes"]
                ),
                "stale_storage_bytes": (
                    result["stale_bytes"]
                ),
            },

            "categories": (
                result["category_stats"]
            ),

            "forecast": forecast_section,

            "capacity": capacity_section,

            "recommendations": recommendations,

            "recommendation_count": len(
                recommendations
            ),
        }

    except HTTPException:
        raise

    except GoCoreAPIError as exc:

        raise HTTPException(
            status_code=503,
            detail=str(exc),
        )

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )