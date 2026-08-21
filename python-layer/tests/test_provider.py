from pathlib import Path

from core.mock_provider import MockDataProvider


DATA_PATH = Path("data/mock_data.json")


def main():
    provider = MockDataProvider(DATA_PATH)

    snapshots = provider.get_snapshots()
    categories = provider.get_category_stats()
    stale_files = provider.get_stale_files()
    duplicates = provider.get_duplicates()
    actions = provider.get_action_history()

    print("\n=== STORAGE SNAPSHOTS ===")
    print(f"Number of snapshots: {len(snapshots)}")

    if snapshots:
        print(f"First snapshot: {snapshots[0]}")
        print(f"Latest snapshot: {snapshots[-1]}")

    print("\n=== CATEGORIES ===")

    for category, stats in categories.items():
        print(
            f"{category}: "
            f"{stats['files']} files, "
            f"{stats['bytes']} bytes"
        )

    print("\n=== STALE FILES ===")
    print(f"Number of stale files: {len(stale_files)}")

    for file in stale_files:
        print(
            f"{file.path} -> "
            f"{file.size_bytes} bytes "
            f"(score={file.staleness_score})"
        )

    print("\n=== DUPLICATES ===")
    print(f"Duplicate clusters: {len(duplicates)}")

    for cluster in duplicates:
        print(
            f"{cluster.hash}: "
            f"{cluster.wasted_bytes} wasted bytes"
        )

    print("\n=== ACTION HISTORY ===")
    print(f"Actions: {len(actions)}")

    for action in actions:
        print(
            f"{action.action}: "
            f"{action.path} "
            f"({action.size_bytes} bytes)"
        )


if __name__ == "__main__":
    main()