# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Tests for structured JSON logging (shared cross-connector log contract)."""
from __future__ import annotations

import io
import json
import logging

from bacnet_connector.logging_setup import JsonFormatter


def _make_record(
    msg: str,
    *,
    level: int = logging.INFO,
    args: tuple = (),
    exc_info=None,
) -> logging.LogRecord:
    return logging.LogRecord(
        name="bacnet_connector.test",
        level=level,
        pathname=__file__,
        lineno=1,
        msg=msg,
        args=args,
        exc_info=exc_info,
    )


def test_formatter_emits_the_four_required_keys():
    fmt = JsonFormatter("conn-abc")
    line = fmt.format(_make_record("device %s down", args=("d1",), level=logging.WARNING))

    obj = json.loads(line)
    assert set(obj) >= {"timestamp", "level", "connector_id", "message"}
    assert obj["connector_id"] == "conn-abc"
    assert obj["level"] == "WARNING"
    assert obj["message"] == "device d1 down"  # %-args are rendered
    # timestamp is ISO-8601 with a timezone offset.
    assert "T" in obj["timestamp"]
    assert obj["timestamp"].endswith("+00:00")
    # No exception field when exc_info is absent.
    assert "exception" not in obj


def test_formatter_includes_exception_when_exc_info_set():
    try:
        raise ValueError("boom")
    except ValueError:
        import sys

        rec = _make_record("failed", level=logging.ERROR, exc_info=sys.exc_info())

    obj = json.loads(JsonFormatter("conn-abc").format(rec))
    assert obj["level"] == "ERROR"
    assert "exception" in obj
    assert "ValueError: boom" in obj["exception"]
    assert "Traceback" in obj["exception"]


def test_handler_writes_one_json_line_per_record():
    stream = io.StringIO()
    handler = logging.StreamHandler(stream)
    handler.setFormatter(JsonFormatter("conn-xyz"))

    log = logging.getLogger("bacnet_connector.test.handler")
    log.setLevel(logging.INFO)
    log.propagate = False
    log.addHandler(handler)
    try:
        log.info("hello %d", 1)
        log.warning("second")
    finally:
        log.removeHandler(handler)

    lines = [ln for ln in stream.getvalue().splitlines() if ln]
    assert len(lines) == 2
    first = json.loads(lines[0])
    assert first["message"] == "hello 1"
    assert first["connector_id"] == "conn-xyz"
    assert json.loads(lines[1])["level"] == "WARNING"
