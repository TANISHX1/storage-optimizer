from core.go_client import GoCoreClient


def main():
    client = GoCoreClient()

    print("\n=== GO CORE CLIENT TEST ===")

    health = client.health()

    print("\n[1] HEALTH")
    print(health)

    stats = client.stats()

    print("\n[2] STATS")
    print(
        f"Files: {stats.get('total_files'):,}"
    )
    print(
        f"Bytes: {stats.get('total_bytes'):,}"
    )

    snapshots = client.snapshots(limit=5)

    print("\n[3] SNAPSHOTS")
    print(
        f"Received: "
        f"{len(snapshots.get('snapshots') or [])}"
    )

    stale = client.stale_files(
        days=30,
        limit=5,
    )

    print("\n[4] STALE")
    print(
        f"Received: "
        f"{len(stale.get('files') or [])}"
    )

    duplicates = client.duplicates()

    print("\n[5] DUPLICATES")
    print(
        f"Groups: "
        f"{len(duplicates.get('groups') or [])}"
    )

    actions = client.action_history()

    print("\n[6] ACTION HISTORY")
    print(
        f"Actions: "
        f"{len(actions.get('actions') or [])}"
    )

    print("\n=== GO CORE CLIENT TEST PASSED ===")


if __name__ == "__main__":
    main()
