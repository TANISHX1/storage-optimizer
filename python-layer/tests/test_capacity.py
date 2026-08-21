from forecast.capacity_pipeline import (
    run_capacity_prediction,
)


GB = 1024 ** 3


def format_bytes(value: float) -> str:
    units = ["B", "KB", "MB", "GB", "TB"]

    size = float(value)

    for unit in units:

        if size < 1024:
            return f"{size:.2f} {unit}"

        size /= 1024

    return f"{size:.2f} PB"


def main():

    # Mock disk capacity = 256 GB

    total_capacity = 256 * GB

    forecast_result, capacity = (
        run_capacity_prediction(
            total_capacity_bytes=total_capacity,
            forecast_days=365,
        )
    )

    print("\n=== CAPACITY PREDICTION ===")

    print(
        f"Selected model: "
        f"{forecast_result.model_name}"
    )

    print(
        f"Total capacity: "
        f"{format_bytes(capacity.total_capacity_bytes)}"
    )

    print(
        f"Current storage: "
        f"{format_bytes(capacity.current_bytes)}"
    )

    print(
        f"Current utilization: "
        f"{capacity.current_utilization_percent:.2f}%"
    )

    print("\n=== THRESHOLDS ===")

    print(
        f"90% threshold: "
        f"{format_bytes(capacity.threshold_90_bytes)}"
    )

    print(
        f"100% threshold: "
        f"{format_bytes(capacity.threshold_100_bytes)}"
    )

    print("\n=== PREDICTIONS ===")

    if capacity.date_at_90_percent:
        print(
            f"90% capacity: "
            f"{capacity.date_at_90_percent}"
        )

        print(
            f"Days until 90%: "
            f"{capacity.days_until_90_percent:.1f}"
        )

    else:
        print(
            "90% capacity: "
            "Not reached within forecast window"
        )

    if capacity.date_at_100_percent:
        print(
            f"100% capacity: "
            f"{capacity.date_at_100_percent}"
        )

        print(
            f"Days until 100%: "
            f"{capacity.days_until_100_percent:.1f}"
        )

    else:
        print(
            "100% capacity: "
            "Not reached within forecast window"
        )


if __name__ == "__main__":
    main()