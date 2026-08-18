from __future__ import annotations

import csv
from datetime import datetime
from pathlib import Path
from typing import List, Dict

from .provider import DataProvider
from .models import (
    Snapshot,
    StaleFile,
    DuplicateCluster,
    Action,
)


class SyntheticDataProvider(DataProvider):
    """
    Provides synthetic historical storage snapshots
    for ARIMA development and evaluation.

    This provider intentionally mirrors the same
    interface used by MockDataProvider and GoCoreProvider.
    """

    def __init__(
        self,
        data_path: str | Path,
    ):
        self.data_path = Path(data_path)

        if not self.data_path.exists():
            raise FileNotFoundError(
                f"Synthetic dataset not found: {self.data_path}"
            )

        self.snapshots = self._load_snapshots()

    def _load_snapshots(self) -> List[Snapshot]:
        snapshots: List[Snapshot] = []

        with self.data_path.open(
            "r",
            encoding="utf-8",
            newline="",
        ) as file:

            reader = csv.DictReader(file)

            required_fields = {
                "timestamp",
                "root_path",
                "total_files",
                "total_bytes",
            }

            if not required_fields.issubset(
                reader.fieldnames or []
            ):
                raise ValueError(
                    "Synthetic dataset is missing required "
                    f"fields: {required_fields}"
                )

            for row in reader:

                snapshots.append(
                    Snapshot(
                        id=len(snapshots) + 1,
                        scanned_at=datetime.fromisoformat(
                            row["timestamp"]
                        ),
                        root_path=row["root_path"],
                        total_files=int(
                            row["total_files"]
                        ),
                        total_bytes=int(
                            row["total_bytes"]
                        ),
                    )
                )

        snapshots.sort(
            key=lambda snapshot: snapshot.scanned_at
        )

        return snapshots

    def get_snapshots(
        self,
        root: str | None = None,
    ) -> List[Snapshot]:

        if root is None:
            return list(self.snapshots)

        return [
            snapshot
            for snapshot in self.snapshots
            if snapshot.root_path == root
        ]

    def get_category_stats(
        self,
    ) -> Dict[str, Dict[str, int]]:

        return {}

    def get_stale_files(
        self,
        days: int = 30,
    ) -> List[StaleFile]:

        return []

    def get_duplicates(
        self,
    ) -> List[DuplicateCluster]:

        return []

    def get_action_history(
        self,
    ) -> List[Action]:

        return []