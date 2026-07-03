# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The project is **pre-1.0**; the public API, wire contracts, and configuration
surface may change between minor versions until a `1.0.0` release.

## [Unreleased]

### Added
- MQTT connector entrypoint (`cmd/mqtt-connector/main.go`) and `connector/mqtt/Dockerfile`; deploy with `docker-compose.mqtt.yml`.

## [0.1.0] - 2026-06-21

First public preview (pre-MVP). MVP baseline is OPC-UA telemetry/control +
Store-and-Forward; see `MVP_READINESS.md`.

### Added
- Connectors: **1-minute polling baseline**. Every point is obtainable on at
  least a 1-minute cycle, with change-driven paths (BACnet COV, OPC-UA
  subscriptions) layered on top as immediate updates.
- OPC-UA connector: **periodic re-poll** alongside the change-driven
  subscription (#110). Previously the connector polled once and then relied
  solely on the subscription, so static server values produced no telemetry
  after the initial burst.
- Store-and-Forward `storefwd_*` metrics on `/metrics`
  (`buffer_depth`, `written_total`, `sent_total`, `dropped_total`,
  `checkpoint_total`, `send_error_total`).
- Gateway flag `--dev-sim-interval` and `SIM_POLL_INTERVAL` env for the sim
  connector.
- OSS release scaffolding: `LICENSE` (Apache-2.0), `NOTICE`, SPDX headers,
  `CODE_OF_CONDUCT.md`, `CONTRIBUTING.md`, `SECURITY.md`, issue/PR templates,
  `CODEOWNERS`, Dependabot, and this `CHANGELOG.md`.

### Changed
- Default poll interval standardized to **60 s** across the BACnet, OPC-UA, and
  sim connectors (`BACNET_POLL_INTERVAL`, `OPCUA_POLL_INTERVAL`, sim
  `--interval`/`SIM_POLL_INTERVAL`).
- `storefwd_buffer_depth` / `Buffer.Depth()` now report the **un-forwarded
  backlog** (frames beyond the ack cursor) instead of the total retained rows.
- BACnet connector reads are chunked (`BACNET_RPM_CHUNK_SIZE`) and bounded
  (`BACNET_READ_TIMEOUT`) to survive devices without segmentation and slow
  responders.

### Fixed
- Store-and-Forward: serialize SQLite writers (single connection + WAL +
  `busy_timeout`) to eliminate `SQLITE_BUSY` stalls that froze forwarding under
  high write rates (#109).
- sim connector: clamp a non-positive interval to the default to avoid a
  `time.NewTicker` panic.

### Security
- Scrubbed real lab/building-OS IP addresses from tracked files, replacing them
  with RFC 5737 documentation addresses (`192.0.2.0/24`).

[Unreleased]: https://github.com/takashikasuya/nexus-gateway/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/takashikasuya/nexus-gateway/releases/tag/v0.1.0
