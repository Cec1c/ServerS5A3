"""Wait for evidence timestamps to become valid on the target game clock."""

from datetime import datetime
import math


EVIDENCE_MAX_AGE_SECONDS = 300.0
MAX_FUTURE_SKEW_SECONDS = 30.0
MIN_CLOCK_MARGIN_SECONDS = 0.05


def _remote_unix(remote_now):
    value = remote_now()
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError("remote_now must return finite Unix seconds")
    if not math.isfinite(value):
        raise ValueError("remote_now must return finite Unix seconds")
    return float(value)


def _parse_timestamps(timestamps):
    if isinstance(timestamps, (str, bytes)):
        raise ValueError("timestamps must be a non-empty list")
    try:
        values = list(timestamps)
    except TypeError as error:
        raise ValueError("timestamps must be a non-empty list") from error
    if not values:
        raise ValueError("timestamps must be a non-empty list")

    parsed = []
    for value in values:
        if not isinstance(value, str) or not value.strip():
            raise ValueError("evidence timestamps must be non-empty ISO strings")
        iso_value = value[:-1] + "+00:00" if value.endswith("Z") else value
        try:
            timestamp = datetime.fromisoformat(iso_value)
        except ValueError as error:
            raise ValueError("invalid evidence timestamp: " + value) from error
        if timestamp.tzinfo is None or timestamp.utcoffset() is None:
            raise ValueError("evidence timestamps must include a timezone")
        parsed.append(timestamp.timestamp())
    return parsed


def _ages(timestamps, remote_unix):
    return [remote_unix - timestamp for timestamp in timestamps]


def _require_valid(ages):
    if any(age < -MAX_FUTURE_SKEW_SECONDS or age > EVIDENCE_MAX_AGE_SECONDS for age in ages):
        raise ValueError("evidence is outside the target clock's -30..300 second window")


def wait_for_remote_evidence_clock(timestamps, remote_now, sleep, maximum_wait=30.0):
    """Return target-clock evidence ages after at most one bounded wait.

    ``sleep`` is called at most once. The timestamps are never changed, and an
    expired or still-future timestamp is rejected after the second clock read.
    """
    try:
        wait_limit = float(maximum_wait)
    except (TypeError, ValueError) as error:
        raise ValueError("maximum_wait must be between 0 and 30 seconds") from error
    if not math.isfinite(wait_limit) or not 0 < wait_limit <= MAX_FUTURE_SKEW_SECONDS:
        raise ValueError("maximum_wait must be between 0 and 30 seconds")

    parsed = _parse_timestamps(timestamps)
    initial_remote = _remote_unix(remote_now)
    initial_ages = _ages(parsed, initial_remote)
    _require_valid(initial_ages)

    oldest_age = max(initial_ages)
    newest_age = min(initial_ages)
    if newest_age >= 0:
        return {
            "waitedSeconds": 0.0,
            "remoteUnix": initial_remote,
            "oldestAgeSeconds": oldest_age,
            "newestAgeSeconds": newest_age,
        }

    future_skew = -newest_age
    requested_wait = min(wait_limit, future_skew + MIN_CLOCK_MARGIN_SECONDS)
    sleep(requested_wait)

    final_remote = _remote_unix(remote_now)
    final_ages = _ages(parsed, final_remote)
    if any(age < 0 or age > EVIDENCE_MAX_AGE_SECONDS for age in final_ages):
        raise ValueError("evidence did not become valid on the target clock after waiting")

    return {
        "waitedSeconds": max(0.0, final_remote - initial_remote),
        "remoteUnix": final_remote,
        "oldestAgeSeconds": max(final_ages),
        "newestAgeSeconds": min(final_ages),
    }
