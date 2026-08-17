import sys

from core.api_provider import GoCoreProvider
from forecast.forecast import (
    get_forecast_status,
    forecast_storage_from_provider,
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
    if len(sys.argv) != 2:
        print(
            "Usage:\n"
            "  python test_live_forecast.py <root-path>"
        )
        sys.exit(1)

    root = sys.argv[1]

    provider = GoCoreProvider()

    print("\n=== LIVE GO CORE FORECAST ===")
    print(f"Root: {root}")

    snapshots = provider.get_snapshots(root=root)

    status = get_forecast_status(
        snapshots,
        root=root,
    )

    print("\n=== FORECAST STATUS ===")
    print(f"Status: {status.status}")
    print(
        f"Valid snapshots: "
        f"{status.snapshots_available}/"
        f"{status.snapshots_required}"
    )
    print(f"Message: {status.message}")

    if status.status != "ready":
        return

    result = forecast_storage_from_provider(
        provider=provider,
        root=root,
        forecast_days=30,
        validation_size=3,
    )

    print("\n=== MODEL ===")
    print(f"Selected model: {result.model_name}")
    print(f"MAE: {format_bytes(result.mae_bytes)}")
    print(f"RMSE: {format_bytes(result.rmse_bytes)}")

    print("\n=== FORECAST ===")

    for point in result.forecast_points[::5]:
        print(
            f"{point.date} -> "
            f"{format_bytes(point.predicted_bytes)}"
        )


if __name__ == "__main__":
    main()
