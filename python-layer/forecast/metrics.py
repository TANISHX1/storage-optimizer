import numpy as np
from sklearn.metrics import mean_absolute_error, mean_squared_error


def calculate_mae(
    actual: np.ndarray,
    predicted: np.ndarray,
) -> float:
    """
    Mean Absolute Error.
    """

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
    """
    Root Mean Squared Error.
    """

    return float(
        np.sqrt(
            mean_squared_error(
                actual,
                predicted,
            )
        )
    )