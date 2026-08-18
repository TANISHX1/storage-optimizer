from dataclasses import dataclass
from typing import List

import numpy as np

from core.models import Snapshot

from .metrics import (
    calculate_mae,
    calculate_rmse,
)
from .model import (
    LinearForecastModel,
    PolynomialForecastModel,
    HoltWintersForecastModel,
)


@dataclass
class ModelEvaluation:
    model_name: str
    mae_bytes: float
    rmse_bytes: float


def evaluate_linear(
    train: List[Snapshot],
    test: List[Snapshot],
) -> ModelEvaluation:

    model = LinearForecastModel()

    model.fit(train)

    test_dates = [
        snapshot.scanned_at
        for snapshot in test
    ]

    actual = np.array([
        snapshot.total_bytes
        for snapshot in test
    ])

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
    )


def evaluate_polynomial(
    train: List[Snapshot],
    test: List[Snapshot],
    degree: int = 2,
) -> ModelEvaluation:

    model = PolynomialForecastModel(
        degree=degree
    )

    model.fit(train)

    test_dates = [
        snapshot.scanned_at
        for snapshot in test
    ]

    actual = np.array([
        snapshot.total_bytes
        for snapshot in test
    ])

    predicted = model.predict(
        test_dates
    )

    return ModelEvaluation(
        model_name=f"Polynomial Regression (degree={degree})",
        mae_bytes=calculate_mae(
            actual,
            predicted,
        ),
        rmse_bytes=calculate_rmse(
            actual,
            predicted,
        ),
    )


def evaluate_holt_winters(
    train: List[Snapshot],
    test: List[Snapshot],
) -> ModelEvaluation:

    model = HoltWintersForecastModel()

    model.fit(train)

    actual = np.array([
        snapshot.total_bytes
        for snapshot in test
    ])

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
    )


def evaluate_models(
    snapshots: List[Snapshot],
    test_size: int = 3,
) -> List[ModelEvaluation]:

    if len(snapshots) < 3:
        raise ValueError(
            "Not enough snapshots for validation."
        )

    snapshots = sorted(
        snapshots,
        key=lambda snapshot: snapshot.scanned_at,
    )

    effective_test_size = max(1, min(test_size, len(snapshots) - 2))
    train = snapshots[:-effective_test_size]
    test = snapshots[-effective_test_size:]

    results = []

    if len(train) >= 2:
        results.append(
            evaluate_linear(
                train,
                test,
            )
        )

    if len(train) >= 3:
        results.append(
            evaluate_polynomial(
                train,
                test,
            )
        )

    if len(train) >= 4:
        results.append(
            evaluate_holt_winters(
                train,
                test,
            )
        )

    if not results:
        raise ValueError("Not enough training snapshots to evaluate any model.")

    return results