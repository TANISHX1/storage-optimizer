import csv
from datetime import datetime
from pathlib import Path

from synthetic.generator import generate_synthetic_snapshots


def main():
    snapshots = generate_synthetic_snapshots(
        days=150,
        seed=42,
    )

    assert len(snapshots) == 150

    timestamps = [
        snapshot.timestamp
        for snapshot in snapshots
    ]

    assert timestamps == sorted(timestamps)

    for previous, current in zip(
        timestamps,
        timestamps[1:],
    ):
        assert (
            current - previous
        ).days == 1

    assert all(
        snapshot.total_bytes > 0
        for snapshot in snapshots
    )

    assert all(
        snapshot.total_files > 0
        for snapshot in snapshots
    )

    print("=== SYNTHETIC DATASET VALIDATION ===")
    print(f"Snapshots: {len(snapshots)}")
    print(
        f"Start: {snapshots[0].timestamp}"
    )
    print(
        f"End: {snapshots[-1].timestamp}"
    )
    print(
        f"Start bytes: "
        f"{snapshots[0].total_bytes:,}"
    )
    print(
        f"End bytes: "
        f"{snapshots[-1].total_bytes:,}"
    )
    print("Daily frequency: PASS")
    print("Positive storage values: PASS")
    print("Positive file counts: PASS")
    print("=== VALIDATION PASSED ===")


if __name__ == "__main__":
    main()
    