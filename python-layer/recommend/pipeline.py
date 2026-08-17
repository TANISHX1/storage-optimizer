from pathlib import Path
from typing import Any

from core.mock_provider import MockDataProvider

from forecast.forecast import forecast_storage
from forecast.capacity import calculate_capacity_prediction

from .engine import generate_recommendations
from forecast.forecast import forecast_storage

def calculate_duplicate_waste(provider):
    """
    Calculate total reclaimable space from duplicate files.
    """

    clusters = provider.get_duplicates()

    return sum(
        cluster.wasted_bytes
        for cluster in clusters
    )


def calculate_stale_storage(provider):
    """
    Calculate total storage occupied by stale files.
    """

    stale_files = provider.get_stale_files()

    return sum(
        file.size_bytes
        for file in stale_files
    )


def calculate_daily_growth(snapshots):
    """
    Calculate average daily storage growth from snapshots.
    """

    if len(snapshots) < 2:
        return 0.0

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    total_growth = (
        snapshots[-1].total_bytes
        - snapshots[0].total_bytes
    )

    total_days = (
        snapshots[-1].scanned_at
        - snapshots[0].scanned_at
    ).total_seconds() / 86400

    if total_days <= 0:
        return 0.0

    return total_growth / total_days


def run_recommendation_pipeline(
    data_path: str | Path,
    total_capacity_bytes: int,
    forecast_days: int = 365,
):
    """
    Run the complete recommendation pipeline.
    """

    provider = MockDataProvider(
        data_path
    )

    # ----------------------------------------
    # 1. Fetch data
    # ----------------------------------------

    snapshots = provider.get_snapshots()
    category_stats = provider.get_category_stats()

    # ----------------------------------------
    # 2. Calculate duplicate waste
    # ----------------------------------------

    duplicate_bytes = calculate_duplicate_waste(
        provider
    )

    # ----------------------------------------
    # 3. Calculate stale storage
    # ----------------------------------------

    stale_bytes = calculate_stale_storage(
        provider
    )

    # ----------------------------------------
    # 4. Calculate growth
    # ----------------------------------------

    daily_growth_bytes = calculate_daily_growth(
        snapshots
    )

    # ----------------------------------------
    # 5. Generate forecast
    # ----------------------------------------

    forecast_result = forecast_storage(
        snapshots,
        forecast_days=forecast_days,
        validation_size=3,
    )

    # ----------------------------------------
    # 6. Calculate capacity prediction
    # ----------------------------------------

    latest_snapshot = snapshots[-1]

    capacity = calculate_capacity_prediction(
        current_bytes=latest_snapshot.total_bytes,
        current_date=latest_snapshot.scanned_at,
        total_capacity_bytes=total_capacity_bytes,
        forecast_points=forecast_result.forecast_points,
    )

    # ----------------------------------------
    # 7. Generate recommendations
    # ----------------------------------------

    recommendations = generate_recommendations(

        duplicate_bytes=duplicate_bytes,

        stale_bytes=stale_bytes,

        stale_days=30,

        daily_growth_bytes=daily_growth_bytes,

        utilization_percent=(
            capacity.current_utilization_percent
        ),

        days_until_90=(
            capacity.days_until_90_percent
        ),

        days_until_100=(
            capacity.days_until_100_percent
        ),

        category_stats=category_stats,
    )

    return {
        "forecast": forecast_result,

        "capacity": capacity,

        "recommendations": recommendations,

        "duplicate_bytes": duplicate_bytes,

        "stale_bytes": stale_bytes,

        "daily_growth_bytes": daily_growth_bytes,
    }

