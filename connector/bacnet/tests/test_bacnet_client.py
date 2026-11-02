# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Tests for Bacpypes3Client behaviour that does not require a real device."""
from __future__ import annotations

import asyncio

import pytest

from bacnet_connector.bacnet_client import Bacpypes3Client, _to_scalar


def test_to_scalar_preserves_telemetry_types():
    assert _to_scalar(12.5) == 12.5
    assert _to_scalar(True) is True
    assert _to_scalar(False) is False
    assert _to_scalar("running") == "running"


class HangingApp:
    """bacpypes3 Application stub whose read never returns — simulates a dead device."""

    async def read_property_multiple(self, address, parameter_list):
        await asyncio.sleep(60)  # longer than any test timeout


@pytest.mark.asyncio
async def test_read_property_multiple_times_out():
    """A read that never returns must time out and yield all-None, not hang."""
    client = Bacpypes3Client(HangingApp(), read_timeout=0.05)
    requests = [("analogInput,0", "presentValue"), ("analogInput,1", "presentValue")]

    result = await asyncio.wait_for(
        client.read_property_multiple("192.168.1.100", 42, requests),
        timeout=2.0,  # the call itself must return well before this
    )

    assert result == [("analogInput,0", None, None), ("analogInput,1", None, None)]

@pytest.mark.asyncio
@pytest.mark.parametrize("value", [None, 21.5])
async def test_write_property_encodes_null_or_real(value):
    from unittest.mock import AsyncMock
    from bacpypes3.primitivedata import Null, Real
    app = AsyncMock()
    await Bacpypes3Client(app).write_property("127.0.0.1", 42, "analogOutput,37", "presentValue", value, 8)
    args = app.write_property.call_args
    assert isinstance(args.args[3], Null if value is None else Real)
    assert args.kwargs["priority"] == 8

@pytest.mark.asyncio
async def test_release_changes_real_bacpypes_priority_array():
    """Use BACpypes' commandable object, including arbitration and relinquishDefault."""
    from bacpypes3.local.analog import AnalogOutputObject
    obj = AnalogOutputObject(objectIdentifier=("analogOutput", 37), objectName="release-test",
                             presentValue=18.0, relinquishDefault=18.0)
    class LocalApp:
        async def write_property(self, address, obj_id, prop_id, value, priority):
            await obj.write_property(prop_id, value, priority=priority)
    client = Bacpypes3Client(LocalApp())
    async def write(value, priority):
        await client.write_property("127.0.0.1", 42, "analogOutput,37", "presentValue", value, priority)
    await write(22.0, 8)
    assert obj.presentValue == 22.0
    await write(None, 8)
    assert obj.priorityArray[7].null is not None
    assert obj.presentValue == obj.relinquishDefault == 18.0
    await write(19.0, 10)
    await write(22.0, 8)
    await write(None, 8)
    assert obj.presentValue == 19.0
    assert obj.priorityArray[9].real == 19.0
