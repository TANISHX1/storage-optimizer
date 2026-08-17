from dataclasses import dataclass
from datetime import datetime
from typing import List

from core.models import Snapshot


@dataclass
class GrowthMetrics:
    current_bytes: int
    current_files: int

    total_growth_bytes: int
    total_growth_percent: float

    daily_growth_rate_bytes: float
    weekly_growth_rate_bytes: float

    average_daily_growth_bytes: float
    growth_volatility_bytes: float

    snapshot_count: int


@dataclass
class GrowthPoint:
    timestamp: datetime
    bytes: int
    growth_bytes: int
    growth_rate_bytes_per_day: float


def calculate_growth_points(
    snapshots: List[Snapshot],
) -> List[GrowthPoint]:
    """
    Calculate growth between consecutive storage snapshots.
    """

    if len(snapshots) < 2:
        return []

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    points = []

    for previous, current in zip(
        snapshots,
        snapshots[1:],
    ):
        time_difference = (
            current.scanned_at - previous.scanned_at
        )

        days = time_difference.total_seconds() / 86400

        if days <= 0:
            continue

        growth_bytes = (
            current.total_bytes
            - previous.total_bytes
        )

        daily_growth = growth_bytes / days

        points.append(
            GrowthPoint(
                timestamp=current.scanned_at,
                bytes=current.total_bytes,
                growth_bytes=growth_bytes,
                growth_rate_bytes_per_day=daily_growth,
            )
        )

    return points


def calculate_growth_metrics(
    snapshots: List[Snapshot],
) -> GrowthMetrics:
    """
    Calculate overall storage growth metrics.
    """

    if not snapshots:
        raise ValueError(
            "At least one snapshot is required"
        )

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    latest = snapshots[-1]
    earliest = snapshots[0]

    total_growth_bytes = (
        latest.total_bytes
        - earliest.total_bytes
    )

    if earliest.total_bytes > 0:
        total_growth_percent = (
            total_growth_bytes
            / earliest.total_bytes
        ) * 100
    else:
        total_growth_percent = 0.0

    growth_points = calculate_growth_points(
        snapshots
    )

    if growth_points:
        daily_rates = [
            point.growth_rate_bytes_per_day
            for point in growth_points
        ]

        average_daily_growth = (
            sum(daily_rates)
            / len(daily_rates)
        )

        variance = sum(
            (
                rate - average_daily_growth
            ) ** 2
            for rate in daily_rates
        ) / len(daily_rates)

        growth_volatility = variance ** 0.5

    else:
        average_daily_growth = 0.0
        growth_volatility = 0.0

    return GrowthMetrics(
        current_bytes=latest.total_bytes,
        current_files=latest.total_files,
        total_growth_bytes=total_growth_bytes,
        total_growth_percent=total_growth_percent,
        daily_growth_rate_bytes=average_daily_growth,
        weekly_growth_rate_bytes=(
            average_daily_growth * 7
        ),
        average_daily_growth_bytes=average_daily_growth,
        growth_volatility_bytes=growth_volatility,
        snapshot_count=len(snapshots),
    )