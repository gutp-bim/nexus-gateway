# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Configuration loaded from environment variables."""
from __future__ import annotations

import json
import os
import re
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


@dataclass(frozen=True)
class ClockConfig:
    clock_id: str
    local_id: str
    property: str
    point_local_ids: tuple[str, ...]


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
    clock_revision: str = ""
    clocks: list[ClockConfig] = field(default_factory=list)

    def apply_clock_bundle(self, bundle: dict) -> None:
        """Startup-only native bundle, staged by Core's offline clock-config tool.

        Parse everything before replacing points/clocks. This is not a live apply
        API and never merges a clock revision with a separately supplied point list.
        """
        try:
            if not isinstance(bundle, dict):
                raise ValueError("bundle must be an object")
            if set(bundle) != {"revision", "connector_id", "device_id", "points", "clocks"}:
                raise ValueError("unsupported bundle keys")
            if bundle["connector_id"] != self.connector_id or type(bundle["device_id"]) is not int or bundle["device_id"] != self.bacnet_device_id:
                raise ValueError("connector/device scope mismatch")
            revision = bundle["revision"]
            if not isinstance(revision, str) or not re.fullmatch(r'"sha256:[0-9a-f]{64}"', revision):
                raise ValueError("invalid revision")
            if not isinstance(bundle["points"], list) or not isinstance(bundle["clocks"], list):
                raise ValueError("points and clocks must be arrays")
            for p in bundle["points"]:
                if not isinstance(p, dict) or set(p) != {"local_id", "device_ref", "unit", "writable"}:
                    raise ValueError("unsupported native point keys")
            points = _parse_points(json.dumps(bundle["points"]))
            point_ids = {p.local_id for p in points}
            if len(point_ids) != len(points):
                raise ValueError("duplicate native points")
            clocks, clock_ids, assigned, clock_objects = [], set(), set(), set()
            for c in bundle["clocks"]:
                if not isinstance(c, dict) or set(c) != {"clock_id", "local_id", "property", "point_local_ids"}:
                    raise ValueError("unsupported clock keys")
                if not isinstance(c["clock_id"], str) or not c["clock_id"] or c["clock_id"] in clock_ids:
                    raise ValueError("invalid/duplicate clock ID")
                match = re.fullmatch(r'(analogValue|analogInput),([0-9]+)', c["local_id"])
                if not match or int(match[2]) > 4194303 or c["property"] != "presentValue" or c["local_id"] in point_ids or c["local_id"] in clock_objects:
                    raise ValueError("invalid clock native reference")
                refs = c["point_local_ids"]
                if not isinstance(refs, list) or not refs or any(not isinstance(r, str) for r in refs):
                    raise ValueError("invalid clock point references")
                if len(set(refs)) != len(refs) or not set(refs) <= point_ids or assigned.intersection(refs):
                    raise ValueError("dangling/conflicting clock point references")
                clocks.append(ClockConfig(c["clock_id"], c["local_id"], c["property"], tuple(refs)))
                clock_ids.add(c["clock_id"])
                clock_objects.add(c["local_id"])
                assigned.update(refs)
            if clocks and self.cov_enabled:
                raise ValueError("clock acquisition requires BACNET_COV_ENABLED=false")
        except (ValueError, TypeError, KeyError) as exc:
            raise ConfigError(f"BACNET_CLOCK_CONFIG_FILE: {exc}") from None
        self.points, self.clocks, self.clock_revision = points, clocks, revision

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
        cfg = cls(
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
        path = os.environ.get("BACNET_CLOCK_CONFIG_FILE")
        if path:
            if "BACNET_POINTS" in os.environ:
                raise ConfigError("BACNET_CLOCK_CONFIG_FILE and BACNET_POINTS cannot be combined")
            try:
                with open(path, encoding="utf-8") as f:
                    bundle = json.load(f)
            except (OSError, ValueError) as exc:
                raise ConfigError(f"BACNET_CLOCK_CONFIG_FILE: {exc}") from None
            cfg.apply_clock_bundle(bundle)
        return cfg
