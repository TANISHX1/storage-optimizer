from core.api_provider import GoCoreProvider


def main():
    provider = GoCoreProvider()

    print("\n=== GO CORE LIVE PROVIDER TEST ===")

    print("\n[1] HEALTH")
    print(provider.health())

    print("\n[2] SNAPSHOTS")
    snapshots = provider.get_snapshots()

    print(f"Snapshots: {len(snapshots)}")

    for snapshot in snapshots[-5:]:
        print(
            f"{snapshot.scanned_at} | "
            f"{snapshot.root_path} | "
            f"{snapshot.total_files:,} files | "
            f"{snapshot.total_bytes:,} bytes"
        )

    print("\n[3] CATEGORIES")
    categories = provider.get_category_stats()

    for category, stats in categories.items():
        print(
            f"{category}: "
            f"{stats['files']:,} files | "
            f"{stats['bytes']:,} bytes"
        )

    print("\n[4] STALE FILES")
    stale = provider.get_stale_files(days=30)

    print(f"Stale files: {len(stale)}")

    for file in stale[:5]:
        print(
            f"{file.id}: "
            f"{file.path} | "
            f"{file.size_bytes:,} bytes | "
            f"score={file.staleness_score:.4f}"
        )

    print("\n[5] DUPLICATES")
    duplicates = provider.get_duplicates()

    print(f"Duplicate groups: {len(duplicates)}")

    for group in duplicates[:5]:
        print(
            f"{group.hash[:16]}... | "
            f"copies={group.file_count} | "
            f"wasted={group.wasted_bytes:,} bytes"
        )

    print("\n[6] ACTION HISTORY")
    actions = provider.get_action_history()

    print(f"Actions: {len(actions)}")

    for action in actions[:5]:
        print(
            f"{action.id}: "
            f"{action.action} | "
            f"{action.path} | "
            f"{action.size_bytes:,} bytes"
        )

    print("\n=== LIVE PROVIDER TEST PASSED ===")


if __name__ == "__main__":
    main()
