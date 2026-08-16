from pathlib import Path

from core.mock_provider import MockDataProvider

from .forecast import forecast_storage
from .capacity import calculate_capacity_prediction


def run_capacity_prediction(
    total_capacity_bytes: int,
    forecast_days: int = 365,
):
    """
    Generate storage forecasts and determine when the
    disk reaches 90% and 100% capacity.
    """

    provider = MockDataProvider(
        Path("data/mock_data.json")
    )

    snapshots = provider.get_snapshots()

    forecast_result = forecast_storage(
        snapshots,
        forecast_days=forecast_days,
        validation_size=3,
    )

    current_snapshot = snapshots[-1]

    capacity_prediction = (
        calculate_capacity_prediction(
            current_bytes=current_snapshot.total_bytes,
            current_date=current_snapshot.scanned_at,
            total_capacity_bytes=total_capacity_bytes,
            forecast_points=forecast_result.forecast_points,
        )
    )

    return forecast_result, capacity_prediction