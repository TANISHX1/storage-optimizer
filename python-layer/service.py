from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException

from core.mock_provider import MockDataProvider
from forecast.pipeline import (
    create_future_dates,
    forecast_linear,
    forecast_polynomial,
    forecast_holt_winters,
)
from forecast.capacity import calculate_capacity_prediction
from recommend.pipeline import run_recommendation_pipeline

# =========================================================
# Configuration
# =========================================================

DATA_PATH = Path("data/mock_data.json")

FORECAST_DAYS = 30


# =========================================================
# FastAPI
# =========================================================

app = FastAPI(
    title="Storage Optimizer ML Service",
    description=(
        "Python ML, storage forecasting and "
        "recommendation service"
    ),
    version="1.0.0",
)


# =========================================================
# Helpers
# =========================================================

def serialize_forecast_points(
    points,
) -> list[dict[str, Any]]:

    return [
        {
            "date": point.date.isoformat(),
            "predicted_bytes": point.predicted_bytes,
            "lower_bound": point.lower_bound,
            "upper_bound": point.upper_bound,
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

def load_provider() -> MockDataProvider:

    if not DATA_PATH.exists():
        raise FileNotFoundError(
            f"Dataset not found: {DATA_PATH}"
        )

    return MockDataProvider(DATA_PATH)


# =========================================================
# Health
# =========================================================

@app.get("/health")
def health():

    return {
        "service": "storage-optimizer-ml",
        "status": "healthy",
        "version": "1.0.0",
    }


# =========================================================
# Root
# =========================================================

@app.get("/")
def root():

    return {
        "service": "storage-optimizer-ml",
        "status": "running",
        "endpoints": [
    "/health",
    "/forecast",
    "/capacity",
    "/recommendations",
    "/analysis",
],
    }


# =========================================================
# Forecast
# =========================================================

@app.get("/forecast")
def forecast():

    try:

        provider = load_provider()

        snapshots = provider.get_snapshots()

        if len(snapshots) < 4:
            raise HTTPException(
                status_code=400,
                detail=(
                    "At least 4 snapshots are required "
                    "for forecasting."
                ),
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        current_snapshot = snapshots[-1]

        future_dates = create_future_dates(
            current_snapshot.scanned_at,
            FORECAST_DAYS,
        )

        linear_points = forecast_linear(
            snapshots,
            future_dates,
        )

        polynomial_points = forecast_polynomial(
            snapshots,
            future_dates,
        )

        holt_winters_points = forecast_holt_winters(
            snapshots,
            future_dates,
        )

        return {

            "current": {
                "date": current_snapshot.scanned_at.isoformat(),
                "bytes": current_snapshot.total_bytes,
                "files": current_snapshot.total_files,
            },

            "forecast_days": FORECAST_DAYS,

            "models": {

                "linear": {
                    "points": serialize_forecast_points(
                        linear_points
                    )
                },

                "polynomial": {
                    "degree": 2,
                    "points": serialize_forecast_points(
                        polynomial_points
                    )
                },

                "holt_winters": {
                    "points": serialize_forecast_points(
                        holt_winters_points
                    )
                },
            },
        }

    except HTTPException:
        raise

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )

@app.get("/capacity")
def capacity():

    try:
        provider = load_provider()

        snapshots = provider.get_snapshots()

        if len(snapshots) < 4:
            raise HTTPException(
                status_code=400,
                detail=(
                    "At least 4 snapshots are required "
                    "for capacity prediction."
                ),
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        current_snapshot = snapshots[-1]

        # Forecast 365 days so we have enough horizon
        # to detect 90% and 100% capacity.
        future_dates = create_future_dates(
            current_snapshot.scanned_at,
            365,
        )

        # Polynomial is currently our best-performing
        # model according to Step 6 evaluation.
        forecast_points = forecast_polynomial(
            snapshots,
            future_dates,
            degree=2,
        )

        # Dataset uses a 256 GB disk.
        total_capacity_bytes = 256 * 1024**3

        prediction = calculate_capacity_prediction(
            current_bytes=current_snapshot.total_bytes,
            current_date=current_snapshot.scanned_at,
            total_capacity_bytes=total_capacity_bytes,
            forecast_points=forecast_points,
        )

        return {
            "model": "Polynomial Regression",
            "model_degree": 2,

            "current": {
                "date": current_snapshot.scanned_at.isoformat(),
                "bytes": prediction.current_bytes,
                "utilization_percent": (
                    prediction.current_utilization_percent
                ),
            },

            "capacity": {
                "total_bytes": (
                    prediction.total_capacity_bytes
                ),
                "total_gb": (
                    prediction.total_capacity_bytes
                    / (1024**3)
                ),
            },

            "thresholds": {
                "90_percent": {
                    "bytes": prediction.threshold_90_bytes,
                    "date": (
                        prediction.date_at_90_percent.isoformat()
                        if prediction.date_at_90_percent
                        else None
                    ),
                    "days_until": (
                        prediction.days_until_90_percent
                    ),
                },

                "100_percent": {
                    "bytes": prediction.threshold_100_bytes,
                    "date": (
                        prediction.date_at_100_percent.isoformat()
                        if prediction.date_at_100_percent
                        else None
                    ),
                    "days_until": (
                        prediction.days_until_100_percent
                    ),
                },
            },
        }

    except HTTPException:
        raise

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )


@app.get("/recommendations")
def recommendations():

    try:

        result = run_recommendation_pipeline(
            data_path=DATA_PATH,
            total_capacity_bytes=256 * 1024**3,
            forecast_days=365,
        )

        capacity = result["capacity"]

        recommendations_data = [
            serialize_recommendation(
                recommendation
            )
            for recommendation
            in result["recommendations"]
        ]

        return {
            "current": {
                "bytes": capacity.current_bytes,
                "utilization_percent": (
                    capacity.current_utilization_percent
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

            "capacity": {
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
                        capacity.date_at_90_percent.isoformat()
                        if capacity.date_at_90_percent
                        else None
                    ),
                    "days_until": (
                        capacity.days_until_90_percent
                    ),
                },

                "100_percent": {
                    "date": (
                        capacity.date_at_100_percent.isoformat()
                        if capacity.date_at_100_percent
                        else None
                    ),
                    "days_until": (
                        capacity.days_until_100_percent
                    ),
                },
            },

            "recommendations": recommendations_data,

            "recommendation_count": (
                len(recommendations_data)
            ),
        }

    except HTTPException:
        raise

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        )

@app.get("/analysis")
def analysis():

    try:

        provider = load_provider()

        snapshots = provider.get_snapshots()

        if len(snapshots) < 4:
            raise HTTPException(
                status_code=400,
                detail=(
                    "At least 4 snapshots are required "
                    "for analysis."
                ),
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        current_snapshot = snapshots[-1]

        # ----------------------------------------
        # Forecast
        # ----------------------------------------

        future_dates = create_future_dates(
            current_snapshot.scanned_at,
            FORECAST_DAYS,
        )

        linear_points = forecast_linear(
            snapshots,
            future_dates,
        )

        polynomial_points = forecast_polynomial(
            snapshots,
            future_dates,
        )

        holt_winters_points = forecast_holt_winters(
            snapshots,
            future_dates,
        )

        # ----------------------------------------
        # Capacity
        # ----------------------------------------

        capacity_dates = create_future_dates(
            current_snapshot.scanned_at,
            365,
        )

        capacity_forecast = forecast_polynomial(
            snapshots,
            capacity_dates,
            degree=2,
        )

        total_capacity_bytes = 256 * 1024**3

        capacity_prediction = (
            calculate_capacity_prediction(
                current_bytes=current_snapshot.total_bytes,
                current_date=current_snapshot.scanned_at,
                total_capacity_bytes=total_capacity_bytes,
                forecast_points=capacity_forecast,
            )
        )

        # ----------------------------------------
        # Recommendations
        # ----------------------------------------

        recommendation_result = (
            run_recommendation_pipeline(
                data_path=DATA_PATH,
                total_capacity_bytes=total_capacity_bytes,
                forecast_days=365,
            )
        )

        recommendations = (
            recommendation_result["recommendations"]
        )

        # ----------------------------------------
        # Category statistics
        # ----------------------------------------

        category_stats = (
            provider.get_category_stats()
        )

        return {

            "current": {
                "date": (
                    current_snapshot.scanned_at.isoformat()
                ),
                "bytes": current_snapshot.total_bytes,
                "files": current_snapshot.total_files,
            },

            "forecast": {
                "days": FORECAST_DAYS,

                "models": {

                    "linear": {
                        "points": (
                            serialize_forecast_points(
                                linear_points
                            )
                        )
                    },

                    "polynomial": {
                        "degree": 2,
                        "points": (
                            serialize_forecast_points(
                                polynomial_points
                            )
                        )
                    },

                    "holt_winters": {
                        "points": (
                            serialize_forecast_points(
                                holt_winters_points
                            )
                        )
                    },
                },
            },

            "capacity": {
                "model": "Polynomial Regression",
                "model_degree": 2,

                "total_bytes": (
                    capacity_prediction.total_capacity_bytes
                ),

                "total_gb": (
                    capacity_prediction.total_capacity_bytes
                    / (1024**3)
                ),

                "utilization_percent": (
                    capacity_prediction
                    .current_utilization_percent
                ),

                "thresholds": {

                    "90_percent": {
                        "bytes": (
                            capacity_prediction
                            .threshold_90_bytes
                        ),

                        "date": (
                            capacity_prediction
                            .date_at_90_percent
                            .isoformat()
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
                        "bytes": (
                            capacity_prediction
                            .threshold_100_bytes
                        ),

                        "date": (
                            capacity_prediction
                            .date_at_100_percent
                            .isoformat()
                            if capacity_prediction
                            .date_at_100_percent
                            else None
                        ),

                        "days_until": (
                            capacity_prediction
                            .days_until_100_percent
                        ),
                    },
                },
            },

            "storage": {
                "duplicate_bytes": (
                    recommendation_result[
                        "duplicate_bytes"
                    ]
                ),

                "stale_bytes": (
                    recommendation_result[
                        "stale_bytes"
                    ]
                ),

                "daily_growth_bytes": (
                    recommendation_result[
                        "daily_growth_bytes"
                    ]
                ),
            },

            "categories": category_stats,

            "recommendations": [
                serialize_recommendation(
                    recommendation
                )
                for recommendation in recommendations
            ],
        }

    except HTTPException:
        raise

    except Exception as exc:

        raise HTTPException(
            status_code=500,
            detail=str(exc),
        ) 