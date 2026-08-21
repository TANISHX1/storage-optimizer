from __future__ import annotations

from core.models import Snapshot
from core.provider import DataProvider

from .forecast import (
    ForecastNotReadyError,
    ForecastResult,
    VALIDATION_SIZE,
    CAPACITY_FORECAST_DAYS,
    filter_valid_snapshots,
    validate_forecast_history,
    forecast_storage,
)


SLOW_FORECAST_DAYS = CAPACITY_FORECAST_DAYS


def run_slow_forecast(
    snapshots: list[Snapshot],
    forecast_days: int = SLOW_FORECAST_DAYS,
    validation_size: int = VALIDATION_SIZE,
    root: str | None = None,
) -> ForecastResult:

    valid_snapshots = filter_valid_snapshots(
        snapshots,
        root=root,
    )

    validation = validate_forecast_history(
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

    return forecast_storage(
        snapshots=valid_snapshots,
        forecast_days=forecast_days,
        validation_size=validation_size,
    )


def run_slow_forecast_from_provider(
    provider: DataProvider,
    root: str | None = None,
    forecast_days: int = SLOW_FORECAST_DAYS,
    validation_size: int = VALIDATION_SIZE,
) -> ForecastResult:

    snapshots = provider.get_snapshots(
        root=root,
        limit=1000,
    )

    return run_slow_forecast(
        snapshots=snapshots,
        forecast_days=forecast_days,
        validation_size=validation_size,
        root=root,
    )