from core.provider import DataProvider

from forecast.forecast import (
    get_unified_forecast,
    ForecastNotReadyError,
)

from forecast.capacity import calculate_capacity_prediction

from .engine import generate_recommendations


def calculate_duplicate_waste(provider: DataProvider) -> int:
    clusters = provider.get_duplicates()
    return sum(cluster.wasted_bytes for cluster in clusters)


def calculate_stale_storage(provider: DataProvider, days: int = 30) -> int:
    stale_files = provider.get_stale_files(days=days)
    return sum(file.size_bytes for file in stale_files)


def calculate_daily_growth(snapshots) -> float:
    if len(snapshots) < 2:
        return 0.0

    snapshots = sorted(snapshots, key=lambda snapshot: snapshot.scanned_at)

    total_growth = snapshots[-1].total_bytes - snapshots[0].total_bytes
    total_days = (
        snapshots[-1].scanned_at - snapshots[0].scanned_at
    ).total_seconds() / 86400

    if total_days <= 0:
        return 0.0

    return total_growth / total_days


def run_recommendation_pipeline(
    provider: DataProvider,
    total_capacity_bytes: int,
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

    duplicate_bytes = calculate_duplicate_waste(provider)

    # ---------------------------------------------------------
    # 3. Stale storage
    # ---------------------------------------------------------

    stale_bytes = calculate_stale_storage(provider, days=stale_days)

    # ---------------------------------------------------------
    # 4. Growth
    # ---------------------------------------------------------

    daily_growth_bytes = calculate_daily_growth(snapshots)

    # ---------------------------------------------------------
    # 5. Forecast
    #    get_unified_forecast is the single source of truth shared
    #    with /forecast and /capacity — same cached ForecastResult
    #    for a given (root, latest snapshot), so this pipeline can
    #    never disagree with what those endpoints report.
    # ---------------------------------------------------------

    try:
        forecast_result = get_unified_forecast(provider, root=root)
        forecast_status = "ready"
    except ForecastNotReadyError as exc:
        forecast_result = None
        forecast_status = exc.status.status

    # ---------------------------------------------------------
    # 6. Capacity prediction
    # ---------------------------------------------------------

    capacity = None
    if forecast_result is not None and snapshots:
        latest_snapshot = max(snapshots, key=lambda s: s.scanned_at)

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
        utilization_percent = capacity.current_utilization_percent
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