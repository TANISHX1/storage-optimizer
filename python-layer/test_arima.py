from pathlib import Path

from core.synthetic_provider import SyntheticDataProvider
from forecast.model import ForecastPoint
from forecast.arima_model import (
    ARIMAForecastModel,
    ARIMAConfig,
)
from forecast.pipeline import create_future_dates


DATA_PATH = Path(
    "data/synthetic_arima.csv"
)


def format_bytes(value: float) -> str:
    units = ["B", "KB", "MB", "GB", "TB"]

    size = float(value)

    for unit in units:
        if size < 1024:
            return f"{size:.2f} {unit}"

        size /= 1024

    return f"{size:.2f} PB"


def main():

    provider = SyntheticDataProvider(
        DATA_PATH
    )

    snapshots = provider.get_snapshots()

    # Use all historical synthetic observations.
    model = ARIMAForecastModel(
        ARIMAConfig(
            p=1,
            d=1,
            q=1,
            confidence_level=0.95,
        )
    )

    model.fit(
        snapshots
    )

    latest = snapshots[-1]

    future_dates = create_future_dates(
        latest.scanned_at,
        30,
    )

    points = model.predict(
        future_dates
    )

    print("\n=== ARIMA FORECAST ===")

    print(
        "Model:",
        (
            f"ARIMA("
            f"{model.config.p},"
            f"{model.config.d},"
            f"{model.config.q}"
            f")"
        ),
    )

    print(
        "Current storage:",
        format_bytes(
            latest.total_bytes
        ),
    )

    print("\n=== FORECAST ===")

    for point in points[::5]:

        print(
            f"{point.date.date()} -> "
            f"{format_bytes(point.predicted_bytes)} "
            f"["
            f"{format_bytes(point.lower_bound)}"
            f" - "
            f"{format_bytes(point.upper_bound)}"
            f"]"
        )

    assert len(points) == 30

    assert all(
        point.predicted_bytes >= 0
        for point in points
    )

    assert all(
        point.lower_bound
        <= point.predicted_bytes
        <= point.upper_bound
        for point in points
    )

    print(
        "\n=== ARIMA TEST PASSED ==="
    )


if __name__ == "__main__":
    main()