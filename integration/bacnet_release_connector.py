# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0
"""Test-only NATS responder using the production handler/client and BACpypes arbitration.

The Application I/O boundary is local; this does not simulate BACnet UDP transport.
"""
import asyncio
import json
import sys

import nats
from bacpypes3.local.analog import AnalogOutputObject
from bacnet_connector.bacnet_client import Bacpypes3Client
from bacnet_connector.config import Config, PointConfig
from bacnet_connector.write_handler import WriteHandler


async def main():
    obj = AnalogOutputObject(objectIdentifier=("analogOutput", 37), objectName="release-test",
                             presentValue=18.0, relinquishDefault=18.0)
    writes = 0
    class LocalApp:
        async def write_property(self, address, obj_id, prop_id, value, priority):
            nonlocal writes
            assert obj_id == "analogOutput,37"
            await obj.write_property(prop_id, value, priority=priority)
            writes += 1
    nc = await nats.connect(sys.argv[1])
    cfg = Config(connector_id="bacnet-release", nats_url=sys.argv[1],
                 bacnet_address="127.0.0.1", bacnet_device_id=42,
                 points=[PointConfig("analogOutput,37", "device-test", writable=True)])
    handler = WriteHandler(cfg, Bacpypes3Client(LocalApp()))
    await nc.subscribe("cmd.bacnet.bacnet-release", cb=handler.handle)
    async def state(msg):
        slots = [None if pv.null is not None else float(pv.real) for pv in obj.priorityArray]
        await msg.respond(json.dumps(dict(value=float(obj.presentValue), slots=slots, writes=writes)).encode())
    await nc.subscribe("test.bacnet.state", cb=state)
    await nc.flush()
    print("ready", flush=True)
    await asyncio.Event().wait()


asyncio.run(main())
