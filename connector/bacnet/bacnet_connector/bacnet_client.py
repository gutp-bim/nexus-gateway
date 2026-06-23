# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""bacpypes3-backed BACnetClient implementation."""
from __future__ import annotations

import asyncio
import logging
from typing import Callable, Awaitable

from bacpypes3.app import Application, DeviceObject, NetworkPortObject
from bacpypes3.apdu import ErrorRejectAbortNack
from bacpypes3.basetypes import ErrorType
from bacpypes3.pdu import Address
from bacpypes3.primitivedata import ObjectIdentifier, Null, Real

from bacnet_connector.connector import BACnetClient

logger = logging.getLogger(__name__)

_COV_BACKOFF_INITIAL = 5.0   # seconds before first COV retry
_COV_BACKOFF_CAP = 300.0     # maximum backoff (5 minutes)
_COV_GIVE_UP_AFTER = 5       # consecutive failures before abandoning COV for this point


def _to_float(raw: object) -> float | None:
    """Coerce a bacpypes3 primitive value to float, or return None."""
    if raw is None or isinstance(raw, (Null, ErrorType)):
        return None
    try:
        return float(raw)  # type: ignore[arg-type]
    except (TypeError, ValueError):
        return None


class Bacpypes3Client(BACnetClient):
    """BACnetClient backed by a bacpypes3 Application."""

    def __init__(self, app: Application, read_timeout: float = 5.0):
        self._app = app
        self._read_timeout = read_timeout
        # Map of (address, obj_id) -> (asyncio.Task, cancel_event) for active COV subs.
        self._cov_tasks: dict[tuple[str, str], asyncio.Task] = {}

    @classmethod
    async def create(cls, local_address: str, read_timeout: float = 5.0) -> "Bacpypes3Client":
        dev = DeviceObject(
            objectIdentifier=("device", 599),
            objectName="nexus-bacnet-client",
            vendorIdentifier=999,
            maxApduLengthAccepted=1476,
            segmentationSupported="noSegmentation",
        )
        net = NetworkPortObject(
            local_address,
            objectIdentifier=("network-port", 1),
            objectName="NP-1",
        )
        app = Application.from_object_list([dev, net])
        return cls(app, read_timeout=read_timeout)

    async def read_property_multiple(
        self,
        address: str,
        device_id: int,
        requests: list[tuple[str, str]],
    ) -> list[tuple[str, float | None, str | None]]:
        # bacpypes3 API: flat alternating list [obj_id, [props], obj_id, [props], ...]
        parameter_list = []
        for obj_id, prop_id in requests:
            parameter_list.append(obj_id)
            parameter_list.append([prop_id])
        # ErrorRejectAbortNack is a BaseException (not Exception) so catch it explicitly.
        try:
            raw = await asyncio.wait_for(
                self._app.read_property_multiple(Address(address), parameter_list),
                timeout=self._read_timeout,
            )
        except asyncio.TimeoutError:
            logger.warning(
                "bacnet: read from %s timed out after %.0fs (%d points)",
                address, self._read_timeout, len(requests),
            )
            return [(obj_id, None, None) for obj_id, _ in requests]
        except ErrorRejectAbortNack as exc:
            logger.warning("bacnet: device error from %s: %s", address, exc)
            return [(obj_id, None, None) for obj_id, _ in requests]

        # Index results by normalized obj_id for lookup
        results_by_obj: dict[str, float | None] = {}
        if raw and not isinstance(raw, ErrorRejectAbortNack):
            for obj_id_result, _prop_id, _index, value in raw:
                key = str(obj_id_result)
                results_by_obj[key] = _to_float(value)

        out = []
        for obj_id, _prop_id in requests:
            normalized = str(ObjectIdentifier(obj_id))
            val = results_by_obj.get(normalized)
            out.append((obj_id, val, None))
        return out

    async def subscribe_cov(
        self,
        address: str,
        device_id: int,
        obj_id: str,
        callback: Callable[[str, float, str], Awaitable[None]],
        lifetime: int = 300,
    ) -> None:
        key = (address, obj_id)
        if key in self._cov_tasks and not self._cov_tasks[key].done():
            return  # already subscribed and still running

        task = asyncio.create_task(
            self._cov_loop(address, obj_id, callback, lifetime),
            name=f"bacpypes3-cov-{obj_id}",
        )
        self._cov_tasks[key] = task

    async def _cov_loop(
        self,
        address: str,
        obj_id: str,
        callback: Callable[[str, float, str], Awaitable[None]],
        lifetime: int,
    ) -> None:
        """Maintain a COV subscription, renewing before it expires.

        Uses exponential backoff on failure.  After _COV_GIVE_UP_AFTER consecutive
        failures the loop exits so the connector relies entirely on polling — this
        is the correct behaviour when a device does not support COV for an object type.
        """
        addr = Address(address)
        oid = ObjectIdentifier(obj_id)
        backoff = _COV_BACKOFF_INITIAL
        consecutive_failures = 0

        while True:
            try:
                async with self._app.change_of_value(addr, oid, lifetime=lifetime) as cov:
                    consecutive_failures = 0
                    backoff = _COV_BACKOFF_INITIAL
                    while True:
                        # get_value() decodes the next PropertyValue from the queue.
                        prop_id, prop_value = await cov.get_value()
                        if str(prop_id) == "present-value":
                            v = _to_float(prop_value)
                            if v is not None:
                                await callback(obj_id, v, "")
            except asyncio.CancelledError:
                return
            except (Exception, ErrorRejectAbortNack) as exc:
                consecutive_failures += 1
                if consecutive_failures >= _COV_GIVE_UP_AFTER:
                    logger.info(
                        "bacnet: COV not supported for %s (%s) — relying on polling only",
                        obj_id, exc,
                    )
                    return
                logger.warning(
                    "bacnet: COV loop error for %s: %s — retry in %.0fs",
                    obj_id, exc, backoff,
                )
                await asyncio.sleep(backoff)
                backoff = min(backoff * 2, _COV_BACKOFF_CAP)

    async def write_property(
        self,
        address: str,
        device_id: int,
        obj_id: str,
        prop_id: str,
        value: float,
        priority: int,
    ) -> None:
        try:
            await self._app.write_property(
                address,
                obj_id,
                prop_id,
                Real(value),
                priority=priority,
            )
        except ErrorRejectAbortNack as exc:
            raise RuntimeError(f"BACnet write rejected: {exc}") from exc

    async def close(self) -> None:
        for task in self._cov_tasks.values():
            task.cancel()
        if self._cov_tasks:
            await asyncio.gather(*self._cov_tasks.values(), return_exceptions=True)
        self._cov_tasks.clear()
        self._app.close()
