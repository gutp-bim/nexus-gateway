# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""BACnet connector: polls / subscribes to BACnet devices and publishes Common Events."""
from __future__ import annotations

import asyncio
import contextlib
import logging
import math
import time
from datetime import datetime, timezone
from typing import Any, Callable, Awaitable

import nats
from nats.js import JetStreamContext

from bacnet_connector.config import Config, PointConfig, ClockConfig
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
    ) -> list[tuple[str, float | str | bool | None, str | None]]:
        """Returns [(objid, value, status), ...]. Raises on transport error."""
        raise NotImplementedError

    async def subscribe_cov(
        self,
        address: str,
        device_id: int,
        obj_id: str,
        callback: Callable[[str, float | str | bool, str], Awaitable[None]],
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

    # Minimum seconds between repeated non-numeric-value WARNINGs for the same
    # point, so a persistently-bad point does not flood the logs. Overridable per
    # instance (e.g. in tests) via ``self._bad_value_warn_window``.
    BAD_VALUE_WARN_WINDOW_SEC: float = 300.0

    def __init__(self, cfg: Config, bacnet: BACnetClient, js: JetStreamContext):
        self._cfg = cfg
        self._bacnet = bacnet
        self._js = js
        self._subject = f"evt.bacnet.{cfg.connector_id}"
        self._point_map = {pt.local_id: pt for pt in cfg.points}
        self._cov_tasks: list[asyncio.Task] = []
        # Device-reachability proxy for health: True once the most recent poll
        # cycle produced at least one successful read (or there are no points).
        self._healthy = False
        # Rate-limit state for the non-numeric-value WARNING: local_id → last warn
        # monotonic timestamp.
        self._bad_value_warn_window = self.BAD_VALUE_WARN_WINDOW_SEC
        self._last_bad_value_warn: dict[str, float] = {}
        self._last_clock: dict[str, float] = {}
        # Without authenticated run context a backwards clock cannot be assigned
        # to a new run. Keep it uncertain for this connector lifetime.
        self._reset_clocks: set[str] = set()

    def healthy(self) -> bool:
        """Return whether the connector is reachable per the most recent poll cycle."""
        return self._healthy

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
        if self._cfg.clocks:
            await self._poll_with_clocks()
            return
        if not self._cfg.points:
            # Nothing to poll — the stack is up, so report healthy.
            self._healthy = True
            return

        requests = [(pt.local_id, "presentValue") for pt in self._cfg.points]
        chunk_size = self._cfg.rpm_chunk_size
        any_success = False
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

            any_success = True
            for obj_id, value, status in results:
                pt = self._point_map.get(obj_id)
                if pt is None:
                    # Unconfigured point — skip silently (not our concern).
                    continue
                if value is None:
                    # A configured point returned a non-numeric/None value. Warn
                    # (naming the point) but rate-limit per local_id so a
                    # persistently-bad point does not flood the logs; still skip.
                    self._warn_bad_value(pt.local_id, status)
                    continue
                await self._publish(pt, value, bacnet_quality(status))

        # A cycle is healthy if any chunk read succeeded this pass.
        self._healthy = any_success

    async def _read_clock(self, clock: ClockConfig) -> float | None:
        """Probe read permission/presence and units on every boundary; no cache.

        Separate requests avoid relying on the client's object-keyed RPM result
        representation for multiple properties of the same object.
        """
        try:
            values = []
            for prop in (clock.property, "units"):
                rows = await asyncio.wait_for(self._bacnet.read_property_multiple(
                    self._cfg.bacnet_address, self._cfg.bacnet_device_id,
                    [(clock.local_id, prop)],
                ), timeout=self._cfg.read_timeout)
                if len(rows) != 1 or rows[0][0] != clock.local_id or bacnet_quality(rows[0][2]) != "Good":
                    return None
                values.append(rows[0][1])
            value, unit = values
            # BACnet EngineeringUnits.seconds = 73. bacpypes3 may expose the
            # enumeration numerically; test adapters can return its label.
            if isinstance(unit, bool) or unit not in ("seconds", 73):
                return None
            if isinstance(value, bool) or not isinstance(value, (float, int)) or not math.isfinite(value) or value < 0:
                return None
            return float(value)
        except Exception as exc:
            logger.warning("bacnet: clock read failed clock_id=%s: %s", clock.clock_id, exc)
            return None

    async def _poll_with_clocks(self) -> None:
        """Fence the entire poll and every chunk; publish only after checking.

        Equality is observational evidence only: a reset and return to the same
        value between reads is undetectable without Testbed's step fence.
        """
        samples: dict[str, list[float | None]] = {c.clock_id: [] for c in self._cfg.clocks}

        async def boundary() -> None:
            for c in self._cfg.clocks:
                value = await self._read_clock(c)
                samples[c.clock_id].append(value)
                if value is not None:
                    previous = self._last_clock.get(c.clock_id)
                    if previous is not None and value < previous:
                        self._reset_clocks.add(c.clock_id)
                    self._last_clock[c.clock_id] = value

        await boundary()
        pending = []
        any_success = False
        for start in range(0, len(self._cfg.points), self._cfg.rpm_chunk_size):
            points = self._cfg.points[start:start + self._cfg.rpm_chunk_size]
            chunk_ids = {p.local_id for p in points}
            try:
                rows = await asyncio.wait_for(self._bacnet.read_property_multiple(
                    self._cfg.bacnet_address, self._cfg.bacnet_device_id,
                    [(p.local_id, "presentValue") for p in points],
                ), timeout=self._cfg.read_timeout)
                # Preserve the acquisition instant: rounding down to seconds can
                # put this timestamp before the start of a fractional-second poll.
                observed_at = datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")
                for obj_id, value, status in rows:
                    if obj_id not in chunk_ids:
                        continue
                    if value is None:
                        self._warn_bad_value(obj_id, status)
                        continue
                    any_success = True
                    pending.append((self._point_map[obj_id], value, bacnet_quality(status), observed_at))
            except Exception as exc:
                logger.warning("bacnet: clocked poll chunk [%d:%d] failed: %s", start, start + len(points), exc)
            await boundary()

        attributes = {}
        for c in self._cfg.clocks:
            values = samples[c.clock_id]
            if c.clock_id in self._reset_clocks:
                quality = "reset"
            elif any(v is None for v in values):
                quality = "missing"
            elif any(v != values[0] for v in values):
                quality = "changed"
            else:
                quality = "stable_unfenced"
            attrs = {"sim_clock_id": c.clock_id, "sim_config_revision": self._cfg.clock_revision,
                     "sim_time_quality": quality, "sim_run_status": "absent"}
            if quality == "stable_unfenced":
                attrs["sim_time_s"] = str(values[0])
            else:
                logger.warning("bacnet: uncertain clock association clock_id=%s sim_time_quality=%s", c.clock_id, quality)
            for local_id in c.point_local_ids:
                attributes[local_id] = attrs
        for pt, value, quality, timestamp in pending:
            await self._publish(pt, value, quality, attributes.get(pt.local_id), timestamp)
        self._healthy = any_success

    def _warn_bad_value(self, local_id: str, status: str | None) -> None:
        """Emit a rate-limited WARNING for a configured point with no numeric value.

        At most one WARNING per ``self._bad_value_warn_window`` seconds per
        ``local_id`` (monotonic clock), so a persistently-bad point stays visible
        without flooding the logs.
        """
        now = time.monotonic()
        last = self._last_bad_value_warn.get(local_id)
        if last is not None and (now - last) < self._bad_value_warn_window:
            return
        self._last_bad_value_warn[local_id] = now
        logger.warning(
            "bacnet: point %s returned no numeric value (status=%s) — skipping",
            local_id, status,
        )

    async def _subscribe_cov(self, pt: PointConfig) -> None:
        """Subscribe to COV for a single point and publish events on each change."""
        async def on_cov(obj_id: str, value: float | str | bool, status: str) -> None:
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

    async def _publish(self, pt: PointConfig, value: float | str | bool, quality: str,
                       attributes: dict[str, str] | None = None, timestamp: str | None = None) -> None:
        data = make_event(
            connector_id=self._cfg.connector_id,
            local_id=pt.local_id,
            device_ref=pt.device_ref,
            value=value,
            unit=pt.unit,
            quality=quality,
            attributes=attributes,
            timestamp=timestamp,
        )
        try:
            if self._cfg.managed_session:
                await self._js.publish(self._subject, data, headers={
                    "Nexus-Session": self._cfg.managed_session,
                    "Nexus-Config-Revision": self._cfg.clock_revision,
                })
            else:
                await self._js.publish(self._subject, data)
            logger.debug("bacnet: published %s=%s quality=%s", pt.local_id, value, quality)
        except Exception as exc:
            logger.warning("bacnet: NATS publish failed — withholding for %s: %s", pt.local_id, exc)
