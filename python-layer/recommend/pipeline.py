from core.provider import DataProvider

from forecast.forecast import (
    forecast_storage,
    forecast_storage_from_provider,
    get_forecast_status,
)

from forecast.capacity import calculate_capacity_prediction

from .engine import generate_recommendations


def calculate_duplicate_waste(provider: DataProvider) -> int:
    clusters = provider.get_duplicates()

    return sum(
        cluster.wasted_bytes
        for cluster in clusters
    )


def calculate_stale_storage(
    provider: DataProvider,
    days: int = 30,
) -> int:
    stale_files = provider.get_stale_files(days=days)

    return sum(
        file.size_bytes
        for file in stale_files
    )


def calculate_daily_growth(snapshots) -> float:
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
    provider: DataProvider,
    total_capacity_bytes: int,
    forecast_days: int = 365,
    stale_days: int = 30,
    root: str | None = None,
):
    """
    Run the recommendation pipeline using any DataProvider.

    In production this will be GoCoreProvider.
    MockDataProvider remains available for offline tests.
    """

    # ---------------------------------------------------------
    # 1. Fetch live/provider data
    # ---------------------------------------------------------

    snapshots = provider.get_snapshots(root=root)
    category_stats = provider.get_category_stats()

    # ---------------------------------------------------------
    # 2. Duplicate analysis
    # ---------------------------------------------------------

    duplicate_bytes = calculate_duplicate_waste(
        provider
    )

    # ---------------------------------------------------------
    # 3. Stale storage
    # ---------------------------------------------------------

    stale_bytes = calculate_stale_storage(
        provider,
        days=stale_days,
    )

    # ---------------------------------------------------------
    # 4. Growth
    # ---------------------------------------------------------

    daily_growth_bytes = calculate_daily_growth(
        snapshots
    )

    # ---------------------------------------------------------
    # 5. Forecast
    # ---------------------------------------------------------

    forecast_result = None
    capacity = None

    forecast_status = "warming_up"

    if root is not None:
        root_snapshots = provider.get_snapshots(
            root=root
        )

        history_status = get_forecast_status(
            root_snapshots,
            root=root,
        )

        forecast_status = history_status.status

        if len(root_snapshots) >= 2:
            try:
                forecast_result = forecast_storage_from_provider(
                    provider=provider,
                    root=root,
                    forecast_days=forecast_days,
                    validation_size=3,
                )
            except ValueError:
                forecast_result = None
                forecast_status = "warming_up"

    else:
        history_status = get_forecast_status(
            snapshots,
            root=None,
        )

        forecast_status = history_status.status

        if len(snapshots) >= 2:
            try:
                forecast_result = forecast_storage(
                    snapshots=snapshots,
                    forecast_days=forecast_days,
                    validation_size=3,
                )
            except ValueError:
                forecast_result = None
                forecast_status = "warming_up"

    # ---------------------------------------------------------
    # 6. Capacity prediction
    # ---------------------------------------------------------

    if forecast_result is not None and snapshots:

        latest_snapshot = snapshots[-1]

        capacity = calculate_capacity_prediction(
            current_bytes=latest_snapshot.total_bytes,
            current_date=latest_snapshot.scanned_at,
            total_capacity_bytes=total_capacity_bytes,
            forecast_points=forecast_result.forecast_points,
        )

    # ---------------------------------------------------------
    # 7. Recommendations
    # ---------------------------------------------------------

    utilization_percent = 0.0
    days_until_90 = None
    days_until_100 = None

    if capacity is not None:
        utilization_percent = (
            capacity.current_utilization_percent
        )
        days_until_90 = capacity.days_until_90_percent
        days_until_100 = capacity.days_until_100_percent

    recommendations = generate_recommendations(
        duplicate_bytes=duplicate_bytes,
        stale_bytes=stale_bytes,
        stale_days=stale_days,
        daily_growth_bytes=daily_growth_bytes,
        utilization_percent=utilization_percent,
        days_until_90=days_until_90,
        days_until_100=days_until_100,
        category_stats=category_stats,
    )

    return {
        "forecast": forecast_result,
        "forecast_status": forecast_status,
        "capacity": capacity,
        "recommendations": recommendations,
        "duplicate_bytes": duplicate_bytes,
        "stale_bytes": stale_bytes,
        "daily_growth_bytes": daily_growth_bytes,
        "category_stats": category_stats,
        "snapshots": snapshots,
    }