# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Minimal stdlib health HTTP server.

Exposes ``GET /health`` returning 200 ``{"status":"ok"}`` when the injected
readiness callable is true, else 503 ``{"status":"degraded"}``. Runs in a daemon
thread so it never blocks process shutdown; binding failures are non-fatal.
"""
from __future__ import annotations

import logging
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Callable

logger = logging.getLogger(__name__)

_OK_BODY = b'{"status":"ok"}'
_DEGRADED_BODY = b'{"status":"degraded"}'


def _make_handler(readiness: Callable[[], bool]) -> type[BaseHTTPRequestHandler]:
    class HealthHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802 (stdlib naming)
            if self.path.split("?", 1)[0] != "/health":
                self.send_response(404)
                self.end_headers()
                return

            try:
                ready = bool(readiness())
            except Exception:  # a broken readiness callable must not 500-crash the server
                logger.warning("bacnet: health readiness check raised", exc_info=True)
                ready = False

            body = _OK_BODY if ready else _DEGRADED_BODY
            self.send_response(200 if ready else 503)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, format: str, *args: object) -> None:  # noqa: A002
            # Silence the default per-request stderr logging.
            pass

    return HealthHandler


def start_health_server(
    port: int,
    readiness: Callable[[], bool],
) -> ThreadingHTTPServer | None:
    """Start a health server on *port* in a daemon thread.

    Returns the server (call ``.shutdown()`` to stop it) or ``None`` if the bind
    failed — a bind failure is logged and non-fatal so the connector keeps running.
    """
    try:
        server = ThreadingHTTPServer(("0.0.0.0", port), _make_handler(readiness))
    except OSError as exc:
        logger.warning("bacnet: health server failed to bind port %d: %s", port, exc)
        return None

    thread = threading.Thread(
        target=server.serve_forever,
        name="bacnet-health",
        daemon=True,
    )
    thread.start()
    logger.info("bacnet: health server listening on :%d", server.server_address[1])
    return server
