from dataclasses import dataclass
from typing import Any, List
from .rules import (
    CAPACITY_WARNING_PERCENT,
    CAPACITY_CRITICAL_PERCENT,
    HIGH_DAILY_GROWTH_BYTES,
    DUPLICATE_WARNING_BYTES,
    DUPLICATE_HIGH_BYTES,
    STALE_WARNING_BYTES,
    STALE_HIGH_BYTES,
    DAYS_TO_CAPACITY_WARNING,
    DAYS_TO_CAPACITY_CRITICAL,
)

@dataclass
class Recommendation:
    type: str
    severity: str
    title: str
    message: str
    potential_savings_bytes: int = 0
    days_until_action: float | None = None
    metadata: dict | None = None


def format_bytes(value: float) -> str:
    """
    Convert bytes into a human-readable representation.
    """

    units = [
        "B",
        "KB",
        "MB",
        "GB",
        "TB",
        "PB",
    ]

    size = float(value)

    for unit in units:

        if size < 1024:
            return f"{size:.2f} {unit}"

        size /= 1024

    return f"{size:.2f} EB"

def format_days(days: float) -> str:
    """
    Convert a number of days into readable language.
    """

    if days < 1:
        hours = days * 24

        return f"{hours:.0f} hours"

    if days < 2:
        return "1 day"

    return f"{days:.0f} days"

def recommend_duplicates(
    duplicate_bytes: int,
) -> Recommendation | None:
    """
    Generate a recommendation based on duplicate data.
    """

    if duplicate_bytes < DUPLICATE_WARNING_BYTES:
        return None

    if duplicate_bytes >= DUPLICATE_HIGH_BYTES:

        severity = "high"

        title = (
            "Duplicate files are consuming significant storage"
        )

        message = (
            f"You have {format_bytes(duplicate_bytes)} "
            "in duplicate files. Review duplicate groups "
            "to reclaim storage space."
        )

    else:

        severity = "medium"

        title = (
            "Duplicate files are using extra storage"
        )

        message = (
            f"You have {format_bytes(duplicate_bytes)} "
            "in duplicate files. Reviewing duplicates "
            "could reclaim some storage."
        )

    return Recommendation(
        type="duplicate_cleanup",
        severity=severity,
        title=title,
        message=message,
        potential_savings_bytes=duplicate_bytes,
        metadata={
            "duplicate_bytes": duplicate_bytes,
        },
    )

def recommend_stale_files(
    stale_bytes: int,
    stale_days: int,
) -> Recommendation | None:
    """
    Generate a recommendation for inactive files.
    """

    if stale_bytes < STALE_WARNING_BYTES:
        return None

    if stale_bytes >= STALE_HIGH_BYTES:

        severity = "high"

    else:

        severity = "medium"

    title = (
        "Inactive files may be using valuable storage"
    )

    message = (
        f"{format_bytes(stale_bytes)} of files "
        f"have not been accessed for more than "
        f"{stale_days} days. Review old files, "
        "caches, downloads, and build artifacts."
    )

    return Recommendation(
        type="stale_cleanup",
        severity=severity,
        title=title,
        message=message,
        potential_savings_bytes=stale_bytes,
        metadata={
            "stale_bytes": stale_bytes,
            "stale_days": stale_days,
        },
    )

def recommend_growth(
    daily_growth_bytes: float,
) -> Recommendation | None:
    """
    Detect unusually high storage growth.
    """

    if daily_growth_bytes < HIGH_DAILY_GROWTH_BYTES:
        return None

    message = (
    f"Storage is growing at approximately "
    f"{format_bytes(daily_growth_bytes)} per day. "
    "Review recent files, logs, caches, and large "
    "downloads to identify the source of the growth."
    )

    return Recommendation(
        type="high_growth",
        severity="warning",
        title="Storage growth is unusually high",
        message=message,
        metadata={
            "daily_growth_bytes": daily_growth_bytes,
        },
    )

def recommend_capacity(
    utilization_percent: float,
    days_until_90: float | None,
    days_until_100: float | None,
) -> Recommendation | None:
    """
    Generate recommendations based on current and predicted
    capacity utilization.
    """

    # Already critically full
    if utilization_percent >= CAPACITY_CRITICAL_PERCENT:

        return Recommendation(
            type="capacity_critical",
            severity="critical",
            title="Storage is critically full",
            message=(
                f"Storage is currently at "
                f"{utilization_percent:.1f}% capacity. "
                "Free space immediately to avoid "
                "storage-related failures."
            ),
            days_until_action=0,
            metadata={
                "utilization_percent": utilization_percent,
            },
        )

    # Predicted to reach 100% very soon
    if (
        days_until_100 is not None
        and days_until_100 <= DAYS_TO_CAPACITY_CRITICAL
    ):

        return Recommendation(
            type="capacity_critical",
            severity="critical",
            title="Storage may become full soon",
            message=(
                "Storage is projected to reach full capacity "
                f"in approximately {format_days(days_until_100)}. "
                "Clean unnecessary files as soon as possible."
            ),
            days_until_action=days_until_100,
            metadata={
                "utilization_percent": utilization_percent,
                "days_until_100": days_until_100,
            },
        )

    # Predicted 90% within warning period
    if (
        days_until_90 is not None
        and days_until_90 <= DAYS_TO_CAPACITY_WARNING
    ):

        return Recommendation(
            type="capacity_warning",
            severity="warning",
            title="Storage capacity warning",
            message=(
                "Storage is projected to reach 90% capacity "
                f"in approximately {format_days(days_until_90)}. "
                "Consider cleaning unnecessary files."
            ),
            days_until_action=days_until_90,
            metadata={
                "utilization_percent": utilization_percent,
                "days_until_90": days_until_90,
            },
        )

    # Already above warning level
    if utilization_percent >= CAPACITY_WARNING_PERCENT:

        return Recommendation(
            type="capacity_warning",
            severity="warning",
            title="Storage usage is high",
            message=(
                f"Storage is currently at "
                f"{utilization_percent:.1f}% capacity. "
                "Consider freeing up space."
            ),
            metadata={
                "utilization_percent": utilization_percent,
            },
        )

    return None

def recommend_categories(
    category_stats: dict,
) -> list[Recommendation]:
    """
    Generate recommendations based on storage categories.
    """

    recommendations = []

    category_rules = {
        "temp": (
            "Temporary files are using storage",
            "Temporary data may be safely reviewable for cleanup.",
            "temp_cleanup",
        ),
        "system_cache": (
            "System cache is consuming storage",
            "Review system cache data to determine whether "
            "unused cache can be reclaimed.",
            "cache_cleanup",
        ),
        "system_log": (
            "System logs are consuming storage",
            "Review older logs and determine whether they "
            "can be archived or removed.",
            "log_cleanup",
        ),
        "crash_dump": (
            "Crash dumps are consuming storage",
            "Old crash dumps can become large. Review "
            "historical dumps that are no longer required.",
            "crash_dump_cleanup",
        ),
    }

    for category, data in category_stats.items():

        if category not in category_rules:
            continue

        if not isinstance(data, dict):
            continue

        bytes_used = data.get("bytes", 0)

        if bytes_used <= 0:
            continue

        title, advice, recommendation_type = (
            category_rules[category]
        )

        recommendations.append(
            Recommendation(
                type=recommendation_type,
                severity="info",
                title=title,
                message=(
                    f"{format_bytes(bytes_used)} "
                    f"of storage belongs to {category.replace('_', ' ')}. "
                    f"{advice}"
                ),
                metadata={
                    "category": category,
                    "bytes": bytes_used,
                },
            )
        )

    return recommendations

def generate_recommendations(
    duplicate_bytes: int = 0,
    stale_bytes: int = 0,
    stale_days: int = 30,
    daily_growth_bytes: float = 0,
    utilization_percent: float = 0,
    days_until_90: float | None = None,
    days_until_100: float | None = None,
    category_stats: dict | None = None,
) -> List[Recommendation]:
    """
    Generate all applicable storage recommendations.
    """

    recommendations = []

    duplicate = recommend_duplicates(
        duplicate_bytes
    )

    if duplicate:
        recommendations.append(
            duplicate
        )

    stale = recommend_stale_files(
        stale_bytes,
        stale_days,
    )

    if stale:
        recommendations.append(
            stale
        )

    growth = recommend_growth(
        daily_growth_bytes
    )

    if growth:
        recommendations.append(
            growth
        )

    capacity = recommend_capacity(
        utilization_percent,
        days_until_90,
        days_until_100,
    )

    if capacity:
        recommendations.append(
            capacity
        )

    if category_stats:
        category_recommendations = (
            recommend_categories(
                category_stats
            )
        )

        recommendations.extend(
            category_recommendations
        )

    # Critical recommendations first
    severity_order = {
        "critical": 0,
        "high": 1,
        "warning": 2,
        "medium": 3,
        "info": 4,
    }

    recommendations.sort(
        key=lambda recommendation:
            severity_order.get(
                recommendation.severity,
                99,
            )
    )

    return recommendations


