from dataclasses import dataclass
from datetime import datetime
from typing import List

from core.models import Snapshot

from .evaluation import evaluate_models
from .pipeline import (
    create_future_dates,
    forecast_linear,
    forecast_polynomial,
    forecast_holt_winters,
)
from .model import ForecastPoint


@dataclass
class ForecastResult:
    model_name: str
    forecast_points: List[ForecastPoint]
    mae_bytes: float
    rmse_bytes: float


def forecast_storage(
    snapshots: List[Snapshot],
    forecast_days: int = 30,
    validation_size: int = 3,
) -> ForecastResult:
    """
    Select the best forecasting model using chronological
    validation and generate future storage predictions.
    """

    if len(snapshots) < validation_size + 3:
        raise ValueError(
            "Not enough snapshots for forecasting."
        )

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    # ----------------------------------------
    # 1. Evaluate candidate models
    # ----------------------------------------

    evaluations = evaluate_models(
        snapshots,
        test_size=validation_size,
    )

    # ----------------------------------------
    # 2. Select model with lowest MAE
    # ----------------------------------------

    best = min(
        evaluations,
        key=lambda result: result.mae_bytes,
    )

    # ----------------------------------------
    # 3. Generate future dates
    # ----------------------------------------

    latest_date = snapshots[-1].scanned_at

    future_dates = create_future_dates(
        latest_date,
        forecast_days,
    )

    # ----------------------------------------
    # 4. Train selected model on ALL data
    # ----------------------------------------

    if best.model_name == "Linear Regression":

        forecast_points = forecast_linear(
            snapshots,
            future_dates,
        )

    elif best.model_name == "Polynomial Regression (degree=2)":

        forecast_points = forecast_polynomial(
            snapshots,
            future_dates,
            degree=2,
        )

    elif best.model_name == "Holt-Winters":

        forecast_points = forecast_holt_winters(
            snapshots,
            future_dates,
        )

    else:
        raise RuntimeError(
            f"Unknown model: {best.model_name}"
        )

    return ForecastResult(
        model_name=best.model_name,
        forecast_points=forecast_points,
        mae_bytes=best.mae_bytes,
        rmse_bytes=best.rmse_bytes,
    )