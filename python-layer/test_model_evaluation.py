from pathlib import Path

from core.synthetic_provider import (
    SyntheticDataProvider,
)

from forecast.evaluation import (
    evaluate_models,
    select_best_model,
)


DATA_PATH = Path(
    "data/synthetic_arima.csv"
)


def format_bytes(
    value: float | None,
) -> str:

    if value is None:
        return "N/A"

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
            return (
                f"{size:.2f} {unit}"
            )

        size /= 1024

    return f"{size:.2f} PB"


def main():

    provider = SyntheticDataProvider(
        DATA_PATH
    )

    snapshots = provider.get_snapshots()

    results = evaluate_models(
        snapshots,
        test_size=30,
    )

    print(
        "\n=== MODEL EVALUATION ==="
    )

    for result in results:

        print(
            f"\n{result.model_name}"
        )

        print(
            f"Status: "
            f"{result.status}"
        )

        if result.status == "valid":

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

        else:

            print(
                f"Reason: "
                f"{result.reason}"
            )

    best = select_best_model(
        results
    )

    print(
        "\n=== BEST VALID MODEL ==="
    )

    print(
        f"Model: {best.model_name}"
    )

    print(
        f"MAE: "
        f"{format_bytes(best.mae_bytes)}"
    )

    print(
        f"RMSE: "
        f"{format_bytes(best.rmse_bytes)}"
    )

    print(
        f"MAPE: "
        f"{best.mape_percent:.3f}%"
    )

    if best.arima_order:
        print(
            f"ARIMA order: "
            f"{best.arima_order}"
        )


if __name__ == "__main__":
    main()