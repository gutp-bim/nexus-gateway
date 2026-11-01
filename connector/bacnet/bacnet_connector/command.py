# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""ControlCommand / WriteReply wire types for the BACnet Command Channel (ADR-0004)."""
from __future__ import annotations

import json
from dataclasses import dataclass


@dataclass
class ControlCommand:
    control_id: str
    local_id: str
    device_ref: str
    value: float | None = None
    priority: int = 0  # 0 = use connector default

    release: bool = False

    @classmethod
    def from_bytes(cls, data: bytes) -> "ControlCommand":
        d = json.loads(data)
        release = d.get("release", False)
        if not isinstance(release, bool):
            raise ValueError("release must be boolean")
        priority = d.get("priority", 0)
        if type(priority) is not int or not 0 <= priority <= 16:
            raise ValueError("priority must be 0–16")
        return cls(
            control_id=d["control_id"],
            local_id=d["local_id"],
            device_ref=d.get("device_ref", ""),
            value=None if release else float(d["value"]),
            priority=priority,
            release=release,
        )


@dataclass
class WriteReply:
    success: bool
    response: str  # "ok" | "bad_request" | "not_writable" | "device_error: ..." | "timeout"

    def encode(self) -> bytes:
        return json.dumps({"success": self.success, "response": self.response}).encode()
