# Soak and resource-evaluation runs

How to run a long-duration test whose memory numbers mean something.

## Why this is separate from `docker compose up`

The base stack is built for development. It starts five services unconditionally
and makes the gateway wait for Keycloak to report healthy before it boots — both
deliberate, and both wrong for a resource evaluation.

The 24h THX run showed the cost. Host free memory fell to roughly 2.5 GB of a
6.69 GiB Docker allocation, and about 3.0 GiB of that was Keycloak: three
instances across three parallel Compose projects, none of them under test, since
the gateway advertises no `KEYCLOAK_*` configuration by default and validates no
tokens. The resulting per-container table put that 3.0 GiB in the same column as
the gateway's own growth, and after the fact there is no way to separate
"the gateway is leaking" from "the VM ran out and everything got squeezed".

## Roles

Every container in a run falls into one of three buckets. `scripts/soak-record.sh`
stamps the role onto every sample, so the distinction is a field in the data
rather than something the reader has to reconstruct.

| Role | Services | Why |
|---|---|---|
| `sut` | `gateway`, plus any connector containers the gateway starts at runtime | What is being measured. Connectors are named from the catalog and cannot be listed ahead of time, so anything unclassified defaults to `sut` — over-inclusion is the safe error. |
| `dependency` | `nats`, `mock-bos` | Inside the measurement boundary but not the subject. NATS RSS tracks JetStream retention, so it must be reported separately rather than folded into "the gateway's memory" — during the 24h run `BUILDING_OS_VALIDATED` held ~505k messages / 174 MB, and NATS at 145 MiB is plausibly the working set that implies. |
| `out-of-scope` | `keycloak`, `admin-ui` | Started by the base stack, exercised by nothing. Deactivated by the soak overlay; the role exists so a sample still classifies them correctly if someone runs without the overlay. |

## Running one

```bash
# 1. Bring up only what the run needs. --build is not optional: a soak against a
#    stale image measures a build nobody can identify afterwards (#120).
make soak-up

# 2. Gate on host resources and pin what is about to run.
make soak-preflight

# 3. Sample for the duration. Runs until interrupted when SOAK_DURATION=0.
make soak-record SOAK_DURATION=259200 SOAK_INTERVAL=60   # 72h at 1/min

# 4. Tear down.
make soak-down
```

Against a real Building OS instead of `mock-bos`, layer the live-BOS overlay —
it deactivates `mock-bos` rather than leaving it idle, and composes with the soak
overlay in either file order:

```bash
docker compose -f docker-compose.yml -f docker-compose.soak.yml \
               -f docker-compose.live-bos.yml up -d --build

# so the recorded compose-resolved.yml describes the run that actually happened
SOAK_COMPOSE_FILES="-f docker-compose.yml -f docker-compose.soak.yml -f docker-compose.live-bos.yml" \
  make soak-preflight
```

## What preflight refuses, and why

`scripts/soak-preflight.sh` fails the run rather than warning, because a run that
starts short of memory produces a graph that looks like a finding.

| Check | Default | Override |
|---|---|---|
| Docker's total memory allocation | 4 GiB | `SOAK_MIN_DOCKER_MEM_GIB` |
| Free memory inside the Docker VM | 2 GiB | `SOAK_MIN_FREE_MEM_GIB` |
| Free space on Docker's data root | 10 GiB | `SOAK_MIN_FREE_DISK_GIB` |
| `/health` and `/health/live` answer, and `/health/live` reports `ok` | — | `--skip-health` when preflighting before bring-up |

Memory is read from inside the Docker VM, not from the host OS. On macOS and
Windows the host's free memory says nothing about what containers can obtain,
and the numbers that bounded the THX run were the VM's. On native Linux the VM
*is* the host, so it is one measurement either way.

Other Compose projects running alongside are reported, not failed on — a real
Building OS stack next door is a legitimate configuration, and it is the reader
who needs to know it was there.

## What gets recorded

`--out DIR` produces:

- `manifest.env` — allocation, free memory, free disk, `/health/live` (which
  carries the build version and VCS revision), and the other Compose projects
  that were running.
- `compose-resolved.yml` — `docker compose config` output: which services were
  actually active after profiles and overrides, with every resolved value.
- `images.txt` — image ID per container, plus repo digests for pulled images.
  The **image ID** is the primary key: locally-built images have no digest, and
  a stale build and a fresh one both report version `0.1.0`.
- `samples.jsonl` — one object per interval:

```json
{"ts":"2026-08-08T20:00:00Z",
 "vm":{"mem_total_kb":7012352,"mem_available_kb":2621440,"swap_total_kb":1048576,"swap_free_kb":1048576},
 "project":{"name":"nexus-gateway","containers":3,"mem_bytes":521142272},
 "containers":[{"service":"gateway","name":"nexus-gateway-gateway-1","role":"sut",
                "mem_bytes":295279001,"mem_pct":"4.31%","cpu_pct":"1.20%",
                "restarts":0,"oom_killed":false,"health":"healthy"}]}
```

`restarts`, `oom_killed` and VM free memory are the fields that decide whether a
rising `mem_bytes` is a leak or contention. None of the three were captured
during the 24h run, which is why its memory growth (Connector Worker 281.6 →
497.5 MiB, NATS 45.0 → 145.0 MiB) could not be adjudicated.

## Memory limits are deliberately not set

No `mem_limit` or `deploy.resources` appears in the soak overlay. Capping a
container changes what is being measured — it reclaims and OOM-kills at the cap
instead of revealing its natural working set, and the working set is the open
question. Add limits only when the question has changed to "does it survive
under a cap".

## Checking the overlays

`make compose-check` runs `docker compose config --quiet` over each supported
file combination. The failure it exists to catch is a `depends_on` entry left
pointing at a service an overlay just deactivated via an unmatched profile;
`!override` and `required: false` are what prevent it, and Compose's own
validator is the only thing that can confirm they still do.
