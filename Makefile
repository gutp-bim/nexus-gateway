GOBIN ?= $(HOME)/go/bin
GO    ?= go
BUF   ?= $(GOBIN)/buf

# Absolute paths are required for docker compose -f to avoid Docker Compose v5
# treating pyproject.toml-bearing build contexts (connector/bacnet) as sub-projects.
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
COMPOSE_BASE := -f $(ROOT)/docker-compose.yml -f $(ROOT)/docker-compose.integration.yml

OPCUA_ENDPOINT  ?= opc.tcp://localhost:4840
BACNET_ADDRESS  ?= localhost

COMPOSE_SOAK := -f $(ROOT)/docker-compose.yml -f $(ROOT)/docker-compose.soak.yml

.PHONY: generate build test lint clean compose-check \
        e2e-up-opcua e2e-up-bacnet e2e-up-both e2e-down \
        e2e-opcua e2e-bacnet e2e-both \
        soak-up soak-preflight soak-record soak-down

generate:
	$(BUF) generate

build: generate
	$(GO) build ./...

test:
	$(GO) test -timeout 60s ./...

buf-breaking:
	$(BUF) breaking --against '.git#branch=master,subdir=proto'

clean:
	rm -f gen/*.go

# ── E2E integration targets ───────────────────────────────────────────────────
# Override OPCUA_ENDPOINT / BACNET_ADDRESS to point at your simulator or device:
#   make e2e-up-opcua OPCUA_ENDPOINT=opc.tcp://192.168.1.10:4840

e2e-up-opcua:
	OPCUA_ENDPOINT=$(OPCUA_ENDPOINT) \
	  docker compose $(COMPOSE_BASE) --profile opcua-remote up -d --no-build

e2e-up-bacnet:
	BACNET_ADDRESS=$(BACNET_ADDRESS) \
	  docker compose $(COMPOSE_BASE) --profile bacnet-remote up -d --no-build

e2e-up-both:
	OPCUA_ENDPOINT=$(OPCUA_ENDPOINT) BACNET_ADDRESS=$(BACNET_ADDRESS) \
	  docker compose $(COMPOSE_BASE) --profile opcua-remote --profile bacnet-remote up -d --no-build

e2e-down:
	docker compose $(COMPOSE_BASE) --profile opcua-remote --profile bacnet-remote down

e2e-opcua: e2e-up-opcua
	docker run --rm --network host \
	  -v $(ROOT):/workspace -w /workspace \
	  -e E2E_NATS_URL=nats://localhost:14222 \
	  -e E2E_ADMIN_URL=http://localhost:18080 \
	  golang:1.25-alpine \
	  go test ./integration/... -run 'TestE2E_(OpcUATelemetry|OpcUAControl)' -v -timeout 120s

e2e-bacnet: e2e-up-bacnet
	docker run --rm --network host \
	  -v $(ROOT):/workspace -w /workspace \
	  -e E2E_NATS_URL=nats://localhost:14222 \
	  golang:1.25-alpine \
	  go test ./integration/... -run 'TestE2E_(BacnetTelemetry|BacnetControl)' -v -timeout 120s

e2e-both: e2e-up-both
	docker run --rm --network host \
	  -v $(ROOT):/workspace -w /workspace \
	  -e E2E_NATS_URL=nats://localhost:14222 \
	  -e E2E_ADMIN_URL=http://localhost:18080 \
	  golang:1.25-alpine \
	  go test ./integration/... \
	  -run 'TestE2E_(OpcUATelemetry|BacnetTelemetry|OpcUAControl|BacnetControl)' \
	  -v -timeout 180s

# ── Compose validation ────────────────────────────────────────────────────────
# `config --quiet` is Compose's own validator, so it catches the one failure the
# overlays are actually prone to: a `depends_on` entry left pointing at a service
# an overlay has just deactivated via an unmatched profile. `!override` and
# `required: false` exist precisely to avoid that, and nothing else checks them.
# Needs no images and no daemon-side work — it only resolves the files.

compose-check:
	@for combo in \
	  "-f docker-compose.yml" \
	  "-f docker-compose.yml -f docker-compose.soak.yml" \
	  "-f docker-compose.yml -f docker-compose.live-bos.yml" \
	  "-f docker-compose.yml -f docker-compose.soak.yml -f docker-compose.live-bos.yml" \
	  "-f docker-compose.yml -f docker-compose.live-bos.yml -f docker-compose.soak.yml" \
	  "-f docker-compose.yml -f docker-compose.external-keycloak.yml" \
	  "-f docker-compose.yml -f docker-compose.integration.yml" ; do \
	    printf 'compose config %s ... ' "$$combo" ; \
	    ( cd $(ROOT) && docker compose $$combo config --quiet ) || exit 1 ; \
	    echo ok ; \
	done

# ── Soak / resource-evaluation targets (#121) ─────────────────────────────────
# The base stack starts Keycloak and the Admin UI whether or not a run touches
# them, and blocks the gateway on Keycloak's health. During the 24h evaluation
# that cost ~3.0 GiB across parallel stacks while free memory fell to ~2.5 GB —
# pressure that is indistinguishable from a gateway leak once it is in a graph.
# docker-compose.soak.yml drops both. See docs/soak-testing.md.
#
# Always builds: a soak that measures a stale image measures nothing (#120).

SOAK_OUT      ?= $(ROOT)/reports/soak
SOAK_INTERVAL ?= 60
SOAK_DURATION ?= 0

soak-up:
	docker compose $(COMPOSE_SOAK) up -d --build

soak-preflight:
	$(ROOT)/scripts/soak-preflight.sh --out $(SOAK_OUT)

soak-record:
	$(ROOT)/scripts/soak-record.sh --out $(SOAK_OUT) \
	  --interval $(SOAK_INTERVAL) --duration $(SOAK_DURATION)

soak-down:
	docker compose $(COMPOSE_SOAK) down
