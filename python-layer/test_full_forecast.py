from pathlib import Path

from core.mock_provider import MockDataProvider
from forecast.forecast import forecast_storage


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

    result = forecast_storage(
        snapshots,
        forecast_days=30,
        validation_size=3,
    )

    print("\n=== STORAGE FORECAST ===")

    print(
        f"Selected model: "
        f"{result.model_name}"
    )

    print(
        f"Validation MAE: "
        f"{format_bytes(result.mae_bytes)}"
    )

    print(
        f"Validation RMSE: "
        f"{format_bytes(result.rmse_bytes)}"
    )

    print("\n=== FORECAST ===")

    for point in result.forecast_points[::5]:

        print(
            f"{point.date.date()} -> "
            f"{format_bytes(point.predicted_bytes)}"
        )


if __name__ == "__main__":
    main()