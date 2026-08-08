#!/usr/bin/env bash
# Gate a long-running resource evaluation before it starts, and record what it
# is about to measure.
#
#   scripts/soak-preflight.sh [--out DIR] [--skip-health]
#
# Two jobs, both learned from the 24h THX run (#120, #121):
#
#   1. Refuse to start when the host cannot hold the run. That run finished with
#      ~2.5 GB free of a 6.69 GiB Docker allocation, most of it consumed by
#      services under no test. Memory pressure that arrives midway through looks
#      exactly like a leak in the graphs, and there is no way to tell the two
#      apart after the fact.
#   2. Write a manifest pinning WHAT ran: image IDs and digests, the gateway's
#      build revision, and the fully-resolved compose configuration. The same
#      run spent 12+ hours against an image predating the /health/live route
#      its own healthcheck required, and nothing recorded at the time could
#      have revealed it.
#
# Thresholds are advisory defaults, not physics — override per host:
#   SOAK_MIN_DOCKER_MEM_GIB (default 4)  Docker's total memory allocation
#   SOAK_MIN_FREE_MEM_GIB   (default 2)  free memory inside the Docker VM
#   SOAK_MIN_FREE_DISK_GIB  (default 10) free space on Docker's data root
set -euo pipefail

OUT_DIR=""
SKIP_HEALTH=0
# Which overlays the run uses, so compose-resolved.yml describes the run that
# actually happened. Override for a live-BOS soak:
#   SOAK_COMPOSE_FILES="-f docker-compose.yml -f docker-compose.soak.yml -f docker-compose.live-bos.yml"
read -r -a COMPOSE_FILES <<<"${SOAK_COMPOSE_FILES:--f docker-compose.yml -f docker-compose.soak.yml}"
GATEWAY_ADMIN_URL="${GATEWAY_ADMIN_URL:-http://localhost:18080}"

MIN_DOCKER_MEM_GIB="${SOAK_MIN_DOCKER_MEM_GIB:-4}"
MIN_FREE_MEM_GIB="${SOAK_MIN_FREE_MEM_GIB:-2}"
MIN_FREE_DISK_GIB="${SOAK_MIN_FREE_DISK_GIB:-10}"

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT_DIR="$2"; shift 2 ;;
    --skip-health) SKIP_HEALTH=1; shift ;;
    -h|--help) awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.."

fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 1; }
note() { echo "  $*"; }

command -v docker >/dev/null 2>&1 || fail "docker not on PATH"
docker info >/dev/null 2>&1 || fail "docker daemon not reachable"

echo "== resources =="

# Docker's own view of its allocation. On Docker Desktop this is the VM's size,
# which is the real ceiling — the Mac's 14.2 GB was never available to the run.
docker_mem_bytes="$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)"
docker_mem_gib=$(awk -v b="$docker_mem_bytes" 'BEGIN { printf "%.2f", b/1073741824 }')
note "docker allocation : ${docker_mem_gib} GiB (minimum ${MIN_DOCKER_MEM_GIB})"
awk -v have="$docker_mem_gib" -v want="$MIN_DOCKER_MEM_GIB" 'BEGIN { exit !(have >= want) }' \
  || fail "Docker is allocated ${docker_mem_gib} GiB, below the ${MIN_DOCKER_MEM_GIB} GiB minimum. Raise it in Docker Desktop → Resources, or lower SOAK_MIN_DOCKER_MEM_GIB if you accept the risk."

# Free memory has to be read from inside the VM: on macOS/Windows the host's
# own free memory says nothing about what containers can get, and /proc does
# not exist there at all. A throwaway container sees the VM's /proc/meminfo
# (cgroups limit what a container may *use*, they do not rewrite meminfo), and
# on native Linux the VM is the host, so this is one code path for both.
meminfo="$(docker run --rm --network none alpine:3 cat /proc/meminfo 2>/dev/null || true)"
[ -n "$meminfo" ] || fail "could not read /proc/meminfo from a container (is the alpine:3 image pullable?)"
mem_available_kb="$(awk '/^MemAvailable:/ { print $2 }' <<<"$meminfo")"
mem_total_kb="$(awk '/^MemTotal:/ { print $2 }' <<<"$meminfo")"
free_mem_gib=$(awk -v k="$mem_available_kb" 'BEGIN { printf "%.2f", k/1048576 }')
note "free memory (VM)  : ${free_mem_gib} GiB (minimum ${MIN_FREE_MEM_GIB})"
awk -v have="$free_mem_gib" -v want="$MIN_FREE_MEM_GIB" 'BEGIN { exit !(have >= want) }' \
  || fail "only ${free_mem_gib} GiB free inside the Docker VM. Stop the stacks that are not under test — 'docker ps' will show them — before starting a run whose whole output is a memory trend."

# Disk: JetStream retention writes for the length of the run, and a full data
# root ends the run in a way no memory graph explains.
#
# Measured from inside a container rather than by bind-mounting the host's `/`:
# a container's own root is an overlay on Docker's data root, so `df /` there
# reports exactly the filesystem that fills up. Bind-mounting `/` would report
# the *host's* disk on Docker Desktop — a different, larger, irrelevant number,
# and only when macOS file sharing happens to cover it.
data_root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || echo /var/lib/docker)"
free_disk_gib="$(docker run --rm --network none alpine:3 \
  df -k / 2>/dev/null | awk 'NR==2 { printf "%.2f", $4/1048576 }' || echo "")"
if [ -n "$free_disk_gib" ]; then
  note "free disk (VM)    : ${free_disk_gib} GiB backing ${data_root} (minimum ${MIN_FREE_DISK_GIB})"
  awk -v have="$free_disk_gib" -v want="$MIN_FREE_DISK_GIB" 'BEGIN { exit !(have >= want) }' \
    || fail "only ${free_disk_gib} GiB free for Docker's data root; JetStream retention will grow for the whole run."
else
  note "free disk (VM)    : unavailable (skipped)"
fi

# Stacks other than this one are the single biggest source of the pressure #121
# describes — three Keycloaks across three projects, none under test. Report
# rather than fail: a real Building OS stack alongside is a legitimate setup.
others="$(docker ps --format '{{.Label "com.docker.compose.project"}}' 2>/dev/null \
  | grep -v '^nexus-gateway$' | grep -v '^$' | sort -u || true)"
if [ -n "$others" ]; then
  echo "== other compose projects running (memory not attributable to the SUT) =="
  while IFS= read -r p; do
    n="$(docker ps -q --filter "label=com.docker.compose.project=$p" | wc -l | tr -d ' ')"
    note "$p ($n containers)"
  done <<<"$others"
fi

if [ "$SKIP_HEALTH" -eq 0 ]; then
  echo "== gateway health contract =="
  command -v curl >/dev/null 2>&1 \
    || fail "curl not on PATH; install it or pass --skip-health"
  curl -sf "$GATEWAY_ADMIN_URL/health" >/dev/null \
    || fail "/health did not answer at $GATEWAY_ADMIN_URL. Bring the stack up first, or pass --skip-health to preflight before bring-up."
  live="$(curl -sf "$GATEWAY_ADMIN_URL/health/live" || true)"
  grep -q '"status":"ok"' <<<"$live" \
    || fail "/health/live missing or not ok — the running image predates the healthcheck contract (#120). Bring up with 'up -d --build'."
  note "$live"
fi

if [ -n "$OUT_DIR" ]; then
  mkdir -p "$OUT_DIR"
  echo "== manifest =="

  # The fully-resolved topology: which services are actually active after
  # profiles and overrides, and every value the run will use.
  docker compose "${COMPOSE_FILES[@]}" config >"$OUT_DIR/compose-resolved.yml" 2>/dev/null \
    || note "compose config unavailable (non-fatal)"

  {
    echo "recorded_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "docker_mem_gib=$docker_mem_gib"
    echo "vm_mem_total_kb=$mem_total_kb"
    echo "vm_mem_available_kb=$mem_available_kb"
    echo "free_disk_gib=${free_disk_gib:-unknown}"
    echo "docker_root=$data_root"
    [ "$SKIP_HEALTH" -eq 0 ] && echo "gateway_health_live=${live}"
    echo "other_compose_projects=$(tr '\n' ',' <<<"$others" | sed 's/,$//')"
  } >"$OUT_DIR/manifest.env"

  # Image identity per running container. `.Image` is the resolved image ID —
  # the value that actually differs between a fresh build and the stale layer
  # #120 ran against, even though both report version 0.1.0. Digests are added
  # underneath for pulled images; locally-built images have none, which is
  # precisely why the ID is the primary key here.
  # `xargs -r` is GNU-only (BSD/macOS xargs rejects it), and macOS is exactly
  # where these runs happen, so guard on emptiness instead.
  running_ids="$(docker ps --filter "label=com.docker.compose.project=nexus-gateway" -q)"
  if [ -n "$running_ids" ]; then
    {
      # shellcheck disable=SC2086 # deliberate word splitting: one id per arg
      docker inspect $running_ids \
        --format '{{.Name}} image_id={{.Image}} image_ref={{.Config.Image}} created={{.Created}}'
      echo "--- repo digests (pulled images only) ---"
      # shellcheck disable=SC2086
      for img in $(docker inspect $running_ids --format '{{.Image}}' | sort -u); do
        docker image inspect "$img" --format '{{.Id}} {{join .RepoDigests ","}}'
      done
    } >"$OUT_DIR/images.txt" 2>/dev/null || true
  else
    echo "(no containers running)" >"$OUT_DIR/images.txt"
  fi

  note "wrote $OUT_DIR/{manifest.env,compose-resolved.yml,images.txt}"
fi

echo "PREFLIGHT OK"
