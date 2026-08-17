from core.api_provider import GoCoreProvider
from recommend.pipeline import run_recommendation_pipeline


ROOT = "/home/vanshpratapsinghjadon/Desktop/storage-optimizer"
GB = 1024 ** 3


def format_bytes(value):
    units = ["B", "KB", "MB", "GB", "TB"]

    size = float(value)

    for unit in units:
        if size < 1024:
            return f"{size:.2f} {unit}"
        size /= 1024

    return f"{size:.2f} PB"


def main():
    provider = GoCoreProvider()

    result = run_recommendation_pipeline(
        provider=provider,
        total_capacity_bytes=256 * GB,
        forecast_days=365,
        stale_days=30,
        root=ROOT,
    )

    print("\n=== LIVE GO CORE RECOMMENDATIONS ===")

    print("\n=== STORAGE INPUTS ===")

    print(
        "Duplicate waste:",
        format_bytes(result["duplicate_bytes"])
    )

    print(
        "Stale storage:",
        format_bytes(result["stale_bytes"])
    )

    print(
        "Daily growth:",
        format_bytes(result["daily_growth_bytes"])
    )

    print(
        "Snapshots:",
        len(result["snapshots"])
    )

    print(
        "Forecast status:",
        result["forecast_status"]
    )

    print("\n=== RECOMMENDATIONS ===")

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
                )
            )


if __name__ == "__main__":
    main()
