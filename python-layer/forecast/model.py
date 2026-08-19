from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import List

import numpy as np

try:
    from sklearn.linear_model import LinearRegression
    from sklearn.preprocessing import PolynomialFeatures
    _HAS_SKLEARN = True
except ImportError:
    _HAS_SKLEARN = False

try:
    from statsmodels.tsa.holtwinters import ExponentialSmoothing
    _HAS_STATSMODELS = True
except ImportError:
    _HAS_STATSMODELS = False

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
        self.start_time: datetime | None = None
        self.coeffs: np.ndarray | None = None
        if _HAS_SKLEARN:
            self.model = LinearRegression()
        else:
            self.model = None

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
            (
                snapshot.scanned_at
                - self.start_time
            ).total_seconds() / 86400
            for snapshot in snapshots
        ], dtype=float)

        y = np.array([
            snapshot.total_bytes
            for snapshot in snapshots
        ], dtype=float)

        if self.model is not None:
            self.model.fit(x.reshape(-1, 1), y)
        else:
            self.coeffs = np.polyfit(x, y, 1)

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
            (
                date - self.start_time
            ).total_seconds() / 86400
            for date in future_dates
        ], dtype=float)

        if self.model is not None:
            predictions = self.model.predict(x.reshape(-1, 1))
        else:
            predictions = np.polyval(self.coeffs, x)

        return np.maximum(predictions, 0)


class PolynomialForecastModel:
    """
    Polynomial regression model for non-linear storage growth.
    """

    def __init__(self, degree: int = 2):
        self.degree = degree
        self.start_time: datetime | None = None
        self.coeffs: np.ndarray | None = None
        if _HAS_SKLEARN:
            self.model = LinearRegression()
            self.transformer = PolynomialFeatures(
                degree=degree
            )
        else:
            self.model = None
            self.transformer = None

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
            (
                snapshot.scanned_at
                - self.start_time
            ).total_seconds() / 86400
            for snapshot in snapshots
        ], dtype=float)

        y = np.array([
            snapshot.total_bytes
            for snapshot in snapshots
        ], dtype=float)

        if self.model is not None and self.transformer is not None:
            x_poly = self.transformer.fit_transform(x.reshape(-1, 1))
            self.model.fit(x_poly, y)
        else:
            self.coeffs = np.polyfit(x, y, self.degree)

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
            (
                date - self.start_time
            ).total_seconds() / 86400
            for date in future_dates
        ], dtype=float)

        if self.model is not None and self.transformer is not None:
            x_poly = self.transformer.transform(x.reshape(-1, 1))
            predictions = self.model.predict(x_poly)
        else:
            predictions = np.polyval(self.coeffs, x)

        return np.maximum(predictions, 0)


class HoltWintersForecastModel:
    """
    Holt's exponential smoothing model.
    """

    def __init__(self):
        self.forecast_model = None
        self.level: float | None = None
        self.trend: float | None = None
        self.phi: float = 0.98

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

        if _HAS_STATSMODELS:
            self.forecast_model = ExponentialSmoothing(
                values,
                trend="add",
                damped_trend=True,
                initialization_method="estimated",
            ).fit()
        else:
            alpha = 0.3
            beta = 0.1
            phi = self.phi
            level = values[0]
            trend = (values[-1] - values[0]) / max(1, len(values) - 1)
            for t in range(1, len(values)):
                new_level = alpha * values[t] + (1 - alpha) * (level + phi * trend)
                trend = beta * (new_level - level) + (1 - beta) * phi * trend
                level = new_level
            self.level = level
            self.trend = trend

        return self

    def predict(
        self,
        steps: int,
    ) -> np.ndarray:

        if _HAS_STATSMODELS and self.forecast_model is not None:
            predictions = self.forecast_model.forecast(
                steps
            )
        elif self.level is not None and self.trend is not None:
            preds = []
            cum_phi = 0.0
            for h in range(1, steps + 1):
                cum_phi += (self.phi ** h)
                preds.append(self.level + cum_phi * self.trend)
            predictions = np.array(preds)
        else:
            raise RuntimeError(
                "Model must be fitted before prediction."
            )

        return np.maximum(
            np.asarray(predictions),
            0,
        )