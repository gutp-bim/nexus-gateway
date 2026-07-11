# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Configuration loaded from environment variables."""
from __future__ import annotations

import json
import os
from dataclasses import dataclass, field


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
            raise ValueError(f"poll_interval must be positive, got {self.poll_interval}")
        if self.rpm_chunk_size < 1:
            raise ValueError(f"rpm_chunk_size must be >= 1, got {self.rpm_chunk_size}")
        if self.read_timeout <= 0:
            raise ValueError(f"read_timeout must be positive, got {self.read_timeout}")
        if not 1 <= self.default_write_priority <= 16:
            raise ValueError(f"default_write_priority must be 1–16, got {self.default_write_priority}")
        if self.write_timeout <= 0:
            raise ValueError(f"write_timeout must be positive, got {self.write_timeout}")

    @classmethod
    def from_env(cls) -> "Config":
        points_raw = os.environ.get("BACNET_POINTS", "[]")
        points = [
            PointConfig(
                local_id=p["local_id"],
                device_ref=p.get("device_ref", ""),
                unit=p.get("unit", ""),
                writable=p.get("writable", False),
            )
            for p in json.loads(points_raw)
        ]
        return cls(
            connector_id=os.environ["CONNECTOR_ID"],
            nats_url=os.environ.get("NATS_URL", "nats://localhost:4222"),
            bacnet_address=os.environ["BACNET_ADDRESS"],
            bacnet_device_id=int(os.environ["BACNET_DEVICE_ID"]),
            points=points,
            poll_interval=float(os.environ.get("BACNET_POLL_INTERVAL", "60")),
            local_address=os.environ.get("BACNET_LOCAL_ADDRESS", "0.0.0.0"),
            rpm_chunk_size=int(os.environ.get("BACNET_RPM_CHUNK_SIZE", "20")),
            read_timeout=float(os.environ.get("BACNET_READ_TIMEOUT", "5")),
            cov_enabled=os.environ.get("BACNET_COV_ENABLED", "true").strip().lower()
            not in ("0", "false", "no"),
            default_write_priority=int(os.environ.get("BACNET_DEFAULT_WRITE_PRIORITY", "8")),
            write_timeout=float(os.environ.get("BACNET_WRITE_TIMEOUT", "10")),
            health_port=int(os.environ.get("HEALTH_PORT", "8080")),
        )
