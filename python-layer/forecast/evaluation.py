from dataclasses import dataclass
from typing import List, Tuple
import warnings

import numpy as np

from core.models import Snapshot

from .metrics import (
    calculate_mae,
    calculate_rmse,
    calculate_mape,
)

from .model import (
    LinearForecastModel,
    PolynomialForecastModel,
    HoltWintersForecastModel,
)

from .arima_model import (
    ARIMAForecastModel,
    ARIMAConfig,
)


@dataclass
class ModelEvaluation:
    model_name: str

    mae_bytes: float | None
    rmse_bytes: float | None
    mape_percent: float | None

    status: str = "valid"
    reason: str | None = None

    arima_order: Tuple[int, int, int] | None = None


# =========================================================
# Linear
# =========================================================

def evaluate_linear(
    train: List[Snapshot],
    test: List[Snapshot],
) -> ModelEvaluation:

    try:
        model = LinearForecastModel()
        model.fit(train)

        test_dates = [
            snapshot.scanned_at
            for snapshot in test
        ]

        actual = np.array(
            [
                snapshot.total_bytes
                for snapshot in test
            ],
            dtype=float,
        )

        predicted = model.predict(
            test_dates
        )

        return ModelEvaluation(
            model_name="Linear Regression",
            mae_bytes=calculate_mae(
                actual,
                predicted,
            ),
            rmse_bytes=calculate_rmse(
                actual,
                predicted,
            ),
            mape_percent=calculate_mape(
                actual,
                predicted,
            ),
        )

    except Exception as exc:
        return ModelEvaluation(
            model_name="Linear Regression",
            mae_bytes=None,
            rmse_bytes=None,
            mape_percent=None,
            status="invalid",
            reason=str(exc),
        )


# =========================================================
# Polynomial
# =========================================================

def evaluate_polynomial(
    train: List[Snapshot],
    test: List[Snapshot],
    degree: int = 2,
) -> ModelEvaluation:

    model_name = (
        f"Polynomial Regression (degree={degree})"
    )

    try:
        model = PolynomialForecastModel(
            degree=degree
        )

        model.fit(train)

        test_dates = [
            snapshot.scanned_at
            for snapshot in test
        ]

        actual = np.array(
            [
                snapshot.total_bytes
                for snapshot in test
            ],
            dtype=float,
        )

        predicted = model.predict(
            test_dates
        )

        return ModelEvaluation(
            model_name=model_name,
            mae_bytes=calculate_mae(
                actual,
                predicted,
            ),
            rmse_bytes=calculate_rmse(
                actual,
                predicted,
            ),
            mape_percent=calculate_mape(
                actual,
                predicted,
            ),
        )

    except Exception as exc:
        return ModelEvaluation(
            model_name=model_name,
            mae_bytes=None,
            rmse_bytes=None,
            mape_percent=None,
            status="invalid",
            reason=str(exc),
        )


# =========================================================
# Holt-Winters
# =========================================================

def evaluate_holt_winters(
    train: List[Snapshot],
    test: List[Snapshot],
) -> ModelEvaluation:

    try:
        model = HoltWintersForecastModel()
        model.fit(train)

        actual = np.array(
            [
                snapshot.total_bytes
                for snapshot in test
            ],
            dtype=float,
        )

        predicted = model.predict(
            len(test)
        )

        return ModelEvaluation(
            model_name="Holt-Winters",
            mae_bytes=calculate_mae(
                actual,
                predicted,
            ),
            rmse_bytes=calculate_rmse(
                actual,
                predicted,
            ),
            mape_percent=calculate_mape(
                actual,
                predicted,
            ),
        )

    except Exception as exc:
        return ModelEvaluation(
            model_name="Holt-Winters",
            mae_bytes=None,
            rmse_bytes=None,
            mape_percent=None,
            status="invalid",
            reason=str(exc),
        )


# =========================================================
# ARIMA
# =========================================================

ARIMA_CANDIDATES = [
    (1, 1, 0),
    (0, 1, 1),
    (1, 1, 1),
    (2, 1, 1),
    (1, 1, 2),
    (2, 1, 2),
]


def evaluate_arima(
    train: List[Snapshot],
    test: List[Snapshot],
    order: Tuple[int, int, int],
) -> ModelEvaluation:

    model_name = (
        f"ARIMA{order}"
    )

    try:
        model = ARIMAForecastModel(
            ARIMAConfig(
                p=order[0],
                d=order[1],
                q=order[2],
                confidence_level=0.95,
            )
        )

        # statsmodels may emit warnings during fitting.
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")

            model.fit(train)

        # IMPORTANT:
        # A fitted model is only considered valid if the
        # optimizer actually converged.
        if model.model_fit is None:
            return ModelEvaluation(
                model_name=model_name,
                mae_bytes=None,
                rmse_bytes=None,
                mape_percent=None,
                status="invalid",
                reason="ARIMA fit returned no model.",
                arima_order=order,
            )

        mle_retvals = getattr(
            model.model_fit,
            "mle_retvals",
            {},
        )

        converged = mle_retvals.get(
            "converged",
            True,
        )

        if converged is False:
            return ModelEvaluation(
                model_name=model_name,
                mae_bytes=None,
                rmse_bytes=None,
                mape_percent=None,
                status="invalid",
                reason=(
                    "ARIMA optimizer failed to converge."
                ),
                arima_order=order,
            )

        test_dates = [
            snapshot.scanned_at
            for snapshot in test
        ]

        actual = np.array(
            [
                snapshot.total_bytes
                for snapshot in test
            ],
            dtype=float,
        )

        points = model.predict(
            test_dates
        )

        predicted = np.array(
            [
                point.predicted_bytes
                for point in points
            ],
            dtype=float,
        )

        if not np.all(
            np.isfinite(predicted)
        ):
            return ModelEvaluation(
                model_name=model_name,
                mae_bytes=None,
                rmse_bytes=None,
                mape_percent=None,
                status="invalid",
                reason=(
                    "ARIMA produced "
                    "non-finite predictions."
                ),
                arima_order=order,
            )

        if np.any(predicted < 0):
            return ModelEvaluation(
                model_name=model_name,
                mae_bytes=None,
                rmse_bytes=None,
                mape_percent=None,
                status="invalid",
                reason=(
                    "ARIMA produced negative "
                    "storage predictions."
                ),
                arima_order=order,
            )

        return ModelEvaluation(
            model_name=model_name,
            mae_bytes=calculate_mae(
                actual,
                predicted,
            ),
            rmse_bytes=calculate_rmse(
                actual,
                predicted,
            ),
            mape_percent=calculate_mape(
                actual,
                predicted,
            ),
            status="valid",
            arima_order=order,
        )

    except Exception as exc:
        return ModelEvaluation(
            model_name=model_name,
            mae_bytes=None,
            rmse_bytes=None,
            mape_percent=None,
            status="invalid",
            reason=str(exc),
            arima_order=order,
        )


# =========================================================
# Unified Evaluation
# =========================================================

def evaluate_models(
    snapshots: List[Snapshot],
    test_size: int = 30,
) -> List[ModelEvaluation]:

    if len(snapshots) <= test_size:
        raise ValueError(
            "Not enough snapshots for validation."
        )

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    train = snapshots[:-test_size]
    test = snapshots[-test_size:]

    results: List[ModelEvaluation] = []

    results.append(
        evaluate_linear(
            train,
            test,
        )
    )

    results.append(
        evaluate_polynomial(
            train,
            test,
            degree=2,
        )
    )

    results.append(
        evaluate_holt_winters(
            train,
            test,
        )
    )

    for order in ARIMA_CANDIDATES:

        results.append(
            evaluate_arima(
                train,
                test,
                order,
            )
        )

    return results


# =========================================================
# Best Model
# =========================================================

def select_best_model(
    evaluations: List[ModelEvaluation],
) -> ModelEvaluation:

    valid_models = [
        result
        for result in evaluations
        if (
            result.status == "valid"
            and result.mae_bytes is not None
        )
    ]

    if not valid_models:
        raise RuntimeError(
            "No valid forecasting model "
            "was available."
        )

    return min(
        valid_models,
        key=lambda result: result.mae_bytes,
    )