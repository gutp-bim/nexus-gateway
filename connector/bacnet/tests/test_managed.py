# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

import asyncio
import hashlib
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from bacnet_connector.config import Config, ConfigError
from bacnet_connector.managed import ManagedRuntime
from .test_clock import bundle


class Message:
    def __init__(self, body):
        self.data = json.dumps(body).encode()
        self.respond = AsyncMock()


def runtime():
    cfg = SimpleNamespace(managed_session="a" * 32, clock_revision='"sha256:example"',
                          native_hash="b" * 64, poll_interval=0.01)
    connector = SimpleNamespace(_poll_all=AsyncMock(), _bacnet=SimpleNamespace(close=AsyncMock()))
    return ManagedRuntime(cfg, connector, SimpleNamespace(flush=AsyncMock()), SimpleNamespace(handle=AsyncMock()))


async def control(r, action, **patch):
    msg = Message(dict(session=r.cfg.managed_session, action=action,
                       revision=r.cfg.clock_revision, native_sha256=r.cfg.native_hash, **patch))
    await r.control(msg)
    return json.loads(msg.respond.call_args.args[0])


@pytest.mark.asyncio
async def test_managed_starts_paused_and_reports_loaded_identity():
    r = runtime()
    stop = asyncio.Event()
    polled = asyncio.Event()
    r.connector._poll_all.side_effect = lambda: polled.set()
    task = asyncio.create_task(r.run(stop))
    await asyncio.sleep(0)
    assert await control(r, "status") == dict(capability="clock-runtime/1", session=r.cfg.managed_session,
                                             revision=r.cfg.clock_revision, native_sha256=r.cfg.native_hash, paused=True)
    msg = Message({})
    await r.command(msg)
    assert json.loads(msg.respond.call_args.args[0])["response"] == "configuration_paused"
    r.handler.handle.assert_not_called()
    r.connector._poll_all.assert_not_called()
    assert (await control(r, "resume"))["paused"] is False
    await asyncio.wait_for(polled.wait(), timeout=1)
    assert r.connector._poll_all.await_count >= 1
    stop.set()
    await task
    r.connector._bacnet.close.assert_awaited_once()


@pytest.mark.asyncio
async def test_quiesce_waits_for_inflight_write_and_flushes():
    r = runtime()
    await control(r, "resume")
    entered, release = asyncio.Event(), asyncio.Event()

    async def write(msg):
        entered.set()
        await release.wait()

    r.handler.handle.side_effect = write
    command = asyncio.create_task(r.command(Message({})))
    await entered.wait()
    quiesce = asyncio.create_task(control(r, "quiesce"))
    await asyncio.sleep(0)
    assert not quiesce.done() and not r.active
    release.set()
    await command
    assert (await quiesce)["paused"] is True
    r.nc.flush.assert_awaited_once()
    await r.command(Message({}))
    assert r.handler.handle.await_count == 1


@pytest.mark.asyncio
async def test_bad_session_or_config_never_resumes():
    r = runtime()
    msg = Message(dict(action="resume", session="wrong", revision=r.cfg.clock_revision, native_sha256=r.cfg.native_hash))
    await r.control(msg)
    assert "error" in json.loads(msg.respond.call_args.args[0]) and not r.active
    msg = Message(dict(action="resume", session=r.cfg.managed_session, revision="wrong", native_sha256=r.cfg.native_hash))
    await r.control(msg)
    assert "error" in json.loads(msg.respond.call_args.args[0]) and not r.active


def test_receipt_hash_comes_from_actual_native_file(tmp_path, monkeypatch):
    raw = json.dumps(bundle(), indent=2).encode()
    path = tmp_path / "native.json"
    path.write_bytes(raw)
    for key, value in {"CONNECTOR_ID": "bacnet-test", "BACNET_ADDRESS": "192.0.2.1", "BACNET_DEVICE_ID": "4321", "BACNET_COV_ENABLED": "false", "BACNET_CLOCK_CONFIG_FILE": str(path), "BACNET_MANAGED_SESSION": "a" * 32}.items():
        monkeypatch.setenv(key, value)
    monkeypatch.delenv("BACNET_POINTS", raising=False)
    auth = tmp_path / "producer-auth.json"
    auth.write_text('{"user":"producer","password":"synthetic"}')
    monkeypatch.setenv("NATS_URL", "nats://broker:4222")
    monkeypatch.setenv("NATS_AUTH_FILE", str(auth))
    cfg = Config.from_env()
    assert cfg.native_hash == hashlib.sha256(raw).hexdigest()
    assert cfg.managed_session == "a" * 32
    monkeypatch.setenv("BACNET_MANAGED_SESSION", "not-a-session")
    with pytest.raises(ConfigError):
        Config.from_env()


def test_managed_nats_authentication_is_file_only_and_fails_closed(tmp_path):
    from .test_clock import config
    cfg = config()
    cfg.managed_session = "a" * 32
    cfg.nats_url = "nats://broker:4222"
    auth = tmp_path / "producer-auth.json"
    cfg.nats_auth_file = str(auth)
    for body in [None, '{"user":"producer"}', '{"user":"producer","password":""}',
                 '{"user":"producer","password":"synthetic-secret-202",bad}',
                 '{"user":"producer","password":"synthetic-secret-202","extra":true}']:
        if body is not None:
            auth.write_text(body)
        with pytest.raises(ConfigError) as err:
            cfg.nats_auth_options()
        assert "synthetic-secret-202" not in str(err.value)
    auth.write_text('{"user":"producer","password":"synthetic-secret-202"}')
    options = cfg.nats_auth_options()
    assert options["user"] == "producer" and options["password"] == "synthetic-secret-202"
    assert options["inbox_prefix"] == b"_INBOX.bacnet-test"
    cfg.nats_url = "nats://core:synthetic-secret-202@broker:4222"
    with pytest.raises(ConfigError) as err:
        cfg.nats_auth_options()
    assert "synthetic-secret-202" not in str(err.value)


@pytest.mark.asyncio
async def test_managed_event_headers_bind_even_clockless_observations():
    from .test_clock import config, Responses
    from bacnet_connector.connector import Connector

    cfg = config()
    cfg.managed_session = "a" * 32
    js = AsyncMock()
    await Connector(cfg, Responses({"analogValue,87": [120] * 3}), js)._poll_all()
    for call in js.publish.call_args_list:
        assert call.kwargs["headers"]["Nexus-Session"] == cfg.managed_session
        assert call.kwargs["headers"]["Nexus-Config-Revision"] == cfg.clock_revision
        assert call.kwargs["headers"]["Nexus-Poll-Started-At"] <= call.kwargs["headers"]["Nexus-Poll-Ended-At"]
        assert json.loads(call.args[1])["attributes"]["sim_run_status"] == "absent"
    cfg.clocks = []
    js.reset_mock()
    # No-clock events keep their old body shape, while transport binding remains.
    bacnet = SimpleNamespace(read_property_multiple=AsyncMock(return_value=[("analogInput,17", 0.5, None)]))
    await Connector(cfg, bacnet, js)._poll_all()
    assert "attributes" not in json.loads(js.publish.call_args.args[1])
    assert js.publish.call_args.kwargs["headers"]["Nexus-Session"] == cfg.managed_session
