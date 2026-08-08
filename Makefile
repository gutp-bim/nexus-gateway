GOBIN ?= $(HOME)/go/bin
GO    ?= go
BUF   ?= $(GOBIN)/buf

# Absolute paths are required for docker compose -f to avoid Docker Compose v5
# treating pyproject.toml-bearing build contexts (connector/bacnet) as sub-projects.
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
COMPOSE_BASE := -f $(ROOT)/docker-compose.yml -f $(ROOT)/docker-compose.integration.yml

OPCUA_ENDPOINT  ?= opc.tcp://localhost:4840
BACNET_ADDRESS  ?= localhost

.PHONY: generate build test lint clean \
        e2e-up-opcua e2e-up-bacnet e2e-up-both e2e-down e2e-preflight \
        e2e-opcua e2e-bacnet e2e-both

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
#
# These build the gateway image rather than reusing whatever `nexus-gateway-gateway`
# happens to be on the host. The compose service has no `image:` tag, so a reused
# layer can predate the routes the healthcheck requires — that is how a 24h soak ran
# for 12+ hours against an image without /health/live (#120). Run `make e2e-preflight`
# after bring-up to confirm the running image satisfies the healthcheck contract.

e2e-up-opcua:
	OPCUA_ENDPOINT=$(OPCUA_ENDPOINT) \
	  docker compose $(COMPOSE_BASE) --profile opcua-remote up -d --build

e2e-up-bacnet:
	BACNET_ADDRESS=$(BACNET_ADDRESS) \
	  docker compose $(COMPOSE_BASE) --profile bacnet-remote up -d --build

e2e-up-both:
	OPCUA_ENDPOINT=$(OPCUA_ENDPOINT) BACNET_ADDRESS=$(BACNET_ADDRESS) \
	  docker compose $(COMPOSE_BASE) --profile opcua-remote --profile bacnet-remote up -d --build

e2e-down:
	docker compose $(COMPOSE_BASE) --profile opcua-remote --profile bacnet-remote down

# Fail fast when the running image does not satisfy the healthcheck contract (#120).
# /health reports the build; /health/live is what Docker actually probes — a 404 there
# leaves the container reported unhealthy for the whole run even while it processes
# telemetry fine, which is indistinguishable from a real liveness fault.
GATEWAY_ADMIN_URL ?= http://localhost:18080
e2e-preflight:
	@echo "preflight: $(GATEWAY_ADMIN_URL)"
	@curl -sf $(GATEWAY_ADMIN_URL)/health >/dev/null \
	  || { echo "FAIL: /health did not answer"; exit 1; }
	@curl -sf $(GATEWAY_ADMIN_URL)/health/live | grep -q '"status":"ok"' \
	  || { echo "FAIL: /health/live missing or not ok — the running image predates the healthcheck contract; bring up with 'up -d --build'"; exit 1; }
	@echo "preflight OK: $$(curl -sf $(GATEWAY_ADMIN_URL)/health/live)"

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
