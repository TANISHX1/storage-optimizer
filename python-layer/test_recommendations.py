from recommend.engine import (
    generate_recommendations,
)


GB = 1024 ** 3


def main():

    recommendations = generate_recommendations(

        duplicate_bytes=8 * GB,

        stale_bytes=12 * GB,

        stale_days=60,

        daily_growth_bytes=2.4 * GB,

        utilization_percent=58.1,

        days_until_90=37,

        days_until_100=46,
    )

    print("\n=== STORAGE RECOMMENDATIONS ===")

    for index, recommendation in enumerate(
        recommendations,
        start=1,
    ):

        print(
            f"\n[{index}] "
            f"{recommendation.severity.upper()}"
        )

        print(
            f"Type: "
            f"{recommendation.type}"
        )

        print(
            f"Title: "
            f"{recommendation.title}"
        )

        print(
            f"Message: "
            f"{recommendation.message}"
        )

        if recommendation.potential_savings_bytes:

            print(
                "Potential savings: "
                f"{recommendation.potential_savings_bytes / GB:.2f} GB"
            )

        if recommendation.days_until_action is not None:

            print(
                "Days until action: "
                f"{recommendation.days_until_action:.1f}"
            )


if __name__ == "__main__":
    main()