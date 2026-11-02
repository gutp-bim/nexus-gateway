# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Opt-in paused producer and command admission, controlled by the owning Core.

The runtime receipt describes the parsed native file, never canonical point IDs
or a run context. NATS ACLs must restrict this control subject to the Core.
"""
import asyncio
import json

from bacnet_connector.write_handler import WriteReply


class ManagedRuntime:
    def __init__(self, cfg, connector, nc, handler):
        self.cfg, self.connector, self.nc, self.handler = cfg, connector, nc, handler
        self.active = False
        self.lock = asyncio.Lock()
        self.wake = asyncio.Event()

    def receipt(self):
        return {"capability": "clock-runtime/1", "session": self.cfg.managed_session,
                "revision": self.cfg.clock_revision, "native_sha256": self.cfg.native_hash,
                "paused": not self.active}

    async def control(self, msg):
        try:
            request = json.loads(msg.data)
            if request["session"] != self.cfg.managed_session:
                raise ValueError("session mismatch")
            action = request["action"]
            if action == "quiesce":
                # Close first, then await the active read/write and all JS PUBACKs.
                self.active = False
                self.wake.clear()
                async with self.lock:
                    await self.nc.flush()
            elif action == "resume":
                if request.get("revision") != self.cfg.clock_revision or request.get("native_sha256") != self.cfg.native_hash:
                    raise ValueError("configuration mismatch")
                async with self.lock:
                    self.active = True
                    self.wake.set()
            elif action != "status":
                raise ValueError("unknown action")
            reply = self.receipt()
        except (ValueError, KeyError, TypeError):
            reply = {"error": "invalid_runtime_request"}
        await msg.respond(json.dumps(reply).encode())

    async def command(self, msg):
        async with self.lock:
            if not self.active:
                await msg.respond(WriteReply(False, "configuration_paused").encode())
                return
            await self.handler.handle(msg)

    async def run(self, stop):
        try:
            while not stop.is_set():
                try:
                    await asyncio.wait_for(self.wake.wait(), timeout=0.1)
                except asyncio.TimeoutError:
                    continue
                async with self.lock:
                    if self.active:
                        await self.connector._poll_all()
                try:
                    await asyncio.wait_for(stop.wait(), timeout=self.cfg.poll_interval)
                except asyncio.TimeoutError:
                    pass
        finally:
            self.active = False
            await self.connector._bacnet.close()
