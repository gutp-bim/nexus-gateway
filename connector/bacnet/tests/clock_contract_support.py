# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Offline test bridge: real connector output and separately recorded evidence.

Invoked by the Go contract test over stdin/stdout. No sockets, device clients,
run-context delivery, or production evidence fields are introduced here.
"""
import asyncio
import copy
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import sys
from types import ModuleType
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
CONTRACT = ROOT / "test/contracts/ri-clock-v2"


def oracle():
    module = ModuleType("ri_clock_time_contract")
    source = CONTRACT / "time_contract.py"
    # Execute the pinned source without writing __pycache__ beside the fixture.
    exec(compile(source.read_bytes(), str(source), "exec"), module.__dict__)
    return module


def read_fixture(name):
    return json.loads((CONTRACT / "fixtures" / name).read_text())


class AcquisitionTime:
    # Every poll begins after a fractional second. Seconds truncation would put
    # the equipment timestamp BEFORE the acquisition interval and fail RI checks.
    current = datetime(2026, 1, 1, 0, 0, 4, 125000, tzinfo=timezone.utc)

    @classmethod
    def now(cls, tz):
        cls.current += timedelta(microseconds=100)
        return cls.current

    @classmethod
    def stamp(cls):
        return cls.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


class Responses:
    def __init__(self, cfg, samples, *, unit="seconds", partial=False):
        self.device_id = cfg.bacnet_device_id
        self.values = {p.local_id: i + 0.5 for i, p in enumerate(cfg.points)}
        clock = cfg.clocks[0]
        self.clock = clock
        self.samples = iter(samples)
        self.unit = unit
        self.partial = partial
        self.equipment_reads = 0

    async def read_property_multiple(self, address, device_id, requests):
        assert device_id == self.device_id and address == "192.0.2.1"
        obj, prop = requests[0]
        if obj == self.clock.local_id:
            return [(obj, self.unit if prop == "units" else next(self.samples), None)]
        self.equipment_reads += 1
        if self.partial and self.equipment_reads == 1:
            # Successful values retain their equipment quality independently of
            # the failed equipment value and the clock association's quality.
            return [(obj, None if i == 0 else self.values[obj], "timeout" if i == 1 else None)
                    for i, (obj, _) in enumerate(requests)]
        return [(obj, self.values[obj], None) for obj, _ in requests]


class Publisher:
    def __init__(self):
        self.events = []

    async def publish(self, subject, data):
        self.events.append(json.loads(data))


async def generate(bundle):
    # Imports are intentionally deferred so the oracle-only tests need no extra
    # services or BACnet adapter initialization.
    from bacnet_connector.config import Config
    from bacnet_connector.connector import Connector
    from bacnet_connector.command import ControlCommand
    from bacnet_connector.write_handler import WriteHandler

    cfg = Config(bundle["connector_id"], "unused", "192.0.2.1", bundle["device_id"],
                 cov_enabled=False, rpm_chunk_size=20)
    cfg.apply_clock_bundle(bundle)
    assert len(cfg.points) == 35 and len(cfg.clocks) == 1
    assert "BPT-" not in json.dumps(bundle) and "point_id" not in json.dumps(bundle)
    # Exercise the real write gate exclusively against an injected recorder.
    # No BACnet adapter, transport, or real equipment is constructed.
    class Writes:
        def __init__(self):
            self.targets = []

        async def write_property(self, address, device_id, local_id, prop, value, priority):
            assert address == cfg.bacnet_address and device_id == cfg.bacnet_device_id
            self.targets.append(local_id)

    writes = Writes()
    handler = WriteHandler(cfg, writes)
    for point in cfg.points:
        result = await handler._execute(ControlCommand("test-write", point.local_id, point.device_ref, 0.5))
        assert result.success == point.writable
        assert result.response == ("ok" if point.writable else "not_writable")
    clock_result = await handler._execute(ControlCommand("test-clock", cfg.clocks[0].local_id, "", 0.5))
    assert not clock_result.success and clock_result.response == "not_writable"
    assert writes.targets == [p.local_id for p in cfg.points if p.writable]
    assert cfg.clocks[0].local_id not in writes.targets
    publisher = Publisher()
    cases = []

    def connector():
        return Connector(cfg, None, publisher)

    async def poll(conn, name, samples, expected, **options):
        before = len(publisher.events)
        previous = conn._last_clock.get(cfg.clocks[0].clock_id)
        reset = cfg.clocks[0].clock_id in conn._reset_clocks
        conn._bacnet = Responses(cfg, samples, **options)
        recorded = []
        original = conn._read_clock

        async def read_clock(clock):
            value = await original(clock)
            recorded.append(None if value is None else str(value))
            return value

        started = AcquisitionTime.stamp()
        with patch.object(conn, "_read_clock", read_clock):
            await conn._poll_all()
        acquisition = {
            "startedAt": started, "endedAt": AcquisitionTime.stamp(),
            "chunkCount": conn._bacnet.equipment_reads, "clockSamples": recorded,
            "resetLatched": reset, "contextChanged": False,
            "previousClockValue": None if previous is None else str(previous),
        }
        events = publisher.events[before:]
        assert len(events) == (34 if options.get("partial") else 35)
        # Expected labels are explicit test inputs, not generated by the oracle.
        assert all(e["attributes"]["sim_time_quality"] == expected for e in events)
        assert all(e["local_id"] != cfg.clocks[0].local_id and "point_id" not in e for e in events)
        cases.append({"name": name, "expectedQuality": expected,
                      "events": events, "acquisition": acquisition})

    with patch("bacnet_connector.connector.datetime", AcquisitionTime):
        for name, samples, expected, options in [
            ("stable fractional", [120.5] * 3, "stable_unfenced", {}),
            ("advance at first chunk boundary", [120.5, 180, 180], "changed", {}),
            ("advance at final boundary", [120.5, 120.5, 180], "changed", {}),
            ("missing", [120.5, None, 120.5], "missing", {}),
            ("missing outranks changed", [120.5, 180, None], "missing", {}),
            ("reset outranks missing", [120.5, 0, None], "reset", {}),
            ("unit mismatch", [120.5] * 3, "missing", {"unit": "minutes"}),
            ("partial equipment failure", [120.5] * 3, "stable_unfenced", {"partial": True}),
        ]:
            await poll(connector(), name, samples, expected, **options)
        conn = connector()
        for name, samples, expected in [
            ("first poll", [120.5] * 3, "stable_unfenced"),
            ("same step repeated", [120.5] * 3, "stable_unfenced"),
            ("temporary loss", [None] * 3, "missing"),
            ("same value restored", [120.5] * 3, "stable_unfenced"),
            ("backward across polls", [0] * 3, "reset"),
            ("reset stays latched", [120.5] * 3, "reset"),
        ]:
            await poll(conn, name, samples, expected)
        await poll(connector(), "restart has no run claims", [0] * 3, "stable_unfenced")
    return cases


def verify(payload):
    contract = oracle()
    snapshot = payload["snapshot"]
    original = copy.deepcopy(payload)
    count = 0
    for case in payload["polls"]:
        for frame in case["frames"]:
            result = contract.validate_observation(snapshot, frame, case["acquisition"])
            assert result["quality"] == case["expectedQuality"], case["name"]
            assert result["runStatus"] == "absent" and result["runReason"] is None
            assert not result["runTimeEligible"] and not result["strictStepEligible"]
            attrs = frame["attributes"]
            assert "sim_clock_revision" not in attrs
            assert not {"sim_run_id", "sim_context_generation", "sim_step_id"}.intersection(attrs)
            assert not {"clockSamples", "authenticated", "startedAt", "resetLatched"}.intersection(attrs)
            count += 1
    assert payload == original, "oracle must not rewrite recorded frames/evidence"
    return {"verifiedFrames": count, "verifiedPolls": len(payload["polls"])}


if __name__ == "__main__":
    payload = json.load(sys.stdin)
    result = asyncio.run(generate(payload)) if sys.argv[1] == "generate" else verify(payload)
    json.dump(result, sys.stdout)
