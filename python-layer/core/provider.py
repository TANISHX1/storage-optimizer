from abc import ABC, abstractmethod
from typing import List, Dict

from .models import (
    Snapshot,
    StaleFile,
    DuplicateCluster,
    Action,
)


class DataProvider(ABC):

    @abstractmethod
    def get_snapshots(
        self,
        root: str | None = None,
    ) -> List[Snapshot]:
        pass

    @abstractmethod
    def get_category_stats(
        self,
    ) -> Dict[str, Dict[str, int]]:
        pass

    @abstractmethod
    def get_stale_files(
        self,
        days: int = 30,
    ) -> List[StaleFile]:
        pass

    @abstractmethod
    def get_duplicates(
        self,
    ) -> List[DuplicateCluster]:
        pass

    @abstractmethod
    def get_action_history(
        self,
    ) -> List[Action]:
        pass