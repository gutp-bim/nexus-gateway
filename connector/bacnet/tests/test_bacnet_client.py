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
