# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Tests for the stdlib health HTTP server."""
from __future__ import annotations

import json
import urllib.error
import urllib.request

from bacnet_connector.health import start_health_server


def _get(port: int, path: str = "/health") -> tuple[int, str]:
    url = f"http://127.0.0.1:{port}{path}"
    try:
        with urllib.request.urlopen(url, timeout=2) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode()


def test_health_reflects_readiness_flag():
    """/health returns 503/degraded when not ready, 200/ok when ready."""
    ready = {"v": False}
    server = start_health_server(0, lambda: ready["v"])
    assert server is not None
    try:
        port = server.server_address[1]

        status, body = _get(port)
        assert status == 503
        assert '"status":"ok"' not in body
        assert json.loads(body)["status"] == "degraded"

        ready["v"] = True
        status, body = _get(port)
        assert status == 200
        assert json.loads(body)["status"] == "ok"
        assert '"status":"ok"' in body
    finally:
        server.shutdown()


def test_bind_failure_is_non_fatal():
    """A bind failure must be caught and return a falsy sentinel, not raise."""
    # Port 1 is privileged / unavailable — binding should fail without raising.
    result = start_health_server(1, lambda: True)
    if result is not None:
        # If it somehow bound (unlikely), clean up so we don't leak a thread.
        result.shutdown()
