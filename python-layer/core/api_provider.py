from datetime import datetime
from typing import Any

from .go_client import (
    GoCoreAPIError,
    GoCoreClient,
)

from .models import (
    Action,
    CategoryStats,
    DuplicateCluster,
    IndexedFile,
    Snapshot,
    StaleFile,
    StorageStats,
)

from .provider import DataProvider


class GoCoreAPIError(RuntimeError):
    """Raised when the Go Core API cannot be reached or returns an error."""


class GoCoreProvider(DataProvider):
    """
    Production DataProvider backed by the Go Core REST API.

    This class is the boundary between:
        Go HTTP/JSON
            and
        the existing Python ML/recommendation domain models.

    Python never accesses SQLite directly.
    """

    def __init__(
    self,
    base_url: str = "http://127.0.0.1:8080/api/v1",
    timeout: float = 30.0,
    client: GoCoreClient | None = None,
    ):
        self.client = client or GoCoreClient(
        base_url=base_url,
        timeout=timeout,
    )

    # =========================================================
    # Conversion helpers
    # =========================================================

    @staticmethod
    def _parse_datetime(value: Any) -> datetime | None:
        if value is None or value == "":
            return None

        if isinstance(value, datetime):
            return value

        if not isinstance(value, str):
            raise GoCoreAPIError(
                f"Expected datetime string, received {type(value).__name__}"
            )

        try:
            return datetime.fromisoformat(
                value.replace("Z", "+00:00")
            )
        except ValueError as exc:
            raise GoCoreAPIError(
                f"Invalid datetime received from Go Core: {value!r}"
            ) from exc

    @classmethod
    def _file_from_go(cls, item: dict[str, Any]) -> IndexedFile:
        return IndexedFile(
            id=int(item.get("id", 0)),
            path=str(item.get("path", "")),
            size_bytes=int(item.get("size", 0)),
            mtime=cls._parse_datetime(item.get("mtime")),
            atime=cls._parse_datetime(item.get("atime")),
            inode=int(item.get("inode", 0)),
            extension=str(item.get("extension", "")),
            content_hash=str(item.get("content_hash") or ""),
            staleness_score=float(
                item.get("staleness_score") or 0.0
            ),
            is_system=bool(item.get("is_system", False)),
            category=str(item.get("category") or ""),
            last_scanned_at=cls._parse_datetime(
                item.get("last_scanned_at")
            ),
        )

    # =========================================================
    # DataProvider implementation
    # =========================================================

    def get_snapshots(
    self,
    root: str | None = None,
) -> list[Snapshot]:

        payload = self.client.snapshots(
        limit=1000,
        root=root,
        )

        result: list[Snapshot] = []

        for item in (payload.get("snapshots") or []):

            scanned_at = self._parse_datetime(
            item.get("scanned_at")
        )

            if scanned_at is None:
                continue

            result.append(
                Snapshot(
                    id=int(item.get("id", 0)),
                    scanned_at=scanned_at,
                    root_path=str(
                    item.get("root_path", "")
                    ),
                    total_files=int(
                    item.get("total_files", 0)
                    ),
                    total_bytes=int(
                    item.get("total_bytes", 0)
                    ),
            )
            )

        result.sort(
        key=lambda snapshot: snapshot.scanned_at
        )

        return result
    

    def get_category_stats(
        self,
    ) -> dict[str, dict[str, int]]:
        """
        Normalize Go's category schema into the schema
        expected by the existing recommendation engine.

        Go:
            total_files
            total_bytes

        Python:
            files
            bytes
        """

        payload = self.client.stats()

        categories: dict[str, dict[str, int]] = {}

        for item in (payload.get("categories") or []):
            category = str(
                item.get("category", "")
            )

            if not category:
                continue

            categories[category] = {
                "files": int(
                    item.get("total_files", 0)
                ),
                "bytes": int(
                    item.get("total_bytes", 0)
                ),
            }

        return categories

    def get_stale_files(
        self,
        days: int = 30,
    ) -> list[StaleFile]:
        """
        Fetch stale candidates from Go Core and convert
        FileMetadata objects into the existing StaleFile model.
        """

        payload = self.client.stale_files(
            days=days,
            min_score=0.05,
            limit=100,
        )

        result: list[StaleFile] = []

        for item in (payload.get("files") or []):
            atime = self._parse_datetime(
                item.get("atime")
            )
            mtime = self._parse_datetime(
                item.get("mtime")
            )

            # Existing Python model calls this field
            # "last_accessed"; Go exposes Linux atime.
            last_accessed = atime or mtime

            if last_accessed is None:
                continue

            result.append(
                StaleFile(
                    path=str(item.get("path", "")),
                    size_bytes=int(
                        item.get("size", 0)
                    ),
                    last_accessed=last_accessed,
                    staleness_score=float(
                        item.get("staleness_score") or 0.0
                    ),
                    id=int(item.get("id", 0)),
                    mtime=mtime,
                    atime=atime,
                    inode=int(item.get("inode", 0)),
                    extension=str(
                        item.get("extension", "")
                    ),
                    content_hash=str(
                        item.get("content_hash") or ""
                    ),
                    is_system=bool(
                        item.get("is_system", False)
                    ),
                    category=str(
                        item.get("category") or ""
                    ),
                    last_scanned_at=self._parse_datetime(
                        item.get("last_scanned_at")
                    ),
                )
            )

        return result

    def get_duplicates(
        self,
    ) -> list[DuplicateCluster]:
        """
        Fetch duplicate groups from Go Core.

        The existing recommendation layer only needs
        wasted_bytes, while file_records preserve the
        complete live metadata for future GUI/AI use.
        """

        payload = self.client.duplicates()

        result: list[DuplicateCluster] = []

        for group in (payload.get("groups") or []):
            file_records = [
                self._file_from_go(item)
                for item in group.get("files", [])
            ]

            paths = [
                file.path
                for file in file_records
            ]

            file_size = int(
                group.get("file_size", 0)
            )

            duplicate_count = int(
                group.get("duplicate_count", len(file_records))
            )

            total_bytes = (
                file_size * duplicate_count
            )

            result.append(
                DuplicateCluster(
                    hash=str(
                        group.get("content_hash") or ""
                    ),
                    file_count=duplicate_count,
                    total_bytes=total_bytes,
                    wasted_bytes=int(
                        group.get("wasted_bytes", 0)
                    ),
                    files=paths,
                    file_records=file_records,
                )
            )

        return result

    def get_action_history(
        self,
    ) -> list[Action]:
        """
        Fetch cleanup audit history from Go Core.
        """

        payload = self.client.action_history(
            limit=100
        )

        result: list[Action] = []

        for item in (payload.get("actions") or []):
            created_at = self._parse_datetime(
                item.get("performed_at")
            )

            if created_at is None:
                continue

            result.append(
                Action(
                    id=int(item.get("id", 0)),
                    action=str(
                        item.get("action_mode", "")
                    ),
                    path=str(
                        item.get("file_path", "")
                    ),
                    size_bytes=int(
                        item.get("file_size", 0)
                    ),
                    created_at=created_at,
                    trashed_to_path=item.get(
                        "trashed_to_path"
                    ),
                )
            )

        return result

    # =========================================================
    # Additional Go-specific methods
    # =========================================================

    def health(self) -> dict[str, Any]:
        return self.client.health()

    def get_storage_stats(self) -> StorageStats:
        """
        Return strongly typed aggregate storage statistics.
        """

        payload = self.client.stats()

        categories = []

        for item in (payload.get("categories") or []):
            categories.append(
                CategoryStats(
                    category=str(
                        item.get("category", "")
                    ),
                    files=int(
                        item.get("total_files", 0)
                    ),
                    bytes=int(
                        item.get("total_bytes", 0)
                    ),
                )
            )

        return StorageStats(
            total_files=int(
                payload.get("total_files", 0)
            ),
            total_bytes=int(
                payload.get("total_bytes", 0)
            ),
            total_duplicates=int(
                payload.get("total_duplicates", 0)
            ),
            total_wasted_bytes=int(
                payload.get("total_wasted_bytes", 0)
            ),
            total_snapshots=int(
                payload.get("total_snapshots", 0)
            ),
            categories=categories,
        )
