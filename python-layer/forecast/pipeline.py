from datetime import datetime, timedelta
from typing import List

import numpy as np

from core.models import Snapshot
from .model import (
    ForecastPoint,
    LinearForecastModel,
    PolynomialForecastModel,
    HoltWintersForecastModel,
)


def create_future_dates(
    start_date: datetime,
    days: int,
) -> List[datetime]:
    """
    Create one forecast point per day.
    """

    return [
        start_date + timedelta(days=i)
        for i in range(1, days + 1)
    ]


def forecast_linear(
    snapshots: List[Snapshot],
    future_dates: List[datetime],
) -> List[ForecastPoint]:

    model = LinearForecastModel()
    model.fit(snapshots)

    predictions = model.predict(
        future_dates
    )

    return [
        ForecastPoint(
            date=date,
            predicted_bytes=float(prediction),
        )
        for date, prediction
        in zip(future_dates, predictions)
    ]


def forecast_polynomial(
    snapshots: List[Snapshot],
    future_dates: List[datetime],
    degree: int = 2,
) -> List[ForecastPoint]:

    model = PolynomialForecastModel(
        degree=degree
    )

    model.fit(snapshots)

    predictions = model.predict(
        future_dates
    )

    return [
        ForecastPoint(
            date=date,
            predicted_bytes=float(prediction),
        )
        for date, prediction
        in zip(future_dates, predictions)
    ]


def forecast_holt_winters(
    snapshots: List[Snapshot],
    future_dates: List[datetime],
) -> List[ForecastPoint]:

    model = HoltWintersForecastModel()
    model.fit(snapshots)

    predictions = model.predict(
        len(future_dates)
    )

    return [
        ForecastPoint(
            date=date,
            predicted_bytes=float(prediction),
        )
        for date, prediction
        in zip(future_dates, predictions)
    ]