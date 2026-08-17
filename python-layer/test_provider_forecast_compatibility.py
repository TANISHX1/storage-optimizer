from pathlib import Path

from core.mock_provider import MockDataProvider
from forecast.forecast import forecast_storage_from_provider


def main():
    provider = MockDataProvider(
        Path("data/mock_data.json")
    )

    # Use the root represented in the mock dataset.
    snapshots = provider.get_snapshots()

    if not snapshots:
        raise RuntimeError("Mock dataset contains no snapshots.")

    root = snapshots[0].root_path

    # Filtered provider path should now work exactly like
    # the live GoCoreProvider path.
    result = forecast_storage_from_provider(
        provider=provider,
        root=root,
        forecast_days=30,
        validation_size=3,
    )

    print("\n=== PROVIDER FORECAST COMPATIBILITY ===")
    print(f"Root: {root}")
    print(f"Model: {result.model_name}")
    print(f"MAE: {result.mae_bytes:.2f}")
    print(f"RMSE: {result.rmse_bytes:.2f}")
    print("Provider-based forecasting: PASSED")


if __name__ == "__main__":
    main()
