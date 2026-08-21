from pathlib import Path

from recommend.pipeline import (
    run_recommendation_pipeline,
)
from core.mock_provider import MockDataProvider

GB = 1024 ** 3


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
    result = run_recommendation_pipeline(
        provider=MockDataProvider(
            Path("data/mock_data.json")
        ),
        total_capacity_bytes=256 * GB,
        forecast_days=365,
    )

    print(
        "\n=== RECOMMENDATION PIPELINE ==="
    )

    print(
        "\n=== INPUT ANALYSIS ==="
    )

    print(
        "Duplicate waste:",
        format_bytes(
            result["duplicate_bytes"]
        ),
    )

    print(
        "Stale storage:",
        format_bytes(
            result["stale_bytes"]
        ),
    )

    print(
        "Average daily growth:",
        format_bytes(
            result["daily_growth_bytes"]
        ),
    )

    capacity = result["capacity"]

    print(
        "\n=== CAPACITY ==="
    )

    print(
        "Current utilization:",
        f"{capacity.current_utilization_percent:.2f}%",
    )

    print(
        "90% date:",
        capacity.date_at_90_percent,
    )

    print(
        "100% date:",
        capacity.date_at_100_percent,
    )

    print(
        "Days until 90%:",
        capacity.days_until_90_percent,
    )

    print(
        "Days until 100%:",
        capacity.days_until_100_percent,
    )

    print(
        "\n=== RECOMMENDATIONS ==="
    )

    for index, recommendation in enumerate(
        result["recommendations"],
        start=1,
    ):
        print(
            f"\n[{index}] "
            f"{recommendation.severity.upper()}"
        )

        print(
            f"Type: {recommendation.type}"
        )

        print(
            f"Title: {recommendation.title}"
        )

        print(
            f"Message: {recommendation.message}"
        )

        if recommendation.potential_savings_bytes:
            print(
                "Potential savings:",
                format_bytes(
                    recommendation.potential_savings_bytes
                ),
            )


if __name__ == "__main__":
    main()