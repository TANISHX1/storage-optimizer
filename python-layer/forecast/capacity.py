from dataclasses import dataclass
from datetime import datetime
from typing import List

from .model import ForecastPoint


@dataclass
class CapacityPrediction:
    total_capacity_bytes: int

    current_bytes: int
    current_utilization_percent: float

    threshold_90_bytes: int
    threshold_100_bytes: int

    date_at_90_percent: datetime | None
    date_at_100_percent: datetime | None

    days_until_90_percent: float | None
    days_until_100_percent: float | None


def find_threshold_date(
    forecast_points: List[ForecastPoint],
    threshold_bytes: int,
) -> datetime | None:
    """
    Find the first forecast date where predicted storage
    reaches or exceeds the requested threshold.
    """

    for point in forecast_points:

        if point.predicted_bytes >= threshold_bytes:
            return point.date

    return None


def calculate_days_until(
    current_date: datetime,
    target_date: datetime | None,
) -> float | None:

    if target_date is None:
        return None

    difference = (
        target_date - current_date
    ).total_seconds()

    return difference / 86400


def calculate_capacity_prediction(
    current_bytes: int,
    current_date: datetime,
    total_capacity_bytes: int,
    forecast_points: List[ForecastPoint],
) -> CapacityPrediction:

    if total_capacity_bytes <= 0:
        raise ValueError(
            "Total capacity must be greater than zero."
        )

    if current_bytes < 0:
        raise ValueError(
            "Current storage cannot be negative."
        )

    threshold_90 = int(
        total_capacity_bytes * 0.90
    )

    threshold_100 = total_capacity_bytes

    date_90 = find_threshold_date(
        forecast_points,
        threshold_90,
    )

    date_100 = find_threshold_date(
        forecast_points,
        threshold_100,
    )

    utilization = (
        current_bytes
        / total_capacity_bytes
    ) * 100

    return CapacityPrediction(
        total_capacity_bytes=total_capacity_bytes,

        current_bytes=current_bytes,

        current_utilization_percent=utilization,

        threshold_90_bytes=threshold_90,

        threshold_100_bytes=threshold_100,

        date_at_90_percent=date_90,

        date_at_100_percent=date_100,

        days_until_90_percent=calculate_days_until(
            current_date,
            date_90,
        ),

        days_until_100_percent=calculate_days_until(
            current_date,
            date_100,
        ),
    )