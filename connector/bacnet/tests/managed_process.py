# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Isolated subprocess harness: real NATS/managed connector, injected BACnet only.

No Bacpypes3 application is imported/created and no equipment socket is opened.
Used by fake-Engine Gateway tests and the documented isolated verification flow.
"""
import asyncio
import sys
from types import ModuleType, SimpleNamespace
from unittest.mock import AsyncMock, patch

from bacnet_connector.config import Config


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
    # Exercise the production entrypoint's auth, stream readiness, subscriptions
    # and shutdown. Replace only equipment construction and the health socket.
    module = ModuleType("bacnet_connector.bacnet_client")
    module.Bacpypes3Client = SimpleNamespace(create=AsyncMock(return_value=InjectedBACnet(cfg)))
    with patch.dict(sys.modules, {"bacnet_connector.bacnet_client": module}):
        from bacnet_connector.main import _run
        with patch("bacnet_connector.main.start_health_server", return_value=None):
            await _run(cfg)


if __name__ == "__main__":
    asyncio.run(main())
