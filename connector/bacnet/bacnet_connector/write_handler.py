# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""BACnet write handler: subscribe to cmd.<protocol>.<connector_id>, execute WriteProperty."""
from __future__ import annotations

import asyncio
import logging

from bacnet_connector.command import ControlCommand, WriteReply
from bacnet_connector.config import Config
from bacnet_connector.connector import BACnetClient

logger = logging.getLogger(__name__)

_DEDUP_MAX = 1000  # maximum cached control_ids kept in memory


class WriteHandler:
    """Handles write commands arriving on cmd.bacnet.<connector_id>.

    Designed for direct testing via handle() without a real NATS connection.
    Wire up the NATS subscription separately in main.py.
    """

    def __init__(self, cfg: Config, bacnet: BACnetClient) -> None:
        self._cfg = cfg
        self._bacnet = bacnet
        self._point_map = {pt.local_id: pt for pt in cfg.points}
        self._dedup: dict[str, WriteReply] = {}

    async def handle(self, msg: object) -> None:
        """Process one command message and reply via msg.respond()."""
        try:
            cmd = ControlCommand.from_bytes(msg.data)  # type: ignore[union-attr]
        except Exception as exc:
            logger.warning("bacnet: write handler received unparseable command: %s", exc)
            await msg.respond(WriteReply(success=False, response="bad_request").encode())  # type: ignore[union-attr]
            return

        # Idempotency: reserve sentinel before first await to prevent TOCTOU double-write.
        if cmd.control_id in self._dedup:
            cached = self._dedup[cmd.control_id]
            if cached is not None:
                await msg.respond(cached.encode())  # type: ignore[union-attr]
            return  # in-flight duplicate: drop silently

        self._dedup[cmd.control_id] = None  # type: ignore[assignment]  # reserve slot
        result = await self._execute(cmd)
        self._cache(cmd.control_id, result)
        await msg.respond(result.encode())  # type: ignore[union-attr]

    async def _execute(self, cmd: ControlCommand) -> WriteReply:
        pt = self._point_map.get(cmd.local_id)
        if pt is None or not pt.writable:
            return WriteReply(success=False, response="not_writable")

        priority = cmd.priority if cmd.priority > 0 else self._cfg.default_write_priority

        try:
            await asyncio.wait_for(
                self._bacnet.write_property(
                    self._cfg.bacnet_address,
                    self._cfg.bacnet_device_id,
                    cmd.local_id,
                    "presentValue",
                    cmd.value,
                    priority,
                ),
                timeout=self._cfg.write_timeout,
            )
            logger.info(
                "bacnet: wrote %s=%s priority=%d for control_id=%s",
                cmd.local_id, cmd.value, priority, cmd.control_id,
            )
            return WriteReply(success=True, response="ok")
        except asyncio.TimeoutError:
            logger.warning("bacnet: write timed out for %s control_id=%s", cmd.local_id, cmd.control_id)
            return WriteReply(success=False, response="timeout")
        except Exception as exc:
            logger.warning("bacnet: write device error for %s: %s", cmd.local_id, exc)
            return WriteReply(success=False, response=f"device_error: {exc}")

    def _cache(self, control_id: str, result: WriteReply) -> None:
        if len(self._dedup) >= _DEDUP_MAX:
            # Evict the oldest entry (insertion-ordered dict, Python 3.7+)
            del self._dedup[next(iter(self._dedup))]
        self._dedup[control_id] = result
