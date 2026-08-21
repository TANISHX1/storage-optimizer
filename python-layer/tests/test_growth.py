from pathlib import Path

from core.mock_provider import MockDataProvider
from forecast.growth import (
    calculate_growth_metrics,
    calculate_growth_points,
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

    metrics = calculate_growth_metrics(
        snapshots
    )

    points = calculate_growth_points(
        snapshots
    )

    print("\n=== STORAGE GROWTH ANALYSIS ===")

    print(
        f"Snapshots analyzed: "
        f"{metrics.snapshot_count}"
    )

    print(
        f"Current storage: "
        f"{format_bytes(metrics.current_bytes)}"
    )

    print(
        f"Current files: "
        f"{metrics.current_files:,}"
    )

    print(
        f"Total growth: "
        f"{format_bytes(metrics.total_growth_bytes)}"
    )

    print(
        f"Total growth percentage: "
        f"{metrics.total_growth_percent:.2f}%"
    )

    print(
        f"Average daily growth: "
        f"{format_bytes(metrics.daily_growth_rate_bytes)}"
    )

    print(
        f"Average weekly growth: "
        f"{format_bytes(metrics.weekly_growth_rate_bytes)}"
    )

    print(
        f"Growth volatility: "
        f"{format_bytes(metrics.growth_volatility_bytes)}"
    )

    print("\n=== GROWTH POINTS ===")

    for point in points:
        print(
            f"{point.timestamp} | "
            f"storage={format_bytes(point.bytes)} | "
            f"growth={format_bytes(point.growth_bytes)} | "
            f"daily={format_bytes(point.growth_rate_bytes_per_day)}"
        )


if __name__ == "__main__":
    main()