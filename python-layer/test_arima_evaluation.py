from pathlib import Path

from core.synthetic_provider import (
    SyntheticDataProvider,
)

from forecast.forecast import (
    forecast_storage,
)


DATA_PATH = Path(
    "data/synthetic_arima.csv"
)


def format_bytes(value: float) -> str:

    units = [
        "B",
        "KB",
        "MB",
        "GB",
        "TB",
    ]

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

    result = forecast_storage(
        snapshots,
        forecast_days=30,
        validation_size=30,
    )

    print(
        "\n=== UNIFIED FORECAST ==="
    )

    print(
        f"Selected model: "
        f"{result.model_name}"
    )

    print(
        f"MAE: "
        f"{format_bytes(result.mae_bytes)}"
    )

    print(
        f"RMSE: "
        f"{format_bytes(result.rmse_bytes)}"
    )

    print(
        f"MAPE: "
        f"{result.mape_percent:.3f}%"
    )

    if result.arima_order:

        print(
            f"ARIMA order: "
            f"{result.arima_order}"
        )

    print(
        "\n=== FUTURE FORECAST ==="
    )

    for point in result.forecast_points[::5]:

        print(
            f"{point.date.date()} -> "
            f"{format_bytes(point.predicted_bytes)}"
        )

        if point.lower_bound is not None:

            print(
                f"    CI: "
                f"{format_bytes(point.lower_bound)}"
                " - "
                f"{format_bytes(point.upper_bound)}"
            )

    assert len(
        result.forecast_points
    ) == 30

    print(
        "\n=== UNIFIED FORECAST TEST PASSED ==="
    )


if __name__ == "__main__":
    main()