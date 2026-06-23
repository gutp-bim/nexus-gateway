# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Common Event formatting (ADR-0001)."""
from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from datetime import datetime, timezone


@dataclass
class CommonEvent:
    protocol: str
    connector_id: str
    local_id: str   # BACnet native address — never a resolved point_id
    device_ref: str
    value: float
    unit: str
    quality: str    # "Good" | "Bad" | "Uncertain"
    timestamp: str  # RFC3339 UTC


def make_event(
    connector_id: str,
    local_id: str,
    device_ref: str,
    value: float,
    unit: str,
    quality: str,
) -> bytes:
    evt = CommonEvent(
        protocol="bacnet",
        connector_id=connector_id,
        local_id=local_id,
        device_ref=device_ref,
        value=value,
        unit=unit,
        quality=quality,
        timestamp=datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    )
    return json.dumps(asdict(evt)).encode()


# BACnet status-code to Common Event quality mapping.
def bacnet_quality(status: str | None) -> str:
    """Map a BACnet status-code string to Common Event quality."""
    if status is None:
        return "Good"
    s = status.lower()
    if "no-error" in s or s == "":
        return "Good"
    if "comm-failure" in s or "unreachable" in s or "timeout" in s:
        return "Bad"
    return "Uncertain"
