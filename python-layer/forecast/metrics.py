import numpy as np

try:
    from sklearn.metrics import mean_absolute_error, mean_squared_error
    _HAS_SKLEARN = True
except ImportError:
    _HAS_SKLEARN = False


def calculate_mae(
    actual: np.ndarray,
    predicted: np.ndarray,
) -> float:
    """
    Mean Absolute Error.
    """
    if _HAS_SKLEARN:
        return float(
            mean_absolute_error(
                actual,
                predicted,
            )
        )
    return float(np.mean(np.abs(np.asarray(actual) - np.asarray(predicted))))


def calculate_rmse(
    actual: np.ndarray,
    predicted: np.ndarray,
) -> float:
    """
    Root Mean Squared Error.
    """
    if _HAS_SKLEARN:
        return float(
            np.sqrt(
                mean_squared_error(
                    actual,
                    predicted,
                )
            )
        )
    return float(np.sqrt(np.mean((np.asarray(actual) - np.asarray(predicted)) ** 2)))