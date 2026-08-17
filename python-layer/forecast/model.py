from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import List

import numpy as np
from sklearn.linear_model import LinearRegression
from sklearn.preprocessing import PolynomialFeatures
from statsmodels.tsa.holtwinters import ExponentialSmoothing

from core.models import Snapshot


@dataclass
class ForecastPoint:
    date: datetime
    predicted_bytes: float
    lower_bound: float | None = None
    upper_bound: float | None = None


class LinearForecastModel:
    """
    Linear regression model for storage growth.
    """

    def __init__(self):
        self.model = LinearRegression()
        self.start_time: datetime | None = None

    def fit(self, snapshots: List[Snapshot]):
        if len(snapshots) < 2:
            raise ValueError(
                "At least 2 snapshots are required."
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        self.start_time = snapshots[0].scanned_at

        x = np.array([
            [
                (
                    snapshot.scanned_at
                    - self.start_time
                ).total_seconds() / 86400
            ]
            for snapshot in snapshots
        ])

        y = np.array([
            snapshot.total_bytes
            for snapshot in snapshots
        ])

        self.model.fit(x, y)

        return self

    def predict(
        self,
        future_dates: List[datetime],
    ) -> np.ndarray:

        if self.start_time is None:
            raise RuntimeError(
                "Model must be fitted before prediction."
            )

        x = np.array([
            [
                (
                    date - self.start_time
                ).total_seconds() / 86400
            ]
            for date in future_dates
        ])

        predictions = self.model.predict(x)

        return np.maximum(predictions, 0)


class PolynomialForecastModel:
    """
    Polynomial regression model for non-linear storage growth.
    """

    def __init__(self, degree: int = 2):
        self.degree = degree
        self.model = LinearRegression()
        self.transformer = PolynomialFeatures(
            degree=degree
        )
        self.start_time: datetime | None = None

    def fit(self, snapshots: List[Snapshot]):
        if len(snapshots) < 3:
            raise ValueError(
                "At least 3 snapshots are required."
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        self.start_time = snapshots[0].scanned_at

        x = np.array([
            [
                (
                    snapshot.scanned_at
                    - self.start_time
                ).total_seconds() / 86400
            ]
            for snapshot in snapshots
        ])

        y = np.array([
            snapshot.total_bytes
            for snapshot in snapshots
        ])

        x_poly = self.transformer.fit_transform(x)

        self.model.fit(x_poly, y)

        return self

    def predict(
        self,
        future_dates: List[datetime],
    ) -> np.ndarray:

        if self.start_time is None:
            raise RuntimeError(
                "Model must be fitted before prediction."
            )

        x = np.array([
            [
                (
                    date - self.start_time
                ).total_seconds() / 86400
            ]
            for date in future_dates
        ])

        x_poly = self.transformer.transform(x)

        predictions = self.model.predict(x_poly)

        return np.maximum(predictions, 0)


class HoltWintersForecastModel:
    """
    Holt's exponential smoothing model.

    We initially use trend='add' without seasonality because
    storage snapshots do not yet provide enough history to
    reliably establish seasonal behavior.
    """

    def __init__(self):
        self.model = None
        self.forecast_model = None

    def fit(self, snapshots: List[Snapshot]):
        if len(snapshots) < 4:
            raise ValueError(
                "At least 4 snapshots are required."
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        values = np.array([
            snapshot.total_bytes
            for snapshot in snapshots
        ], dtype=float)

        self.forecast_model = ExponentialSmoothing(
            values,
            trend="add",
            damped_trend=True,
            initialization_method="estimated",
        ).fit()

        return self

    def predict(
        self,
        steps: int,
    ) -> np.ndarray:

        if self.forecast_model is None:
            raise RuntimeError(
                "Model must be fitted before prediction."
            )

        predictions = self.forecast_model.forecast(
            steps
        )

        return np.maximum(
            np.asarray(predictions),
            0,
        )