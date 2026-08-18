import numpy as np
from sklearn.metrics import (
    mean_absolute_error,
    mean_squared_error,
)


def calculate_mae(
    actual: np.ndarray,
    predicted: np.ndarray,
) -> float:
    return float(
        mean_absolute_error(
            actual,
            predicted,
        )
    )


def calculate_rmse(
    actual: np.ndarray,
    predicted: np.ndarray,
) -> float:
    return float(
        np.sqrt(
            mean_squared_error(
                actual,
                predicted,
            )
        )
    )


def calculate_mape(
    actual: np.ndarray,
    predicted: np.ndarray,
) -> float:
    """
    Mean Absolute Percentage Error.

    Returns percentage, not fraction.
    Zero-valued actual observations are excluded.
    """

    actual = np.asarray(
        actual,
        dtype=float,
    )

    predicted = np.asarray(
        predicted,
        dtype=float,
    )

    mask = actual != 0

    if not np.any(mask):
        return 0.0

    return float(
        np.mean(
            np.abs(
                (
                    actual[mask]
                    - predicted[mask]
                )
                / actual[mask]
            )
        )
        * 100
    )