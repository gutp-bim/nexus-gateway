# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Structured JSON logging for the BACnet connector.

Emits ONE JSON object per line on stderr with a fixed key set shared across all
connectors (``timestamp`` / ``level`` / ``connector_id`` / ``message``, plus
``exception`` when the record carries ``exc_info``). Do NOT rename these keys —
they are the cross-connector log contract.
"""
from __future__ import annotations

import datetime as _dt
import json
import logging
import os
import sys


class JsonFormatter(logging.Formatter):
    """Formats a LogRecord as a single-line JSON object with the shared key set."""

    def __init__(self, connector_id: str) -> None:
        super().__init__()
        self._connector_id = connector_id

    def format(self, record: logging.LogRecord) -> str:
        # Record creation time → ISO-8601 with a timezone offset (UTC).
        ts = _dt.datetime.fromtimestamp(record.created, tz=_dt.timezone.utc)
        payload = {
            "timestamp": ts.isoformat(),
            "level": record.levelname,
            "connector_id": self._connector_id,
            "message": record.getMessage(),
        }
        if record.exc_info:
            payload["exception"] = self.formatException(record.exc_info)
        return json.dumps(payload, ensure_ascii=False)


def configure(connector_id: str) -> None:
    """Install the JSON StreamHandler (stderr) on the root logger.

    The root level is taken from ``LOG_LEVEL`` (default ``INFO``). Any handlers
    already installed (e.g. a prior ``basicConfig``) are removed so exactly one
    JSON line is emitted per record.
    """
    root = logging.getLogger()

    level_name = os.environ.get("LOG_LEVEL", "INFO").strip().upper()
    root.setLevel(getattr(logging, level_name, logging.INFO))

    # Replace any pre-existing handlers so we don't double-log or emit plain text.
    for existing in list(root.handlers):
        root.removeHandler(existing)

    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(JsonFormatter(connector_id))
    root.addHandler(handler)
