# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Tests for the BACnet write handler (Command Channel, ADR-0004)."""
from __future__ import annotations

import asyncio
import dataclasses
import json

import pytest

from bacnet_connector.command import ControlCommand, WriteReply
from bacnet_connector.config import Config, PointConfig
from bacnet_connector.connector import BACnetClient
from bacnet_connector.write_handler import WriteHandler


# ── helpers ───────────────────────────────────────────────────────────────────

def make_config(points: list[PointConfig] | None = None) -> Config:
    return Config(
        connector_id="test-conn",
        nats_url="nats://localhost:4222",
        bacnet_address="192.168.1.100",
        bacnet_device_id=42,
        points=points or [],
        poll_interval=30,
        default_write_priority=8,
        write_timeout=10.0,
    )


class MockBACnetClient(BACnetClient):
    def __init__(self, write_raises: Exception | None = None, write_delay: float = 0):
        self.writes: list[tuple] = []
        self._write_raises = write_raises
        self._write_delay = write_delay

    async def read_property_multiple(self, *a, **kw):
        return []

    async def subscribe_cov(self, *a, **kw):
        pass

    async def write_property(self, address, device_id, obj_id, prop_id, value, priority):
        if self._write_delay:
            await asyncio.sleep(self._write_delay)
        if self._write_raises:
            raise self._write_raises
        self.writes.append((obj_id, prop_id, value, priority))


class MockMsg:
    """Minimal stand-in for a NATS Msg."""
    def __init__(self, data: bytes):
        self.data = data
        self._replies: list[bytes] = []

    async def respond(self, data: bytes) -> None:
        self._replies.append(data)

    def reply_json(self) -> dict:
        assert self._replies, "no reply sent"
        return json.loads(self._replies[-1])


def cmd_msg(
    control_id: str = "ctrl-1",
    local_id: str = "analogOutput,0",
    value: float = 21.5,
    priority: int = 0,
) -> MockMsg:
    payload = json.dumps({
        "control_id": control_id,
        "local_id": local_id,
        "device_ref": "dev-1",
        "value": value,
        "priority": priority,
    }).encode()
    return MockMsg(payload)


def writable_point() -> PointConfig:
    return PointConfig(local_id="analogOutput,0", device_ref="dev-1", unit="degC", writable=True)


def readonly_point() -> PointConfig:
    return PointConfig(local_id="analogInput,0", device_ref="dev-1", unit="degC", writable=False)


# ── tests ─────────────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_write_ok_replies_success():
    """Successful WriteProperty returns success=True, response='ok'."""
    bacnet = MockBACnetClient()
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg()
    await handler.handle(msg)

    reply = msg.reply_json()
    assert reply["success"] is True
    assert reply["response"] == "ok"
    assert len(bacnet.writes) == 1
    assert bacnet.writes[0] == ("analogOutput,0", "presentValue", 21.5, 8)  # default priority


@pytest.mark.asyncio
async def test_write_device_error_replies_device_error():
    """A BACnet error returns success=False with device_error response."""
    bacnet = MockBACnetClient(write_raises=OSError("BACnet: object not found"))
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg()
    await handler.handle(msg)

    reply = msg.reply_json()
    assert reply["success"] is False
    assert "device_error" in reply["response"]


@pytest.mark.asyncio
async def test_write_timeout_replies_timeout():
    """A write that exceeds write_timeout returns success=False with 'timeout'."""
    bacnet = MockBACnetClient(write_delay=99)
    cfg = Config(
        connector_id="test-conn",
        nats_url="nats://localhost:4222",
        bacnet_address="192.168.1.100",
        bacnet_device_id=42,
        points=[writable_point()],
        poll_interval=30,
        default_write_priority=8,
        write_timeout=0.05,  # very short
    )
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg()
    await handler.handle(msg)

    reply = msg.reply_json()
    assert reply["success"] is False
    assert reply["response"] == "timeout"


@pytest.mark.asyncio
async def test_priority_zero_uses_default_write_priority():
    """priority=0 in command falls back to config.default_write_priority."""
    bacnet = MockBACnetClient()
    cfg = dataclasses.replace(make_config([writable_point()]), default_write_priority=12)
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg(priority=0)
    await handler.handle(msg)

    assert bacnet.writes[0][3] == 12  # priority slot


@pytest.mark.asyncio
async def test_explicit_priority_used_as_is():
    """Non-zero priority in command is used directly."""
    bacnet = MockBACnetClient()
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg(priority=3)
    await handler.handle(msg)

    assert bacnet.writes[0][3] == 3


@pytest.mark.asyncio
async def test_write_unknown_local_id_replies_not_writable():
    """Command for an unknown local_id is rejected without calling BACnet."""
    bacnet = MockBACnetClient()
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg(local_id="analogInput,99")
    await handler.handle(msg)

    reply = msg.reply_json()
    assert reply["success"] is False
    assert len(bacnet.writes) == 0


@pytest.mark.asyncio
async def test_write_non_writable_point_rejected():
    """Command for a point with writable=False is rejected without calling BACnet."""
    bacnet = MockBACnetClient()
    cfg = make_config([readonly_point()])
    handler = WriteHandler(cfg, bacnet)

    msg = cmd_msg(local_id="analogInput,0")
    await handler.handle(msg)

    reply = msg.reply_json()
    assert reply["success"] is False
    assert len(bacnet.writes) == 0


@pytest.mark.asyncio
async def test_duplicate_control_id_no_double_write():
    """Second delivery of the same control_id returns cached result without re-writing."""
    bacnet = MockBACnetClient()
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg1 = cmd_msg(control_id="ctrl-dup")
    msg2 = cmd_msg(control_id="ctrl-dup")

    await handler.handle(msg1)
    await handler.handle(msg2)

    assert len(bacnet.writes) == 1, "device must only be written once per control_id"
    assert msg2.reply_json() == msg1.reply_json()


@pytest.mark.asyncio
async def test_malformed_command_replied_with_bad_request():
    """Unparseable payload replies bad_request without crashing."""
    bacnet = MockBACnetClient()
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg = MockMsg(b"not json at all")
    await handler.handle(msg)

    reply = msg.reply_json()
    assert reply["success"] is False
    assert "bad_request" in reply["response"]


@pytest.mark.asyncio
async def test_in_flight_duplicate_replies_in_flight():
    """A duplicate arriving while the first write is in flight replies in_flight
    immediately (not silently dropped), so the Command Channel does not stall (#30)."""
    bacnet = MockBACnetClient(write_delay=0.1)
    cfg = make_config([writable_point()])
    handler = WriteHandler(cfg, bacnet)

    msg1 = cmd_msg(control_id="ctrl-race")
    msg2 = cmd_msg(control_id="ctrl-race")

    task1 = asyncio.create_task(handler.handle(msg1))
    await asyncio.sleep(0.01)  # let msg1 reserve the sentinel and enter the (delayed) write
    await handler.handle(msg2)  # duplicate while msg1 is still in flight

    reply2 = msg2.reply_json()
    assert reply2["success"] is False
    assert reply2["response"] == "in_flight"

    await task1
    assert msg1.reply_json()["response"] == "ok"
    assert len(bacnet.writes) == 1, "the in-flight original still writes exactly once"


def test_nats_connect_kwargs_configures_unlimited_reconnect():
    """The connector must reconnect indefinitely (#30), not stop at the nats-py default cap."""
    from bacnet_connector.main import nats_connect_kwargs

    kwargs = nats_connect_kwargs()
    assert kwargs["max_reconnect_attempts"] == -1
    assert kwargs["reconnect_time_wait"] > 0
