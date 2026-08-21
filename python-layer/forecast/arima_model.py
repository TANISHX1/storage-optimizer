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

    # Snapshots are scan-event based, not guaranteed to land on
    # exact 24h boundaries. This tolerance defines how far a gap
    # between consecutive snapshots may drift from 24h and still
    # be considered "daily" for ARIMA purposes.
    DAILY_TOLERANCE_SECONDS = 3600

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

    @staticmethod
    def _validate_daily_cadence(
        timestamps: List[datetime],
        tolerance_seconds: int = 3600,
    ) -> None:
        """
        ARIMA needs a genuinely regular time index. Rather than
        silently replacing real scan timestamps with a fabricated
        pd.date_range(freq="D"), we validate that the real gaps
        between consecutive snapshots are approximately daily and
        fail loudly if they are not.
        """

        if len(timestamps) < 2:
            return

        expected_seconds = 86400  # seconds in a day

        for earlier, later in zip(timestamps, timestamps[1:]):
            delta_seconds = (later - earlier).total_seconds()

            if abs(delta_seconds - expected_seconds) > tolerance_seconds:
                raise ValueError(
                    "ARIMA requires approximately daily snapshots. "
                    f"Found a gap of {delta_seconds / 3600:.1f}h between "
                    f"{earlier.isoformat()} and {later.isoformat()} "
                    f"(expected ~24h, tolerance "
                    f"\u00b1{tolerance_seconds / 3600:.1f}h)."
                )

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

        # IMPORTANT:
        # Snapshots come from scan events, not a guaranteed daily
        # cron job. We must NOT replace the real timestamps with a
        # fabricated pd.date_range(freq="D") sequence -- that would
        # silently disconnect the model from the actual cadence of
        # the data (e.g. two scans on the same day, or a multi-day
        # gap, would get mislabeled as consecutive days).
        #
        # Instead we validate that the real timestamps are
        # approximately daily, and only then tag the index with a
        # daily frequency so statsmodels is happy.
        self._validate_daily_cadence(
            timestamps,
            tolerance_seconds=self.DAILY_TOLERANCE_SECONDS,
        )

        index = pd.DatetimeIndex(timestamps)

        series = pd.Series(
            values,
            index=index,
            dtype=float,
        )

        # We've already confirmed above that the real timestamps
        # are ~daily, so it's safe to label the index accordingly.
        # This differs from the original bug: there, freq="D" was
        # used to fabricate timestamps out of thin air. Here we're
        # only attaching frequency metadata to real, validated
        # timestamps, which normalizes each to midnight so
        # statsmodels can treat the series as regular.
        series.index = series.index.normalize()

        if series.index.has_duplicates:
            raise ValueError(
                "Multiple snapshots fall on the same calendar day. "
                "ARIMA requires at most one observation per day."
            )

        series = series.asfreq("D")

        if series.isna().any():
            raise ValueError(
                "Could not align snapshots to a strict daily "
                "frequency after validation. This should not "
                "happen if cadence validation passed; treat as a bug."
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