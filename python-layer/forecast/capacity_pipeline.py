# In forecast/capacity_pipeline.py

from core.provider import DataProvider
from .forecast import forecast_storage
from .capacity import calculate_capacity_prediction, CapacityPrediction
from .forecast import ForecastResult


def run_capacity_prediction(
    provider: DataProvider,
    total_capacity_bytes: int,
    forecast_days: int = 365,
    root: str | None = None,
    validation_size: int = 3,
) -> tuple[ForecastResult, CapacityPrediction]:
    snapshots = provider.get_snapshots(root=root)

    if not snapshots:
        raise ValueError("No snapshots retrieved from provider.")

    # Guarantee sort order
    snapshots = sorted(snapshots, key=lambda s: s.scanned_at)

    forecast_result = forecast_storage(
        snapshots,
        forecast_days=forecast_days,
        validation_size=validation_size,
    )

    current_snapshot = snapshots[-1]

    capacity_prediction = calculate_capacity_prediction(
        current_bytes=current_snapshot.total_bytes,
        current_date=current_snapshot.scanned_at,
        total_capacity_bytes=total_capacity_bytes,
        forecast_points=forecast_result.forecast_points,
    )

    return forecast_result, capacity_prediction