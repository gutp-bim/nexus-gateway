# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Fail-fast configuration validation matrix (#31)."""
from __future__ import annotations

import pytest

from bacnet_connector.config import Config, ConfigError

# A minimal environment that produces a valid Config. Each test starts from this
# and applies overrides; a value of None deletes the variable.
_VALID_ENV = {
    "CONNECTOR_ID": "bacnet-01",
    "BACNET_ADDRESS": "127.0.0.1",
    "BACNET_DEVICE_ID": "1001",
}


def _apply(monkeypatch: pytest.MonkeyPatch, overrides: dict[str, str | None]) -> None:
    env = {**_VALID_ENV, **overrides}
    for key, value in env.items():
        if value is None:
            monkeypatch.delenv(key, raising=False)
        else:
            monkeypatch.setenv(key, value)
    # Ensure optional vars from a polluted ambient environment don't leak in.
    for key in (
        "BACNET_POINTS", "BACNET_POLL_INTERVAL", "BACNET_RPM_CHUNK_SIZE",
        "BACNET_READ_TIMEOUT", "BACNET_DEFAULT_WRITE_PRIORITY", "BACNET_WRITE_TIMEOUT",
        "HEALTH_PORT",
    ):
        if key not in overrides:
            monkeypatch.delenv(key, raising=False)


def test_valid_config_unchanged(monkeypatch: pytest.MonkeyPatch) -> None:
    _apply(monkeypatch, {"BACNET_POINTS": '[{"local_id": "analogInput,1", "unit": "degC"}]'})
    cfg = Config.from_env()
    assert cfg.connector_id == "bacnet-01"
    assert cfg.bacnet_address == "127.0.0.1"
    assert cfg.bacnet_device_id == 1001
    assert len(cfg.points) == 1
    assert cfg.points[0].local_id == "analogInput,1"
    assert cfg.points[0].unit == "degC"


# (overrides, expected substring naming the offender)
_CASES: list[tuple[dict[str, str | None], str]] = [
    ({"CONNECTOR_ID": None}, "CONNECTOR_ID"),
    ({"BACNET_ADDRESS": None}, "BACNET_ADDRESS"),
    ({"BACNET_DEVICE_ID": None}, "BACNET_DEVICE_ID"),
    ({"BACNET_DEVICE_ID": "not-an-int"}, "BACNET_DEVICE_ID"),
    ({"BACNET_POINTS": "{not json"}, "BACNET_POINTS"),
    ({"BACNET_POINTS": '{"local_id": "x"}'}, "BACNET_POINTS"),  # object, not array
    ({"BACNET_POINTS": '[{"unit": "degC"}]'}, "local_id"),       # missing key
    ({"BACNET_POINTS": '[{"local_id": ""}]'}, "local_id"),       # empty
    ({"BACNET_POINTS": '[{"local_id": 5}]'}, "local_id"),        # wrong type
    ({"BACNET_POINTS": '[{"local_id": "a", "writable": "yes"}]'}, "writable"),
    ({"BACNET_POINTS": '[{"local_id": "a", "unit": 7}]'}, "unit"),
    ({"BACNET_POLL_INTERVAL": "fast"}, "BACNET_POLL_INTERVAL"),
    ({"BACNET_RPM_CHUNK_SIZE": "1.5"}, "BACNET_RPM_CHUNK_SIZE"),
    ({"HEALTH_PORT": "abc"}, "HEALTH_PORT"),
    ({"BACNET_POLL_INTERVAL": "0"}, "BACNET_POLL_INTERVAL"),      # range check
    ({"BACNET_DEFAULT_WRITE_PRIORITY": "99"}, "BACNET_DEFAULT_WRITE_PRIORITY"),
    ({"BACNET_WRITE_TIMEOUT": "-1"}, "BACNET_WRITE_TIMEOUT"),
]


@pytest.mark.parametrize("overrides,expected", _CASES)
def test_invalid_config_names_offender(
    monkeypatch: pytest.MonkeyPatch, overrides: dict[str, str | None], expected: str
) -> None:
    _apply(monkeypatch, overrides)
    with pytest.raises(ConfigError) as exc_info:
        Config.from_env()
    message = str(exc_info.value)
    assert expected in message, f"error should name {expected!r}, got: {message}"
    # One-line, operator-facing — no multi-line traceback dump embedded.
    assert "\n" not in message


def test_index_named_for_second_point(monkeypatch: pytest.MonkeyPatch) -> None:
    _apply(
        monkeypatch,
        {"BACNET_POINTS": '[{"local_id": "ok,1"}, {"unit": "degC"}]'},
    )
    with pytest.raises(ConfigError) as exc_info:
        Config.from_env()
    assert "BACNET_POINTS[1]" in str(exc_info.value)
