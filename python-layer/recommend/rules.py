"""
Thresholds and rules used by the recommendation engine.
"""

# ---------------------------------------------------------
# Storage capacity thresholds
# ---------------------------------------------------------

CAPACITY_WARNING_PERCENT = 80.0
CAPACITY_CRITICAL_PERCENT = 90.0


# ---------------------------------------------------------
# Growth thresholds
# ---------------------------------------------------------

# Daily growth above this is considered significant.
HIGH_DAILY_GROWTH_BYTES = 2 * 1024 ** 3


# ---------------------------------------------------------
# Duplicate thresholds
# ---------------------------------------------------------

DUPLICATE_WARNING_BYTES = 1 * 1024 ** 3
DUPLICATE_HIGH_BYTES = 5 * 1024 ** 3


# ---------------------------------------------------------
# Stale file thresholds
# ---------------------------------------------------------

STALE_WARNING_BYTES = 1 * 1024 ** 3
STALE_HIGH_BYTES = 5 * 1024 ** 3


# ---------------------------------------------------------
# Forecast thresholds
# ---------------------------------------------------------

DAYS_TO_CAPACITY_WARNING = 30
DAYS_TO_CAPACITY_CRITICAL = 14


# ---------------------------------------------------------
# Staleness thresholds
# ---------------------------------------------------------

DEFAULT_STALE_DAYS = 30
HIGH_STALE_DAYS = 60