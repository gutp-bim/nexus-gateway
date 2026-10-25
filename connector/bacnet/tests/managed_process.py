# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Isolated subprocess harness: real NATS/managed connector, injected BACnet only.

No Bacpypes3 application is imported/created and no equipment socket is opened.
Used by fake-Engine Gateway tests and the documented isolated verification flow.
"""
import asyncio
import signal

import nats

from bacnet_connector.config import Config
from bacnet_connector.connector import Connector
from bacnet_connector.managed import ManagedRuntime
from bacnet_connector.write_handler import WriteHandler


class InjectedBACnet:
    def __init__(self, cfg):
        self.cfg = cfg
        self.clocks = {c.local_id for c in cfg.clocks}
        self.values = {p.local_id: i + 0.5 for i, p in enumerate(cfg.points)}

    async def read_property_multiple(self, address, device_id, requests):
        assert address == "192.0.2.1" and device_id == self.cfg.bacnet_device_id
        return [(obj, ("seconds" if prop == "units" else 120.5) if obj in self.clocks else self.values[obj], None)
                for obj, prop in requests]

    async def write_property(self, *args):
        pass  # Injected success only; never any network/device operation.

    async def close(self):
        pass


async def main():
    cfg = Config.from_env()
    assert cfg.managed_session, "managed test harness requires explicit managed configuration"
    nc = await nats.connect(cfg.nats_url)
    bacnet = InjectedBACnet(cfg)
    connector = Connector(cfg, bacnet, nc.jetstream())
    runtime = ManagedRuntime(cfg, connector, nc, WriteHandler(cfg, bacnet))
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stop.set)
    await nc.subscribe(f"cmd.bacnet.{cfg.connector_id}", cb=runtime.command)
    await nc.subscribe(f"cfg.bacnet.{cfg.connector_id}.runtime", cb=runtime.control)
    await nc.flush()
    try:
        await runtime.run(stop)
    finally:
        await nc.drain()


if __name__ == "__main__":
    asyncio.run(main())
