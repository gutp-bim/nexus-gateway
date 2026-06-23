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
    value: float
    priority: int = 0  # 0 = use connector default

    @classmethod
    def from_bytes(cls, data: bytes) -> "ControlCommand":
        d = json.loads(data)
        return cls(
            control_id=d["control_id"],
            local_id=d["local_id"],
            device_ref=d.get("device_ref", ""),
            value=float(d["value"]),
            priority=int(d.get("priority", 0)),
        )


@dataclass
class WriteReply:
    success: bool
    response: str  # "ok" | "bad_request" | "not_writable" | "device_error: ..." | "timeout"

    def encode(self) -> bytes:
        return json.dumps({"success": self.success, "response": self.response}).encode()
