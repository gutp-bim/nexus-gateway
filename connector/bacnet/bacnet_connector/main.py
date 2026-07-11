# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Entry point: wire Config, BACnet client, NATS, and run the Connector."""
from __future__ import annotations

import asyncio
import logging
import signal
import sys

import nats
from nats.js import JetStreamContext

from bacnet_connector.bacnet_client import Bacpypes3Client
from bacnet_connector.config import Config, ConfigError
from bacnet_connector.connector import Connector
from bacnet_connector.health import start_health_server
from bacnet_connector.write_handler import WriteHandler

logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s")
logger = logging.getLogger(__name__)


async def _await_stream(js: JetStreamContext, subject: str, *, poll_interval: float = 5.0) -> None:
    """Block until the JetStream stream covering *subject* exists.

    The EVENTS stream is owned by the gateway; the connector must wait for it
    rather than publishing into a void and emitting WARNING noise on every poll.
    """
    while True:
        try:
            await js.find_stream_name_by_subject(subject)
            return
        except Exception:
            logger.info(
                "bacnet: EVENTS stream not ready for subject %s — waiting %.0fs "
                "(start the gateway, or create the stream manually)",
                subject, poll_interval,
            )
            await asyncio.sleep(poll_interval)


async def _run(cfg: Config) -> None:
    nc = await nats.connect(cfg.nats_url)
    js: JetStreamContext = nc.jetstream()

    subject = f"evt.bacnet.{cfg.connector_id}"
    await _await_stream(js, subject)

    bacnet = await Bacpypes3Client.create(cfg.local_address, read_timeout=cfg.read_timeout)

    stop_event = asyncio.Event()

    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stop_event.set)

    handler = WriteHandler(cfg, bacnet)
    cmd_subject = f"cmd.bacnet.{cfg.connector_id}"
    sub = await nc.subscribe(cmd_subject, cb=handler.handle)
    logger.info("bacnet: write handler subscribed to %s", cmd_subject)

    connector = Connector(cfg, bacnet, js)
    health_server = start_health_server(cfg.health_port, connector.healthy)
    try:
        await connector.run(stop_event=stop_event)
    finally:
        if health_server is not None:
            health_server.shutdown()
        try:
            await sub.unsubscribe()
        finally:
            await nc.drain()


def main() -> None:
    try:
        cfg = Config.from_env()
    except ConfigError as exc:
        # Fail fast with a single operator-facing line, not a raw traceback.
        logger.error("bacnet: configuration error: %s", exc)
        sys.exit(1)
    asyncio.run(_run(cfg))


if __name__ == "__main__":
    main()
