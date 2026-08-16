from dataclasses import dataclass
from datetime import datetime
from typing import List, Dict, Any


@dataclass
class Snapshot:
    id: int
    scanned_at: datetime
    root_path: str
    total_files: int
    total_bytes: int


@dataclass
class CategoryStats:
    files: int
    bytes: int


@dataclass
class StaleFile:
    path: str
    size_bytes: int
    last_accessed: datetime
    staleness_score: float


@dataclass
class DuplicateCluster:
    hash: str
    file_count: int
    total_bytes: int
    wasted_bytes: int
    files: List[str]


@dataclass
class Action:
    id: int
    action: str
    path: str
    size_bytes: int
    created_at: datetime