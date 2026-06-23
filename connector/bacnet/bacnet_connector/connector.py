# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""BACnet connector: polls / subscribes to BACnet devices and publishes Common Events."""
from __future__ import annotations

import asyncio
import contextlib
import logging
from typing import Any, Callable, Awaitable

import nats
from nats.js import JetStreamContext

from bacnet_connector.config import Config, PointConfig
from bacnet_connector.event import make_event, bacnet_quality

logger = logging.getLogger(__name__)


# BACnetClient is an interface (structural typing) so tests can inject a mock.
class BACnetClient:
    """Minimal interface for BACnet operations needed by the Connector."""

    async def read_property_multiple(
        self,
        address: str,
        device_id: int,
        requests: list[tuple[str, str]],  # [(objid, prop_id), ...]
    ) -> list[tuple[str, float | None, str | None]]:
        """Returns [(objid, value, status), ...]. Raises on transport error."""
        raise NotImplementedError

    async def subscribe_cov(
        self,
        address: str,
        device_id: int,
        obj_id: str,
        callback: Callable[[str, float, str], Awaitable[None]],
        lifetime: int = 300,
    ) -> None:
        """Subscribe to COV notifications for obj_id. Calls callback on each change."""
        raise NotImplementedError

    async def write_property(
        self,
        address: str,
        device_id: int,
        obj_id: str,
        prop_id: str,
        value: float,
        priority: int,
    ) -> None:
        """Write a single property. Raises on device error or transport failure."""
        raise NotImplementedError

    async def close(self) -> None:
        pass


class Connector:
    """Connects to a BACnet device, polls points, and publishes Common Events.

    Dependencies are injected so tests can stub the BACnet and NATS layers.
    """

    def __init__(self, cfg: Config, bacnet: BACnetClient, js: JetStreamContext):
        self._cfg = cfg
        self._bacnet = bacnet
        self._js = js
        self._subject = f"evt.bacnet.{cfg.connector_id}"
        self._point_map = {pt.local_id: pt for pt in cfg.points}
        self._cov_tasks: list[asyncio.Task] = []

    async def run(self, *, stop_event: asyncio.Event | None = None) -> None:
        """Poll all points once, then set up COV subscriptions and keep polling.

        Runs until stop_event is set (or forever if stop_event is None).
        """
        logger.info("bacnet: connector %s starting", self._cfg.connector_id)

        # Initial full poll so we have values immediately on startup.
        await self._poll_all()

        # Subscribe to COV for every point — tasks are cancelled on shutdown.
        # Skipped in poll-only mode: thousands of long-lived COV sessions can
        # overwhelm a device, so large deployments rely on periodic polling alone.
        if self._cfg.cov_enabled:
            for pt in self._cfg.points:
                task = asyncio.create_task(
                    self._subscribe_cov(pt),
                    name=f"cov-{pt.local_id}",
                )
                self._cov_tasks.append(task)

        # Periodic poll loop — keeps values fresh even when COV subscriptions lapse.
        # Uses stop_event.wait() with a timeout so shutdown is immediate.
        try:
            while True:
                if stop_event is not None:
                    try:
                        await asyncio.wait_for(
                            stop_event.wait(),
                            timeout=self._cfg.poll_interval,
                        )
                        break  # stop_event fired
                    except asyncio.TimeoutError:
                        pass  # poll interval elapsed, continue
                else:
                    await asyncio.sleep(self._cfg.poll_interval)
                await self._poll_all()
        finally:
            for t in self._cov_tasks:
                t.cancel()
            await asyncio.gather(*self._cov_tasks, return_exceptions=True)
            self._cov_tasks.clear()
            await self._bacnet.close()
            logger.info("bacnet: connector %s stopped", self._cfg.connector_id)

    async def _poll_all(self) -> None:
        """Poll all points and publish events.

        Points are read in chunks of ``rpm_chunk_size`` rather than a single
        ReadPropertyMultiple: a response for many points can overflow the device's
        APDU, and devices without segmentation reject the whole request with
        "segmentation-not-supported". A failed chunk is logged and skipped so the
        remaining chunks still publish.
        """
        if not self._cfg.points:
            return

        requests = [(pt.local_id, "presentValue") for pt in self._cfg.points]
        chunk_size = self._cfg.rpm_chunk_size
        for start in range(0, len(requests), chunk_size):
            chunk = requests[start:start + chunk_size]
            try:
                results = await self._bacnet.read_property_multiple(
                    self._cfg.bacnet_address,
                    self._cfg.bacnet_device_id,
                    chunk,
                )
            except Exception as exc:
                logger.warning(
                    "bacnet: poll chunk [%d:%d] failed: %s",
                    start, start + len(chunk), exc,
                )
                continue

            for obj_id, value, status in results:
                pt = self._point_map.get(obj_id)
                if pt is None or value is None:
                    continue
                await self._publish(pt, value, bacnet_quality(status))

    async def _subscribe_cov(self, pt: PointConfig) -> None:
        """Subscribe to COV for a single point and publish events on each change."""
        async def on_cov(obj_id: str, value: float, status: str) -> None:
            await self._publish(pt, value, bacnet_quality(status))

        while True:
            try:
                await self._bacnet.subscribe_cov(
                    self._cfg.bacnet_address,
                    self._cfg.bacnet_device_id,
                    pt.local_id,
                    callback=on_cov,
                )
                return  # subscription active — callbacks will fire
            except asyncio.CancelledError:
                return
            except Exception as exc:
                logger.warning(
                    "bacnet: COV subscribe failed for %s, retrying in 30s: %s",
                    pt.local_id, exc,
                )
                await asyncio.sleep(30)

    async def _publish(self, pt: PointConfig, value: float, quality: str) -> None:
        data = make_event(
            connector_id=self._cfg.connector_id,
            local_id=pt.local_id,
            device_ref=pt.device_ref,
            value=value,
            unit=pt.unit,
            quality=quality,
        )
        try:
            await self._js.publish(self._subject, data)
            logger.debug("bacnet: published %s=%s quality=%s", pt.local_id, value, quality)
        except Exception as exc:
            logger.warning("bacnet: NATS publish failed — withholding for %s: %s", pt.local_id, exc)
