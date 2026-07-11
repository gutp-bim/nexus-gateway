# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Configuration loaded from environment variables."""
from __future__ import annotations

import json
import os
from dataclasses import dataclass, field


class ConfigError(ValueError):
    """A configuration problem worded for an operator (no traceback needed).

    Subclasses ``ValueError`` so range checks in ``__post_init__`` and callers
    that already expect ``ValueError`` keep working; ``main`` catches this to exit
    non-zero with a single named line instead of a raw stack trace.
    """


def _require(key: str) -> str:
    """Return a required env var's value or raise a named ConfigError."""
    value = os.environ.get(key)
    if value is None or value == "":
        raise ConfigError(f"Missing required environment variable: {key}")
    return value


def _env_int(key: str, default: str) -> int:
    raw = os.environ.get(key, default)
    try:
        return int(raw)
    except ValueError:
        raise ConfigError(f"{key} must be an integer, got {raw!r}") from None


def _env_float(key: str, default: str) -> float:
    raw = os.environ.get(key, default)
    try:
        return float(raw)
    except ValueError:
        raise ConfigError(f"{key} must be a number, got {raw!r}") from None


def _require_int(key: str) -> int:
    raw = _require(key)
    try:
        return int(raw)
    except ValueError:
        raise ConfigError(f"{key} must be an integer, got {raw!r}") from None


def _parse_points(raw: str) -> list[PointConfig]:
    """Parse BACNET_POINTS JSON, naming the offending index/key on failure."""
    try:
        entries = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ConfigError(f"BACNET_POINTS: invalid JSON: {exc}") from None
    if not isinstance(entries, list):
        raise ConfigError(f"BACNET_POINTS: must be a JSON array, got {type(entries).__name__}")

    points: list[PointConfig] = []
    for i, entry in enumerate(entries):
        if not isinstance(entry, dict):
            raise ConfigError(f"BACNET_POINTS[{i}]: must be a JSON object, got {type(entry).__name__}")
        local_id = entry.get("local_id")
        if local_id is None:
            raise ConfigError(f"BACNET_POINTS[{i}]: missing required key 'local_id'")
        if not isinstance(local_id, str) or local_id == "":
            raise ConfigError(f"BACNET_POINTS[{i}]: 'local_id' must be a non-empty string, got {local_id!r}")
        for key in ("device_ref", "unit"):
            if key in entry and not isinstance(entry[key], str):
                raise ConfigError(f"BACNET_POINTS[{i}]: '{key}' must be a string, got {entry[key]!r}")
        if "writable" in entry and not isinstance(entry["writable"], bool):
            raise ConfigError(f"BACNET_POINTS[{i}]: 'writable' must be a boolean, got {entry['writable']!r}")
        points.append(
            PointConfig(
                local_id=local_id,
                device_ref=entry.get("device_ref", ""),
                unit=entry.get("unit", ""),
                writable=entry.get("writable", False),
            )
        )
    return points


@dataclass
class PointConfig:
    local_id: str        # BACnet native address: "<objtype>,<instance>" e.g. "analogInput,0"
    device_ref: str      # opaque device reference for the Normalizer
    unit: str = ""       # engineering unit, e.g. "degC"
    writable: bool = False


@dataclass
class Config:
    connector_id: str
    nats_url: str
    bacnet_address: str  # target BACnet device IP address or "host/24"
    bacnet_device_id: int
    points: list[PointConfig] = field(default_factory=list)
    poll_interval: float = 60.0          # seconds between full polls (1-min freshness floor)
    local_address: str = "0.0.0.0"      # local BACnet interface address
    default_write_priority: int = 8     # BACnet write priority (1=highest, 16=lowest)
    write_timeout: float = 10.0         # seconds before a write is declared timed-out
    # Max points per ReadPropertyMultiple request. A single RPM for many points can
    # overflow the device's APDU; devices without segmentation then reject the whole
    # request with "segmentation-not-supported". Polling in chunks keeps each response
    # small enough to fit. Tune to the device's max APDU (smaller = safer, more round-trips).
    rpm_chunk_size: int = 20
    # Per-read deadline. bacpypes3 reads have no built-in timeout: a slow or
    # unresponsive device would otherwise hang the poll loop forever. On timeout the
    # chunk yields no values and polling continues on the next cycle.
    read_timeout: float = 5.0
    # Whether to open a per-point COV (change-of-value) subscription in addition to
    # polling. Each subscription is a long-lived session; thousands of them can
    # overwhelm a device. Disable (poll-only) for large point counts.
    cov_enabled: bool = True
    # Port for the stdlib health HTTP server (GET /health). Device-reachability proxy.
    health_port: int = 8080

    def __post_init__(self) -> None:
        if self.poll_interval <= 0:
            raise ConfigError(f"BACNET_POLL_INTERVAL must be positive, got {self.poll_interval}")
        if self.rpm_chunk_size < 1:
            raise ConfigError(f"BACNET_RPM_CHUNK_SIZE must be >= 1, got {self.rpm_chunk_size}")
        if self.read_timeout <= 0:
            raise ConfigError(f"BACNET_READ_TIMEOUT must be positive, got {self.read_timeout}")
        if not 1 <= self.default_write_priority <= 16:
            raise ConfigError(f"BACNET_DEFAULT_WRITE_PRIORITY must be 1–16, got {self.default_write_priority}")
        if self.write_timeout <= 0:
            raise ConfigError(f"BACNET_WRITE_TIMEOUT must be positive, got {self.write_timeout}")

    @classmethod
    def from_env(cls) -> "Config":
        """Build a Config from the environment, failing fast with a named error.

        Every required variable and per-point parse error raises ConfigError with
        a one-line, operator-facing message identifying the offender; main catches
        it and exits non-zero without a traceback. Mirrors the MQTT connector's
        startup validation.
        """
        return cls(
            connector_id=_require("CONNECTOR_ID"),
            nats_url=os.environ.get("NATS_URL", "nats://localhost:4222"),
            bacnet_address=_require("BACNET_ADDRESS"),
            bacnet_device_id=_require_int("BACNET_DEVICE_ID"),
            points=_parse_points(os.environ.get("BACNET_POINTS", "[]")),
            poll_interval=_env_float("BACNET_POLL_INTERVAL", "60"),
            local_address=os.environ.get("BACNET_LOCAL_ADDRESS", "0.0.0.0"),
            rpm_chunk_size=_env_int("BACNET_RPM_CHUNK_SIZE", "20"),
            read_timeout=_env_float("BACNET_READ_TIMEOUT", "5"),
            cov_enabled=os.environ.get("BACNET_COV_ENABLED", "true").strip().lower()
            not in ("0", "false", "no"),
            default_write_priority=_env_int("BACNET_DEFAULT_WRITE_PRIORITY", "8"),
            write_timeout=_env_float("BACNET_WRITE_TIMEOUT", "10"),
            health_port=_env_int("HEALTH_PORT", "8080"),
        )
