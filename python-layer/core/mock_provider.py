import json
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


class MockDataProvider(DataProvider):
    """
    Loads storage optimizer data from the local mock JSON dataset.

    This provider is used during Windows development.
    """

    def __init__(self, data_path: str | Path):
        self.data_path = Path(data_path)

        if not self.data_path.exists():
            raise FileNotFoundError(
                f"Mock dataset not found: {self.data_path}"
            )

        with self.data_path.open("r", encoding="utf-8") as file:
            self.data = json.load(file)

    def get_snapshots(self) -> List[Snapshot]:
        snapshots = []

        for item in self.data.get("snapshots", []):
            snapshots.append(
                Snapshot(
                    id=item["id"],
                    scanned_at=datetime.fromisoformat(
                        item["scanned_at"]
                    ),
                    root_path=item["root_path"],
                    total_files=item["total_files"],
                    total_bytes=item["total_bytes"],
                )
            )

        return snapshots

    def get_category_stats(self) -> Dict[str, Dict[str, int]]:
        stats = self.data.get("stats", {})

        return stats.get("categories", {})

    def get_stale_files(self, days: int = 30) -> List[StaleFile]:
        stale_data = self.data.get("stale_files", {})

        files = []

        for item in stale_data.get("files", []):
            files.append(
                StaleFile(
                    path=item["path"],
                    size_bytes=item["size_bytes"],
                    last_accessed=datetime.fromisoformat(
                        item["last_accessed"]
                    ),
                    staleness_score=item["staleness_score"],
                )
            )

        return files

    def get_duplicates(self) -> List[DuplicateCluster]:
        duplicate_data = self.data.get("duplicates", {})

        clusters = []

        for item in duplicate_data.get("clusters", []):
            clusters.append(
                DuplicateCluster(
                    hash=item["hash"],
                    file_count=item["file_count"],
                    total_bytes=item["total_bytes"],
                    wasted_bytes=item["wasted_bytes"],
                    files=item["files"],
                )
            )

        return clusters

    def get_action_history(self) -> List[Action]:
        actions = []

        for item in self.data.get("actions_history", []):
            actions.append(
                Action(
                    id=item["id"],
                    action=item["action"],
                    path=item["path"],
                    size_bytes=item["size_bytes"],
                    created_at=datetime.fromisoformat(
                        item["created_at"]
                    ),
                )
            )

        return actions