from dataclasses import dataclass
from datetime import datetime
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


# =========================================================
# Forecasting configuration
# =========================================================

MIN_FORECAST_SNAPSHOTS = 40
MIN_HISTORY_DAYS = 14
VALIDATION_SIZE = 30

# Longest horizon required by any consumer.
# /forecast can display only the first 30 points,
# while /capacity may inspect the full 365-day horizon.
CAPACITY_FORECAST_DAYS = 365


# =========================================================
# Exceptions
# =========================================================

class ForecastNotReadyError(Exception):
    """Raised when there isn't enough history to forecast yet."""

    def __init__(self, status: "ForecastStatus"):
        self.status = status
        super().__init__(status.message)


# =========================================================
# Result / status models
# =========================================================

@dataclass
class HistoryValidation:
    status: str
    root: str | None
    snapshots_available: int
    snapshots_required: int
    history_days: float
    message: str


@dataclass
class ForecastStatus:
    status: str
    root: str | None
    snapshots_available: int
    snapshots_required: int
    history_days: float
    message: str


@dataclass
class ForecastResult:
    model_name: str
    forecast_points: List[ForecastPoint]

    mae_bytes: float
    rmse_bytes: float
    mape_percent: float

    snapshots_used: int
    history_days: float

    snapshots_required: int = MIN_FORECAST_SNAPSHOTS

    arima_order: tuple[int, int, int] | None = None


# =========================================================
# In-process forecast cache
# =========================================================

# Key:
#   root -> latest snapshot timestamp + ForecastResult
#
# A new scan changes the latest snapshot timestamp and therefore
# automatically invalidates the cached result for that root.
#
# This cache is intentionally single-process. That is appropriate
# for the current local FastAPI deployment.
_forecast_cache: dict[
    str | None,
    tuple[datetime, ForecastResult],
] = {}


# =========================================================
# History utilities
# =========================================================

def get_history_span_days(
    snapshots: list[Snapshot],
) -> float:
    """
    Return the elapsed time between the earliest and latest
    snapshot in days.
    """

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


def filter_valid_snapshots(
    snapshots: list[Snapshot],
    root: str | None = None,
) -> list[Snapshot]:
    """
    Filter invalid snapshots and optionally restrict them to a root.
    """

    valid_snapshots = [
        snapshot
        for snapshot in snapshots
        if snapshot.total_files > 0
        and snapshot.total_bytes > 0
        and (
            root is None
            or snapshot.root_path == root
            or snapshot.root_path.startswith(root + "/")
        )
    ]

    valid_snapshots.sort(
        key=lambda snapshot: snapshot.scanned_at,
    )

    return valid_snapshots


# =========================================================
# Single readiness gate
# =========================================================

def validate_forecast_history(
    snapshots: list[Snapshot],
    root: str | None = None,
    validation_size: int = VALIDATION_SIZE,
) -> HistoryValidation:
    """
    The single readiness gate.

    Forecasting is ready only when:
      1. enough valid snapshots exist,
      2. enough historical time has elapsed,
      3. enough snapshots exist to support the validation holdout.
    """

    valid_snapshots = filter_valid_snapshots(
        snapshots,
        root=root,
    )

    history_days = get_history_span_days(
        valid_snapshots
    )

    required_snapshots = max(
        MIN_FORECAST_SNAPSHOTS,
        validation_size + 1,
    )

    if len(valid_snapshots) < required_snapshots:
        return HistoryValidation(
            status="warming_up",
            root=root,
            snapshots_available=len(valid_snapshots),
            snapshots_required=required_snapshots,
            history_days=history_days,
            message=(
                f"Only {len(valid_snapshots)} snapshots available. "
                f"At least {required_snapshots} are required."
            ),
        )

    if history_days < MIN_HISTORY_DAYS:
        return HistoryValidation(
            status="warming_up",
            root=root,
            snapshots_available=len(valid_snapshots),
            snapshots_required=required_snapshots,
            history_days=history_days,
            message=(
                f"Only {history_days:.2f} days of history available. "
                f"At least {MIN_HISTORY_DAYS} days are required."
            ),
        )

    return HistoryValidation(
        status="ready",
        root=root,
        snapshots_available=len(valid_snapshots),
        snapshots_required=required_snapshots,
        history_days=history_days,
        message=(
            "Enough historical data is available for forecasting."
        ),
    )


# =========================================================
# Public forecast status
# =========================================================

def get_forecast_status(
    snapshots: list[Snapshot],
    root: str | None = None,
) -> ForecastStatus:
    """
    Convert the canonical HistoryValidation result into the
    public ForecastStatus model.
    """

    validation = validate_forecast_history(
        snapshots,
        root=root,
        validation_size=VALIDATION_SIZE,
    )

    return ForecastStatus(
        status=validation.status,
        root=validation.root,
        snapshots_available=(
            validation.snapshots_available
        ),
        snapshots_required=(
            validation.snapshots_required
        ),
        history_days=validation.history_days,
        message=validation.message,
    )


# =========================================================
# Forecast validation
# =========================================================

def is_forecast_valid(
    forecast_points,
    current_bytes: int,
) -> bool:
    """
    Verify that generated forecast points contain positive,
    finite predictions.

    current_bytes is retained for API compatibility and future
    validation enhancements.
    """

    _ = current_bytes

    if not forecast_points:
        return False

    for point in forecast_points:

        if point.predicted_bytes <= 0:
            return False

        if point.predicted_bytes != point.predicted_bytes:
            return False

    return True


# =========================================================
# Provider-based forecasting
# =========================================================

def forecast_storage_from_provider(
    provider: DataProvider,
    root: str | None = None,
    forecast_days: int = CAPACITY_FORECAST_DAYS,
    validation_size: int = VALIDATION_SIZE,
) -> ForecastResult:
    """
    Fetch live/provider snapshots, validate readiness, and run
    the forecasting pipeline.

    Raises ForecastNotReadyError when history is insufficient.
    """

    snapshots = provider.get_snapshots(
        root=root,
        limit=1000,
    )

    valid_snapshots = filter_valid_snapshots(
        snapshots,
        root=root,
    )

    status = validate_forecast_history(
        valid_snapshots,
        root=root,
        validation_size=validation_size,
    )

    if status.status != "ready":
        raise ForecastNotReadyError(
            ForecastStatus(
                status=status.status,
                root=status.root,
                snapshots_available=status.snapshots_available,
                snapshots_required=status.snapshots_required,
                history_days=status.history_days,
                message=status.message,
            )
        )

    return forecast_storage(
        snapshots=valid_snapshots,
        forecast_days=forecast_days,
        validation_size=validation_size,
    )


# =========================================================
# Core forecast execution
# =========================================================

def forecast_storage(
    snapshots: List[Snapshot],
    forecast_days: int = CAPACITY_FORECAST_DAYS,
    validation_size: int = VALIDATION_SIZE,
) -> ForecastResult:
    """
    Fit/evaluate all candidate models and generate the future
    forecast using the best valid model.

    Callers should normally pass snapshots that have already
    passed validate_forecast_history().
    """

    if len(snapshots) < MIN_FORECAST_SNAPSHOTS:
        raise ValueError(
            f"At least {MIN_FORECAST_SNAPSHOTS} snapshots are required "
            f"for forecasting, received {len(snapshots)}."
        )

    if forecast_days <= 0:
        raise ValueError(
            "forecast_days must be greater than zero."
        )

    if validation_size <= 0:
        raise ValueError(
            "validation_size must be greater than zero."
        )

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    if len(snapshots) <= validation_size:
        raise ValueError(
            "Not enough snapshots for the requested validation size."
        )

    # -----------------------------------------------------
    # 1. Evaluate candidate models
    # -----------------------------------------------------

    evaluations = evaluate_models(
        snapshots,
        test_size=validation_size,
    )

    # -----------------------------------------------------
    # 2. Select best valid model
    # -----------------------------------------------------

    best = select_best_model(
        evaluations
    )

    # -----------------------------------------------------
    # 3. Create future dates
    # -----------------------------------------------------

    latest_date = snapshots[-1].scanned_at

    future_dates = create_future_dates(
        latest_date,
        forecast_days,
    )

    # -----------------------------------------------------
    # 4. Fit selected model on ALL historical data
    # -----------------------------------------------------

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

    if not is_forecast_valid(
        forecast_points,
        snapshots[-1].total_bytes,
    ):
        raise RuntimeError(
            "Generated forecast contains invalid predictions."
        )

    return ForecastResult(
        model_name=best.model_name,
        forecast_points=forecast_points,
        mae_bytes=best.mae_bytes,
        rmse_bytes=best.rmse_bytes,
        mape_percent=best.mape_percent,
        snapshots_used=len(snapshots),
        history_days=get_history_span_days(
            snapshots
        ),
        snapshots_required=(
            max(
                MIN_FORECAST_SNAPSHOTS,
                validation_size + 1,
            )
        ),
        arima_order=best.arima_order,
    )


# =========================================================
# Unified forecast
# =========================================================

def get_unified_forecast(
    provider: DataProvider,
    root: str | None = None,
) -> ForecastResult:
    """
    Single source of truth for forecast computation.

    The system always computes the longest forecast horizon
    required by any consumer.

    /forecast:
        displays the first 30 points.

    /capacity:
        uses the complete 365-day forecast.

    /recommendations:
        reuses the same cached ForecastResult.

    A new latest snapshot automatically invalidates the cache.
    """

    snapshots = provider.get_snapshots(
        root=root,
        limit=1000,
    )

    valid_snapshots = filter_valid_snapshots(
        snapshots,
        root=root,
    )

    # -----------------------------------------------------
    # No usable snapshots
    # -----------------------------------------------------

    if not valid_snapshots:

        raise ForecastNotReadyError(
            get_forecast_status(
                [],
                root=root,
            )
        )

    # -----------------------------------------------------
    # Determine cache key/version
    # -----------------------------------------------------

    latest_timestamp = max(
        snapshot.scanned_at
        for snapshot in valid_snapshots
    )

    cached = _forecast_cache.get(root)

    if (
        cached is not None
        and cached[0] == latest_timestamp
    ):
        return cached[1]

    # -----------------------------------------------------
    # Run canonical provider-based forecast
    # -----------------------------------------------------

    result = forecast_storage_from_provider(
        provider=provider,
        root=root,
        forecast_days=CAPACITY_FORECAST_DAYS,
        validation_size=VALIDATION_SIZE,
    )

    # -----------------------------------------------------
    # Cache result
    # -----------------------------------------------------

    _forecast_cache[root] = (
        latest_timestamp,
        result,
    )

    return result