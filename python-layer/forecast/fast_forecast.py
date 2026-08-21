from __future__ import annotations

from core.models import Snapshot
from core.provider import DataProvider

from .evaluation import (
    evaluate_linear,
    evaluate_polynomial,
)
from .forecast import (
    ForecastNotReadyError,
    ForecastResult,
    VALIDATION_SIZE,
    filter_valid_snapshots,
    get_history_span_days,
    validate_forecast_history,
)
from .pipeline import (
    create_future_dates,
    forecast_linear,
    forecast_polynomial,
)

from .metrics import (
    calculate_mae,
    calculate_rmse,
    calculate_mape,
)

FAST_MIN_SNAPSHOTS = 5
FAST_MIN_HISTORY_DAYS = 2
FAST_FORECAST_DAYS = 30


def _validate_fast_history(
    snapshots: list[Snapshot],
    root: str | None = None,
    validation_size: int = 3,
):
    valid = filter_valid_snapshots(
        snapshots,
        root=root,
    )

    history_days = get_history_span_days(valid)

    required = max(
        FAST_MIN_SNAPSHOTS,
        validation_size + 1,
    )

    from .forecast import HistoryValidation

    if len(valid) < required:
        return HistoryValidation(
            status="warming_up",
            root=root,
            snapshots_available=len(valid),
            snapshots_required=required,
            history_days=history_days,
            message=(
                f"Only {len(valid)} snapshots available. "
                f"Fast forecasting requires at least "
                f"{required} snapshots."
            ),
        )

    if history_days < FAST_MIN_HISTORY_DAYS:
        return HistoryValidation(
            status="warming_up",
            root=root,
            snapshots_available=len(valid),
            snapshots_required=required,
            history_days=history_days,
            message=(
                f"Only {history_days:.2f} days of history available. "
                f"Fast forecasting requires at least "
                f"{FAST_MIN_HISTORY_DAYS} days."
            ),
        )

    return HistoryValidation(
        status="ready",
        root=root,
        snapshots_available=len(valid),
        snapshots_required=required,
        history_days=history_days,
        message="Enough history is available for fast forecasting.",
    )


def run_fast_forecast(
    snapshots: list[Snapshot],
    forecast_days: int = FAST_FORECAST_DAYS,
    validation_size: int = 3,
    root: str | None = None,
) -> ForecastResult:

    valid_snapshots = filter_valid_snapshots(
        snapshots,
        root=root,
    )

    validation = _validate_fast_history(
        valid_snapshots,
        root=root,
        validation_size=validation_size,
    )

    if validation.status != "ready":
        from .forecast import ForecastStatus

        raise ForecastNotReadyError(
            ForecastStatus(
                status=validation.status,
                root=validation.root,
                snapshots_available=validation.snapshots_available,
                snapshots_required=validation.snapshots_required,
                history_days=validation.history_days,
                message=validation.message,
            )
        )

    train = valid_snapshots[:-validation_size]
    test = valid_snapshots[-validation_size:]

    linear_result = evaluate_linear(
        train,
        test,
    )

    polynomial_result = evaluate_polynomial(
        train,
        test,
        degree=2,
    )

    evaluations = [
        linear_result,
        polynomial_result,
    ]

    best = min(
        evaluations,
        key=lambda result: result.mae_bytes,
    )

    future_dates = create_future_dates(
        valid_snapshots[-1].scanned_at,
        forecast_days,
    )

    if best.model_name == "Linear Regression":
        points = forecast_linear(
            valid_snapshots,
            future_dates,
        )

    elif best.model_name == (
        "Polynomial Regression (degree=2)"
    ):
        points = forecast_polynomial(
            valid_snapshots,
            future_dates,
            degree=2,
        )

    else:
        raise RuntimeError(
            f"Unexpected fast model: {best.model_name}"
        )

    return ForecastResult(
        model_name=best.model_name,
        forecast_points=points,
        mae_bytes=best.mae_bytes,
        rmse_bytes=best.rmse_bytes,
        mape_percent=getattr(
            best,
            "mape_percent",
            0.0,
        ),
        snapshots_used=len(valid_snapshots),
        history_days=get_history_span_days(
            valid_snapshots,
        ),
        snapshots_required=validation.snapshots_required,
        arima_order=None,
    )


def run_fast_forecast_from_provider(
    provider: DataProvider,
    root: str | None = None,
    forecast_days: int = FAST_FORECAST_DAYS,
    validation_size: int = 3,
) -> ForecastResult:

    snapshots = provider.get_snapshots(
        root=root,
        limit=1000,
    )

    return run_fast_forecast(
        snapshots=snapshots,
        forecast_days=forecast_days,
        validation_size=validation_size,
        root=root,
    )