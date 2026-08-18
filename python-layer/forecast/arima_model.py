from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import List

import numpy as np
import pandas as pd
from statsmodels.tsa.arima.model import ARIMA

from core.models import Snapshot
from .model import ForecastPoint


@dataclass
class ARIMAConfig:
    p: int = 1
    d: int = 1
    q: int = 1
    confidence_level: float = 0.95


class ARIMAForecastModel:
    """
    ARIMA-based storage forecasting model.

    Input:
        Historical Snapshot objects.

    Output:
        ForecastPoint objects containing:
        - predicted storage
        - lower confidence bound
        - upper confidence bound
    """

    def __init__(
        self,
        config: ARIMAConfig | None = None,
    ):
        self.config = config or ARIMAConfig()

        if not 0.0 < self.config.confidence_level < 1.0:
            raise ValueError(
                "confidence_level must be between 0 and 1."
            )

        self.model_fit = None
        self.last_timestamp: datetime | None = None

    def fit(
        self,
        snapshots: List[Snapshot],
    ):
        if len(snapshots) < 10:
            raise ValueError(
                "At least 10 snapshots are required "
                "for ARIMA fitting."
            )

        snapshots = sorted(
            snapshots,
            key=lambda snapshot: snapshot.scanned_at,
        )

        timestamps = [
            snapshot.scanned_at
            for snapshot in snapshots
        ]

        values = np.asarray(
            [
                snapshot.total_bytes
                for snapshot in snapshots
            ],
            dtype=float,
        )

        if np.any(~np.isfinite(values)):
            raise ValueError(
                "Snapshot values contain invalid numbers."
            )

        if np.any(values < 0):
            raise ValueError(
                "Storage values cannot be negative."
            )

        self.last_timestamp = timestamps[-1]

        index = pd.date_range(
            start=timestamps[0],
            periods=len(timestamps),
            freq="D",
        )

        series = pd.Series(
            values,
            index=index,
            dtype=float,
        )

        # ARIMA expects a regular time series.
        inferred_frequency = (
            series.index.inferred_freq
        )

        if inferred_frequency is None:
            raise ValueError(
                "ARIMA requires regularly spaced timestamps."
            )

        self.model_fit = ARIMA(
            series,
            order=(
                self.config.p,
                self.config.d,
                self.config.q,
            ),
        ).fit()

        return self

    def predict(
        self,
        future_dates: List[datetime],
    ) -> List[ForecastPoint]:

        if self.model_fit is None:
            raise RuntimeError(
                "Model must be fitted before prediction."
            )

        if not future_dates:
            return []

        steps = len(future_dates)

        forecast = self.model_fit.get_forecast(
            steps=steps
        )

        predicted = np.asarray(
            forecast.predicted_mean,
            dtype=float,
        )

        alpha = (
            1.0
            - self.config.confidence_level
        )

        confidence = forecast.conf_int(
            alpha=alpha
        )

        lower = np.asarray(
            confidence.iloc[:, 0],
            dtype=float,
        )

        upper = np.asarray(
            confidence.iloc[:, 1],
            dtype=float,
        )

        predicted = np.maximum(
            predicted,
            0,
        )

        lower = np.maximum(
            lower,
            0,
        )

        upper = np.maximum(
            upper,
            0,
        )

        return [
            ForecastPoint(
                date=date,
                predicted_bytes=float(prediction),
                lower_bound=float(lower_bound),
                upper_bound=float(upper_bound),
            )
            for date, prediction, lower_bound, upper_bound
            in zip(
                future_dates,
                predicted,
                lower,
                upper,
            )
        ]