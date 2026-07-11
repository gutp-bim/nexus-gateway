# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Tests for the BACnet connector using injected mocks (no real BACnet or NATS)."""
from __future__ import annotations

import asyncio
import json
from typing import Any, Callable, Awaitable
from unittest.mock import AsyncMock

import pytest

from bacnet_connector.config import Config, PointConfig
from bacnet_connector.connector import BACnetClient, Connector
from bacnet_connector.event import bacnet_quality, make_event


# ── helpers ──────────────────────────────────────────────────────────────────

def make_config(
    points: list[PointConfig] | None = None,
    rpm_chunk_size: int = 20,
    cov_enabled: bool = True,
) -> Config:
    return Config(
        connector_id="test-conn",
        nats_url="nats://localhost:4222",
        bacnet_address="192.168.1.100",
        bacnet_device_id=42,
        points=points or [],
        poll_interval=999,  # large value — tests control timing via stop_event
        rpm_chunk_size=rpm_chunk_size,
        cov_enabled=cov_enabled,
    )


class MockBACnetClient(BACnetClient):
    """BACnetClient that returns canned poll results and records COV callbacks."""

    def __init__(self, poll_results: list[tuple[str, float | None, str | None]] | None = None):
        self.poll_results = poll_results or []
        self.poll_count = 0
        self.cov_callbacks: dict[str, Callable] = {}
        self.closed = False

    async def read_property_multiple(self, address, device_id, requests):
        self.poll_count += 1
        return self.poll_results

    async def subscribe_cov(self, address, device_id, obj_id, callback, lifetime=300):
        self.cov_callbacks[obj_id] = callback

    async def close(self):
        self.closed = True


class MockJetStream:
    """JetStreamContext stub that records published messages."""

    def __init__(self):
        self.published: list[tuple[str, bytes]] = []

    async def publish(self, subject: str, data: bytes):
        self.published.append((subject, data))


# ── unit tests ───────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_poll_publishes_event_for_each_point():
    """Initial poll must publish one Common Event per point."""
    points = [
        PointConfig(local_id="analogInput,0", device_ref="dev-1", unit="degC"),
        PointConfig(local_id="analogInput,1", device_ref="dev-1", unit="degC"),
    ]
    bacnet = MockBACnetClient(poll_results=[
        ("analogInput,0", 21.5, None),
        ("analogInput,1", 22.0, None),
    ])
    js = MockJetStream()
    cfg = make_config(points)
    stop = asyncio.Event()
    stop.set()  # stop after first poll cycle

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)

    assert len(js.published) == 2
    assert all(subj == "evt.bacnet.test-conn" for subj, _ in js.published)

    bodies = [json.loads(data) for _, data in js.published]
    local_ids = {b["local_id"] for b in bodies}
    assert local_ids == {"analogInput,0", "analogInput,1"}
    assert all(b["protocol"] == "bacnet" for b in bodies)
    assert all(b["connector_id"] == "test-conn" for b in bodies)


class ChunkRecordingClient(BACnetClient):
    """Records the requests of each read_property_multiple call (one call per chunk).

    Returns ``value_for(obj_id)`` for every requested point, so the number and
    composition of chunks the connector issues is observable. A chunk whose start
    index is in ``fail_chunks`` raises, simulating a per-chunk device error.
    """

    def __init__(self, fail_chunks: set[int] | None = None):
        self.calls: list[list[str]] = []  # obj_ids per call, in order
        self._fail_chunks = fail_chunks or set()

    async def read_property_multiple(self, address, device_id, requests):
        obj_ids = [obj_id for obj_id, _ in requests]
        self.calls.append(obj_ids)
        if (len(self.calls) - 1) in self._fail_chunks:
            raise OSError("simulated chunk failure")
        return [(obj_id, float(i), None) for i, obj_id in enumerate(obj_ids)]

    async def subscribe_cov(self, address, device_id, obj_id, callback, lifetime=300):
        pass

    async def close(self):
        pass


def _points(n: int) -> list[PointConfig]:
    return [PointConfig(local_id=f"analogInput,{i}", device_ref="d") for i in range(n)]


@pytest.mark.asyncio
async def test_poll_chunks_requests():
    """Points must be read in chunks of rpm_chunk_size; every point still publishes."""
    bacnet = ChunkRecordingClient()
    js = MockJetStream()
    cfg = make_config(_points(5), rpm_chunk_size=2)
    stop = asyncio.Event()
    stop.set()  # stop after first poll cycle

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)

    # 5 points / chunk 2 → 3 RPM calls of sizes [2, 2, 1].
    assert [len(c) for c in bacnet.calls] == [2, 2, 1]
    # Chunks cover every point exactly once, in order.
    assert [oid for c in bacnet.calls for oid in c] == [f"analogInput,{i}" for i in range(5)]
    # All five points published.
    published_ids = {json.loads(d)["local_id"] for _, d in js.published}
    assert published_ids == {f"analogInput,{i}" for i in range(5)}


@pytest.mark.asyncio
async def test_poll_chunk_failure_is_isolated():
    """A failed chunk must not prevent the other chunks from publishing."""
    # 5 points, chunk 2 → chunks [0:2], [2:4], [4:5]; fail the middle chunk (index 1).
    bacnet = ChunkRecordingClient(fail_chunks={1})
    js = MockJetStream()
    cfg = make_config(_points(5), rpm_chunk_size=2)
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)

    # Surviving chunks publish points 0,1 (chunk 0) and 4 (chunk 2) — point 2,3 dropped.
    published_ids = {json.loads(d)["local_id"] for _, d in js.published}
    assert published_ids == {"analogInput,0", "analogInput,1", "analogInput,4"}


@pytest.mark.asyncio
async def test_cov_disabled_skips_subscriptions():
    """With cov_enabled=False the connector must not open any COV subscription."""
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", 1.0, None)])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")], cov_enabled=False)
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)

    assert bacnet.cov_callbacks == {}      # no subscribe_cov calls
    assert len(js.published) == 1          # polling still publishes


@pytest.mark.asyncio
async def test_poll_skips_none_values():
    """Points with None value (e.g. BACnet error) must not publish events."""
    bacnet = MockBACnetClient(poll_results=[
        ("analogInput,0", None, "comm-failure"),
    ])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)

    assert js.published == []


@pytest.mark.asyncio
async def test_configured_none_value_warns_once_within_window(caplog):
    """A configured point returning None warns once (naming local_id) across
    repeated polls inside the rate-limit window, and never publishes."""
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", None, "comm-failure")])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])

    conn = Connector(cfg, bacnet, js)
    conn._bad_value_warn_window = 300  # keep the default window so repeats are suppressed

    with caplog.at_level("WARNING", logger="bacnet_connector.connector"):
        for _ in range(5):
            await conn._poll_all()

    warnings = [
        r for r in caplog.records
        if r.levelname == "WARNING" and "analogInput,0" in r.getMessage()
    ]
    assert len(warnings) == 1
    assert js.published == []  # non-numeric value is never published


@pytest.mark.asyncio
async def test_configured_none_value_warns_again_after_window(caplog):
    """Once the rate-limit window elapses, the next poll warns again."""
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", None, "comm-failure")])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])

    conn = Connector(cfg, bacnet, js)
    conn._bad_value_warn_window = 0  # window elapses immediately → every poll warns

    with caplog.at_level("WARNING", logger="bacnet_connector.connector"):
        await conn._poll_all()
        await conn._poll_all()

    warnings = [
        r for r in caplog.records
        if r.levelname == "WARNING" and "analogInput,0" in r.getMessage()
    ]
    assert len(warnings) == 2


@pytest.mark.asyncio
async def test_unconfigured_none_value_does_not_warn(caplog):
    """An unconfigured point (pt is None) is still skipped silently — no WARNING."""
    # "analogInput,9" is returned by the device but not in the configured points.
    bacnet = MockBACnetClient(poll_results=[("analogInput,9", None, "comm-failure")])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])

    conn = Connector(cfg, bacnet, js)
    with caplog.at_level("WARNING", logger="bacnet_connector.connector"):
        await conn._poll_all()

    assert [r for r in caplog.records if r.levelname == "WARNING"] == []
    assert js.published == []


@pytest.mark.asyncio
async def test_cov_callback_publishes_event():
    """A COV notification must publish a Common Event to NATS."""
    pt = PointConfig(local_id="analogInput,0", device_ref="dev-1", unit="degC")
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", 20.0, None)])
    js = MockJetStream()
    cfg = make_config([pt])
    stop = asyncio.Event()

    conn = Connector(cfg, bacnet, js)

    # Run connector in background, then fire a COV callback, then stop.
    task = asyncio.create_task(conn.run(stop_event=stop))

    # Yield until the connector has registered the COV subscription.
    async def _cov_ready() -> None:
        while "analogInput,0" not in bacnet.cov_callbacks:
            await asyncio.sleep(0)

    await asyncio.wait_for(_cov_ready(), timeout=1.0)

    # Fire COV with a new value
    cb = bacnet.cov_callbacks.get("analogInput,0")
    assert cb is not None, "Connector must subscribe to COV for the point"
    await cb("analogInput,0", 25.5, "")

    stop.set()
    await task

    # At least the COV-triggered event must be present.
    cov_events = [
        json.loads(data)
        for _, data in js.published
        if json.loads(data).get("value") == 25.5
    ]
    assert len(cov_events) == 1
    assert cov_events[0]["local_id"] == "analogInput,0"
    assert cov_events[0]["quality"] == "Good"


@pytest.mark.asyncio
async def test_nats_publish_failure_does_not_crash():
    """A transient NATS publish failure must be logged and swallowed — connector keeps running."""
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", 21.0, None)])

    class FlakyJetStream(MockJetStream):
        def __init__(self):
            super().__init__()
            self._call = 0

        async def publish(self, subject: str, data: bytes):
            self._call += 1
            if self._call == 1:
                raise ConnectionError("nats: temporary failure")
            self.published.append((subject, data))

    flaky_js = FlakyJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, flaky_js)
    # Should not raise even though first publish fails.
    await conn.run(stop_event=stop)


@pytest.mark.asyncio
async def test_poll_transport_failure_is_tolerated():
    """A failed ReadPropertyMultiple call must be logged and not crash the connector."""
    class FailingClient(BACnetClient):
        async def read_property_multiple(self, *a, **kw):
            raise OSError("unreachable")
        async def subscribe_cov(self, *a, **kw):
            pass
        async def close(self):
            pass

    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, FailingClient(), js)
    await conn.run(stop_event=stop)  # must not raise

    assert js.published == []


# ── health / reachability tests ───────────────────────────────────────────────

@pytest.mark.asyncio
async def test_healthy_false_before_first_poll():
    """A freshly constructed connector is unhealthy until the first successful poll."""
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", 1.0, None)])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])
    conn = Connector(cfg, bacnet, js)
    assert conn.healthy() is False


@pytest.mark.asyncio
async def test_healthy_true_after_successful_poll():
    """After a poll cycle with at least one successful chunk read, healthy() is True."""
    bacnet = MockBACnetClient(poll_results=[("analogInput,0", 1.0, None)])
    js = MockJetStream()
    cfg = make_config([PointConfig(local_id="analogInput,0", device_ref="d")])
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)
    assert conn.healthy() is True


@pytest.mark.asyncio
async def test_healthy_false_when_all_chunks_fail():
    """If every chunk read raises in a poll cycle, healthy() is False."""
    # 3 points, chunk 1 → 3 chunks, all failing.
    bacnet = ChunkRecordingClient(fail_chunks={0, 1, 2})
    js = MockJetStream()
    cfg = make_config(_points(3), rpm_chunk_size=1)
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)
    assert conn.healthy() is False


@pytest.mark.asyncio
async def test_healthy_true_when_any_chunk_succeeds():
    """A single failed chunk must not flip healthy False if another chunk succeeds."""
    # 3 points, chunk 1 → 3 chunks; fail only the first.
    bacnet = ChunkRecordingClient(fail_chunks={0})
    js = MockJetStream()
    cfg = make_config(_points(3), rpm_chunk_size=1)
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)
    assert conn.healthy() is True


@pytest.mark.asyncio
async def test_healthy_true_with_no_points():
    """With no points configured there is nothing to poll — healthy once the stack is up."""
    bacnet = MockBACnetClient()
    js = MockJetStream()
    cfg = make_config([])
    stop = asyncio.Event()
    stop.set()

    conn = Connector(cfg, bacnet, js)
    await conn.run(stop_event=stop)
    assert conn.healthy() is True


# ── event module tests ────────────────────────────────────────────────────────

def test_make_event_fields():
    data = json.loads(make_event("c1", "analogInput,0", "dev-1", 22.5, "degC", "Good"))
    assert data["protocol"] == "bacnet"
    assert data["connector_id"] == "c1"
    assert data["local_id"] == "analogInput,0"
    assert data["device_ref"] == "dev-1"
    assert data["value"] == 22.5
    assert data["unit"] == "degC"
    assert data["quality"] == "Good"
    assert data["timestamp"].endswith("Z")


def test_bacnet_quality_mapping():
    assert bacnet_quality(None) == "Good"
    assert bacnet_quality("no-error") == "Good"
    assert bacnet_quality("comm-failure") == "Bad"
    assert bacnet_quality("unreachable") == "Bad"
    assert bacnet_quality("timeout") == "Bad"
    assert bacnet_quality("unknown-error") == "Uncertain"


def test_make_event_local_id_is_not_subject():
    """The NATS subject must not contain the native local_id (ADR-0001)."""
    # make_event just creates the payload — the subject is assigned by the connector.
    # This test verifies the event payload local_id field is present and un-modified.
    data = json.loads(make_event("c1", "analogInput,3", "dev", 0.0, "", "Good"))
    assert data["local_id"] == "analogInput,3"
    # Native addressing stays in the payload, never leaks into the NATS subject.
    # The connector publishes to "evt.bacnet.c1", tested in test_poll_publishes_event_for_each_point.
