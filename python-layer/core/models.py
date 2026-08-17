from dataclasses import dataclass, field
from datetime import datetime
from typing import List


@dataclass
class Snapshot:
    """
    Point-in-time storage snapshot returned by Go Core.

    This is the primary input for the forecasting pipeline.
    """

    id: int
    scanned_at: datetime
    root_path: str
    total_files: int
    total_bytes: int


@dataclass
class CategoryStats:
    """
    Normalized category statistics.

    The Go API calls these fields:
        total_files
        total_bytes

    Python exposes them as:
        files
        bytes

    to preserve compatibility with the existing recommendation layer.
    """

    files: int
    bytes: int
    category: str = ""


@dataclass
class IndexedFile:
    """
    Full normalized representation of a Go Core FileMetadata record.

    This model mirrors the information exposed by:
        /api/v1/files/duplicates
        /api/v1/files/stale

    It gives the Python layer access to complete file metadata
    without requiring direct SQLite access.
    """

    id: int
    path: str
    size_bytes: int

    mtime: datetime | None = None
    atime: datetime | None = None

    inode: int = 0
    extension: str = ""

    content_hash: str = ""
    staleness_score: float = 0.0

    is_system: bool = False
    category: str = ""

    last_scanned_at: datetime | None = None


@dataclass
class StaleFile:
    """
    Stale-file domain model consumed by the existing
    recommendation pipeline.

    The original fields are intentionally preserved:

        path
        size_bytes
        last_accessed
        staleness_score

    so existing code continues working unchanged.
    """

    path: str
    size_bytes: int
    last_accessed: datetime
    staleness_score: float

    # Live Go metadata
    id: int = 0
    mtime: datetime | None = None
    atime: datetime | None = None
    inode: int = 0
    extension: str = ""
    content_hash: str = ""
    is_system: bool = False
    category: str = ""
    last_scanned_at: datetime | None = None


@dataclass
class DuplicateCluster:
    """
    Duplicate file cluster normalized from Go Core.

    Existing recommendation code only requires:
        wasted_bytes

    but live metadata is preserved for future GUI/AI analysis.
    """

    hash: str
    file_count: int
    total_bytes: int
    wasted_bytes: int

    # Existing compatibility field:
    # paths of files in the cluster.
    files: List[str] = field(default_factory=list)

    # Complete live Go metadata for the cluster.
    file_records: List[IndexedFile] = field(
        default_factory=list
    )


@dataclass
class Action:
    """
    Normalized cleanup audit record.

    Existing Python code uses:
        id
        action
        path
        size_bytes
        created_at

    Additional Go fields are preserved for future integration.
    """

    id: int
    action: str
    path: str
    size_bytes: int
    created_at: datetime

    trashed_to_path: str | None = None


@dataclass
class StorageStats:
    """
    Aggregate storage statistics returned by Go Core /stats.
    """

    total_files: int
    total_bytes: int

    total_duplicates: int
    total_wasted_bytes: int

    total_snapshots: int

    categories: List[CategoryStats] = field(
        default_factory=list
    )