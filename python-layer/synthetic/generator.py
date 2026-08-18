from __future__ import annotations

import csv
import random
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path


@dataclass
class SyntheticSnapshot:
    timestamp: datetime
    root_path: str
    total_files: int
    total_bytes: int


def generate_synthetic_snapshots(
    days: int = 150,
    start_date: datetime | None = None,
    root_path: str = "/synthetic/storage",
    initial_bytes: int = 100 * 1024**3,
    initial_files: int = 80_000,
    seed: int = 42,
) -> list[SyntheticSnapshot]:
    """
    Generate deterministic synthetic daily storage snapshots.

    The generated series contains:
    - baseline growth
    - weekly variation
    - random noise
    - occasional growth spikes
    - occasional cleanup events
    """

    if days < 30:
        raise ValueError("At least 30 days are recommended.")

    if initial_bytes <= 0:
        raise ValueError("initial_bytes must be positive.")

    if initial_files <= 0:
        raise ValueError("initial_files must be positive.")

    rng = random.Random(seed)

    if start_date is None:
        start_date = datetime(2026, 5, 1)

    storage = float(initial_bytes)
    files = float(initial_files)

    snapshots: list[SyntheticSnapshot] = []

    for day in range(days):

        timestamp = start_date + timedelta(days=day)

        # Normal daily growth: roughly 0.5–1.8 GB.
        baseline_growth = rng.uniform(
            0.5 * 1024**3,
            1.8 * 1024**3,
        )

        # Weak weekly pattern.
        weekday = timestamp.weekday()

        weekly_multiplier = {
            0: 1.00,  # Monday
            1: 1.05,
            2: 1.10,
            3: 1.15,
            4: 1.25,  # Friday
            5: 0.75,
            6: 0.65,
        }[weekday]

        weekly_growth = (
            baseline_growth
            * weekly_multiplier
        )

        # Measurement / behavioral noise.
        noise = rng.gauss(
            0,
            0.12 * 1024**3,
        )

        delta = weekly_growth + noise

        # Occasional burst: large download, build, dataset, etc.
        if rng.random() < 0.07:
            delta += rng.uniform(
                2 * 1024**3,
                8 * 1024**3,
            )

        # Occasional cleanup.
        if rng.random() < 0.05:
            delta -= rng.uniform(
                1 * 1024**3,
                6 * 1024**3,
            )

        storage = max(
            1,
            storage + delta,
        )

        # File count broadly follows storage growth,
        # with independent small variation.
        file_delta = (
            delta / (350 * 1024)
        )

        file_delta += rng.gauss(0, 75)

        files = max(
            1,
            files + file_delta,
        )

        snapshots.append(
            SyntheticSnapshot(
                timestamp=timestamp,
                root_path=root_path,
                total_files=int(files),
                total_bytes=int(storage),
            )
        )

    return snapshots


def write_csv(
    snapshots: list[SyntheticSnapshot],
    output_path: str | Path,
) -> None:
    output_path = Path(output_path)

    output_path.parent.mkdir(
        parents=True,
        exist_ok=True,
    )

    with output_path.open(
        "w",
        newline="",
        encoding="utf-8",
    ) as file:

        writer = csv.writer(file)

        writer.writerow([
            "timestamp",
            "root_path",
            "total_files",
            "total_bytes",
        ])

        for snapshot in snapshots:
            writer.writerow([
                snapshot.timestamp.isoformat(),
                snapshot.root_path,
                snapshot.total_files,
                snapshot.total_bytes,
            ])


if __name__ == "__main__":
    snapshots = generate_synthetic_snapshots()

    output = (
        Path(__file__).resolve().parent.parent
        / "data"
        / "synthetic_arima.csv"
    )

    write_csv(
        snapshots,
        output,
    )

    print(
        f"Generated {len(snapshots)} snapshots."
    )

    print(
        f"Output: {output}"
    )

    print(
        f"Initial storage: "
        f"{snapshots[0].total_bytes:,} bytes"
    )

    print(
        f"Final storage: "
        f"{snapshots[-1].total_bytes:,} bytes"
    )