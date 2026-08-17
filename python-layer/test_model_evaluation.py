from pathlib import Path

from core.mock_provider import MockDataProvider

from forecast.evaluation import evaluate_models


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

    results = evaluate_models(
        snapshots,
        test_size=3,
    )

    print("\n=== MODEL EVALUATION ===")

    for result in results:

        print(
            f"\n{result.model_name}"
        )

        print(
            f"MAE: "
            f"{format_bytes(result.mae_bytes)}"
        )

        print(
            f"RMSE: "
            f"{format_bytes(result.rmse_bytes)}"
        )

    best_model = min(
        results,
        key=lambda result: result.mae_bytes,
    )

    print("\n=== BEST MODEL ===")

    print(
        f"{best_model.model_name}"
    )

    print(
        f"MAE: "
        f"{format_bytes(best_model.mae_bytes)}"
    )


if __name__ == "__main__":
    main()