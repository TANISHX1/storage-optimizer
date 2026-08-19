from dataclasses import dataclass
from datetime import datetime
from dataclasses import dataclass
from typing import List

from core.models import Snapshot
from core.provider import DataProvider

from .evaluation import (
    evaluate_models,
    select_best_model,
)
from .pipeline import (
    create_future_dates,
    forecast_linear,
    forecast_polynomial,
    forecast_holt_winters,
)
from .model import ForecastPoint
from .arima_model import (
    ARIMAForecastModel,
    ARIMAConfig,
)

MIN_FORECAST_SNAPSHOTS = 7
MIN_HISTORY_DAYS = 7


@dataclass
class ForecastStatus:
    status: str
    root: str | None
    snapshots_available: int
    snapshots_required: int
    message: str

@dataclass
class ForecastResult:
    model_name: str
    forecast_points: List[ForecastPoint]

    mae_bytes: float
    rmse_bytes: float

    mape_percent: float

    arima_order: tuple[int, int, int] | None = None

def get_history_span_days(
    snapshots: list[Snapshot],
) -> float:
    if len(snapshots) < 2:
        return 0.0

    ordered = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    return (
        ordered[-1].scanned_at
        - ordered[0].scanned_at
    ).total_seconds() / 86400

def get_forecast_status(
    snapshots: list[Snapshot],
    root: str | None = None,
) -> ForecastStatus:

    valid_snapshots = [
        snapshot
        for snapshot in snapshots
        if snapshot.total_files > 0
        and snapshot.total_bytes > 0
    ]

    valid_snapshots.sort(
        key=lambda snapshot: snapshot.scanned_at
    )

    if len(valid_snapshots) < MIN_FORECAST_SNAPSHOTS:
        return ForecastStatus(
            status="warming_up",
            root=root,
            snapshots_available=len(valid_snapshots),
            snapshots_required=MIN_FORECAST_SNAPSHOTS,
            message=(
                f"Only {history_days:.2f} days of history are available. "
                f"At least {MIN_HISTORY_DAYS} days are required "
                f"for reliable long-range forecasting."
            ),
        )

    history_days = get_history_span_days(
        valid_snapshots
    )

    if history_days < MIN_HISTORY_DAYS:
        return ForecastStatus(
            status="warming_up",
            root=root,
            snapshots_available=len(valid_snapshots),
            snapshots_required=MIN_FORECAST_SNAPSHOTS,
            message=(
                f"Only {history_days:.2f} days of history "
                f"are available. At least "
                f"{MIN_HISTORY_DAYS} days are required "
                f"for reliable long-range forecasting."
            ),
        )

    return ForecastStatus(
        status="ready",
        root=root,
        snapshots_available=len(valid_snapshots),
        snapshots_required=MIN_FORECAST_SNAPSHOTS,
        message=(
            "Enough historical data is available "
            "for forecasting."
        ),
    )

def is_forecast_valid(
    forecast_points,
    current_bytes: int,
) -> bool:

    if not forecast_points:
        return False

    for point in forecast_points:

        if point.predicted_bytes <= 0:
            return False

        if not (
            point.predicted_bytes
            == point.predicted_bytes
        ):
            return False

    return True

def forecast_storage_from_provider(
    provider: DataProvider,
    root: str | None = None,
    forecast_days: int = 30,
    validation_size: int = 3,
) -> ForecastResult:
    """
    Fetch live snapshots for one root and run the existing
    forecasting pipeline.
    """

    snapshots = provider.get_snapshots(root=root)

    valid_snapshots = [
        snapshot
        for snapshot in snapshots
        if (root is None or snapshot.root_path == root or snapshot.root_path.startswith(root))
        and snapshot.total_files > 0
        and snapshot.total_bytes > 0
    ]

    valid_snapshots.sort(
        key=lambda snapshot: snapshot.scanned_at
    )

    if len(valid_snapshots) < 2:
        target_name = root if root is not None else "all roots"
        raise ValueError(
            f"Not enough valid snapshots for {target_name!r}. "
            f"Required at least 2 snapshots, "
            f"received {len(valid_snapshots)}."
        )

    return forecast_storage(
        snapshots=valid_snapshots,
        forecast_days=forecast_days,
        validation_size=validation_size,
    )

def forecast_storage(
    snapshots: List[Snapshot],
    forecast_days: int = 30,
    validation_size: int = 30,
) -> ForecastResult:

    if len(snapshots) < validation_size + 10:
        raise ValueError(
            "At least 2 snapshots are required for forecasting."
        )

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    latest_date = snapshots[-1].scanned_at
    future_dates = create_future_dates(
        latest_date,
        forecast_days,
    )

    if len(snapshots) == 2:
        forecast_points = forecast_linear(snapshots, future_dates)
        return ForecastResult(
            model_name="Linear Regression",
            forecast_points=forecast_points,
            mae_bytes=0.0,
            rmse_bytes=0.0,
        )

    # ----------------------------------------
    # 1. Evaluate models
    # ----------------------------------------

    evaluations = evaluate_models(
        snapshots,
        test_size=validation_size,
    )

    # ----------------------------------------
    # 2. Select best valid model
    # ----------------------------------------

    best = select_best_model(
        evaluations
    )

    # ----------------------------------------
    # 3. Future dates
    # ----------------------------------------

    latest_date = snapshots[-1].scanned_at

    future_dates = create_future_dates(
        latest_date,
        forecast_days,
    )

    # ----------------------------------------
    # 4. Fit winner on ALL history
    # ----------------------------------------

    if best.model_name == "Linear Regression":

        forecast_points = forecast_linear(
            snapshots,
            future_dates,
        )

    elif best.model_name == (
        "Polynomial Regression (degree=2)"
    ):

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

    elif best.arima_order is not None:

        model = ARIMAForecastModel(
            ARIMAConfig(
                p=best.arima_order[0],
                d=best.arima_order[1],
                q=best.arima_order[2],
                confidence_level=0.95,
            )
        )

        model.fit(
            snapshots
        )

        forecast_points = model.predict(
            future_dates
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
        mape_percent=best.mape_percent,
        arima_order=best.arima_order,
    )