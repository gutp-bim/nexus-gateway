# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Static clock prototype: injected BACnet responses only, no sockets or REST."""
import copy
import json
from datetime import datetime, timezone
from unittest.mock import AsyncMock

import pytest

from bacnet_connector.config import Config, ConfigError
from bacnet_connector.connector import Connector


def bundle():
    return {
        "revision": '"sha256:' + "a" * 64 + '"',
        "connector_id": "bacnet-test", "device_id": 4321,
        "points": [{"local_id": f"analogInput,{i}", "device_ref": "4321", "unit": "Cel", "writable": False} for i in (17, 29)],
        "clocks": [{"clock_id": "urn:clock:a", "local_id": "analogValue,87", "property": "presentValue", "point_local_ids": ["analogInput,17", "analogInput,29"]}],
    }


def config(data=None):
    cfg = Config("bacnet-test", "nats://unused", "192.0.2.1", 4321, cov_enabled=False, rpm_chunk_size=1)
    cfg.apply_clock_bundle(data or bundle())
    return cfg


class Responses:
    def __init__(self, clocks, *, unit="seconds", fail_point=None):
        self.clocks = {key: iter(values) for key, values in clocks.items()}
        self.unit = unit
        self.requests = []
        self.fail_point = fail_point

    async def read_property_multiple(self, address, device_id, requests):
        self.requests.append(requests)
        obj, prop = requests[0]
        if obj in self.clocks:
            value = self.unit if prop == "units" else next(self.clocks[obj])
            if isinstance(value, Exception):
                raise value
            return [(obj, value, None)]
        if obj == self.fail_point:
            raise TimeoutError("injected point timeout")
        return [(obj, 21.5, None)]


async def poll(values, **kwargs):
    client = Responses({"analogValue,87": values}, **kwargs)
    js = AsyncMock()
    conn = Connector(config(), client, js)
    await conn._poll_all()
    return conn, [json.loads(call.args[1]) for call in js.publish.call_args_list], client


@pytest.mark.asyncio
async def test_stable_clock_is_metadata_only_and_preserves_native_identity():
    conn, events, client = await poll([120, 120, 120])
    assert len(events) == 2
    assert conn.healthy()
    for e in events:
        assert "point_id" not in e and "analogValue,87" != e["local_id"]
        assert e["timestamp"].endswith("Z") and e["quality"] == "Good"
        assert e["attributes"] == {"sim_clock_id": "urn:clock:a", "sim_config_revision": bundle()["revision"], "sim_time_s": "120.0", "sim_time_quality": "stable_unfenced", "sim_run_status": "absent"}
        assert "sim_run_id" not in e["attributes"] and "sim_step_id" not in e["attributes"]
    assert client.requests[0] == [("analogValue,87", "presentValue")]


@pytest.mark.asyncio
async def test_timestamp_is_captured_at_equipment_read_before_delayed_publication(monkeypatch):
    class AcquisitionTime:
        @staticmethod
        def now(tz):
            return datetime(2026, 1, 2, 3, 4, 5, 500, tzinfo=timezone.utc)

    monkeypatch.setattr("bacnet_connector.connector.datetime", AcquisitionTime)
    _, events, _ = await poll([120] * 3)
    assert all(e["timestamp"] == "2026-01-02T03:04:05.000500Z" for e in events)


@pytest.mark.asyncio
@pytest.mark.parametrize("values,quality", [
    ([120, 180, 180], "changed"),  # step at first chunk boundary invalidates entire poll
    ([120, 120, 180], "changed"),
    ([120, None, 120], "missing"),
    ([None, 120, 120], "missing"),
    ([120, 120, None], "missing"),
    ([120, TimeoutError("injected"), 120], "missing"),
    ([120, 0, 0], "reset"),
    ([True, 120, 120], "missing"),
    ([float("nan"), 120, 120], "missing"),
    ([float("inf"), 120, 120], "missing"),
    ([-1, 120, 120], "missing"),
    (["120", 120, 120], "missing"),
])
async def test_uncertain_clock_never_reuses_time(values, quality, caplog):
    _, events, _ = await poll(values)
    assert len(events) == 2
    assert all(e["attributes"]["sim_time_quality"] == quality for e in events)
    assert all("sim_time_s" not in e["attributes"] for e in events)
    assert f"sim_time_quality={quality}" in caplog.text


@pytest.mark.asyncio
@pytest.mark.parametrize("unit", ["minutes", None, True, 72])
async def test_unit_probe_failure(unit):
    _, events, _ = await poll([120] * 3, unit=unit)
    assert all(e["attributes"]["sim_time_quality"] == "missing" for e in events)


@pytest.mark.asyncio
async def test_partial_equipment_failure_does_not_create_a_value():
    _, events, _ = await poll([120] * 3, fail_point="analogInput,17", unit=73)
    assert len(events) == 1 and events[0]["local_id"] == "analogInput,29"
    assert events[0]["attributes"]["sim_time_s"] == "120.0"


@pytest.mark.asyncio
async def test_repeated_poll_missing_recovery_and_reset_latch():
    values = [120] * 6 + [None] * 3 + [120] * 3 + [0] * 3 + [120] * 3
    client = Responses({"analogValue,87": values})
    js = AsyncMock()
    conn = Connector(config(), client, js)
    for _ in range(6):
        await conn._poll_all()
    attrs = [json.loads(c.args[1])["attributes"] for c in js.publish.call_args_list][::2]
    assert [a["sim_time_quality"] for a in attrs] == ["stable_unfenced", "stable_unfenced", "missing", "stable_unfenced", "reset", "reset"]
    assert "sim_time_s" not in attrs[2] and "sim_time_s" not in attrs[-1]
    # New connector has no authenticated run history and must still say unfenced.
    fresh = Connector(config(), Responses({"analogValue,87": [0] * 3}), js)
    await fresh._poll_all()
    assert json.loads(js.publish.call_args.args[1])["attributes"]["sim_time_quality"] == "stable_unfenced"


@pytest.mark.asyncio
async def test_independent_clocks_and_unbound_point():
    data = bundle()
    data["clocks"][0]["point_local_ids"] = ["analogInput,17"]
    data["clocks"].append({"clock_id": "urn:clock:b", "local_id": "analogInput,99", "property": "presentValue", "point_local_ids": ["analogInput,29"]})
    data["points"].append({"local_id": "analogInput,30", "device_ref": "4321", "unit": "Cel", "writable": False})
    js = AsyncMock()
    await Connector(config(data), Responses({"analogValue,87": [10] * 4, "analogInput,99": [20, 20, 30, 30]}), js)._poll_all()
    events = [json.loads(c.args[1]) for c in js.publish.call_args_list]
    assert events[0]["attributes"]["sim_time_s"] == "10.0"
    assert events[1]["attributes"]["sim_time_quality"] == "changed"
    assert "attributes" not in events[2]


@pytest.mark.parametrize("change", [
    lambda b: b.update(revision="other"),
    lambda b: b.update(connector_id="other"),
    lambda b: b.update(device_id=1),
    lambda b: b["clocks"][0].update(point_local_ids=["unknown"]),
    lambda b: b["clocks"][0].update(local_id="analogInput,17"),
    lambda b: b["clocks"].append(copy.deepcopy(b["clocks"][0])),
    lambda b: b["clocks"][0].update(point_id="forbidden"),
    lambda b: b["clocks"][0].update(property="objectName"),
])
def test_invalid_bundle_keeps_previous_config(change):
    cfg = config()
    original = (cfg.points, cfg.clocks, cfg.clock_revision)
    bad = bundle()
    change(bad)
    with pytest.raises(ConfigError):
        cfg.apply_clock_bundle(bad)
    assert original == (cfg.points, cfg.clocks, cfg.clock_revision)


def test_clock_bundle_rejects_cov():
    cfg = Config("bacnet-test", "unused", "192.0.2.1", 4321)
    with pytest.raises(ConfigError, match="COV"):
        cfg.apply_clock_bundle(bundle())


def test_env_native_bundle_and_separate_points_are_exclusive(tmp_path, monkeypatch):
    path = tmp_path / "native.json"
    path.write_text(json.dumps(bundle()))
    for key, value in {"CONNECTOR_ID": "bacnet-test", "BACNET_ADDRESS": "192.0.2.1", "BACNET_DEVICE_ID": "4321", "BACNET_COV_ENABLED": "false", "BACNET_CLOCK_CONFIG_FILE": str(path)}.items():
        monkeypatch.setenv(key, value)
    monkeypatch.delenv("BACNET_POINTS", raising=False)
    assert len(Config.from_env().clocks) == 1
    monkeypatch.setenv("BACNET_POINTS", "[]")
    with pytest.raises(ConfigError, match="cannot be combined"):
        Config.from_env()
