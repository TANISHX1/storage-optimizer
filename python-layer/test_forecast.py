from pathlib import Path

from core.mock_provider import MockDataProvider

from forecast.pipeline import (
    create_future_dates,
    forecast_linear,
    forecast_polynomial,
    forecast_holt_winters,
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

    provider = MockDataProvider(
        Path("data/mock_data.json")
    )

    snapshots = provider.get_snapshots()

    latest_date = snapshots[-1].scanned_at

    future_dates = create_future_dates(
        latest_date,
        days=30,
    )

    print("\n=== LINEAR REGRESSION ===")

    linear = forecast_linear(
        snapshots,
        future_dates,
    )

    for point in linear[::5]:
        print(
            f"{point.date.date()} -> "
            f"{format_bytes(point.predicted_bytes)}"
        )

    print("\n=== POLYNOMIAL REGRESSION ===")

    polynomial = forecast_polynomial(
        snapshots,
        future_dates,
        degree=2,
    )

    for point in polynomial[::5]:
        print(
            f"{point.date.date()} -> "
            f"{format_bytes(point.predicted_bytes)}"
        )

    print("\n=== HOLT-WINTERS ===")

    holt = forecast_holt_winters(
        snapshots,
        future_dates,
    )

    for point in holt[::5]:
        print(
            f"{point.date.date()} -> "
            f"{format_bytes(point.predicted_bytes)}"
        )


if __name__ == "__main__":
    main()