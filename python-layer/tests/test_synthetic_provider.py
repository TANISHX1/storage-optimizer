from pathlib import Path

from core.synthetic_provider import SyntheticDataProvider


DATA_PATH = Path("data/synthetic_arima.csv")


def main():

    provider = SyntheticDataProvider(
        DATA_PATH
    )

    snapshots = provider.get_snapshots()

    print("\n=== SYNTHETIC PROVIDER TEST ===")

    print(
        f"Snapshots: {len(snapshots)}"
    )

    print(
        f"First: {snapshots[0]}"
    )

    print(
        f"Latest: {snapshots[-1]}"
    )

    print(
        f"Root: {snapshots[0].root_path}"
    )

    print(
        f"Initial bytes: "
        f"{snapshots[0].total_bytes:,}"
    )

    print(
        f"Final bytes: "
        f"{snapshots[-1].total_bytes:,}"
    )

    assert len(snapshots) == 150

    assert all(
        snapshot.root_path == "/synthetic/storage"
        for snapshot in snapshots
    )

    assert all(
        snapshot.total_bytes > 0
        for snapshot in snapshots
    )

    print(
        "\n=== SYNTHETIC PROVIDER TEST PASSED ==="
    )


if __name__ == "__main__":
    main()