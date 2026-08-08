#!/usr/bin/env bash
# Sample resource use for the length of a soak run, classified by whether the
# container is under test.
#
#   scripts/soak-record.sh --out reports/soak-$(date +%Y%m%d) [--interval 60] [--duration 259200]
#
# Writes one JSON object per sample to $OUT/samples.jsonl. Runs until --duration
# elapses, or forever until interrupted.
#
# WHY THE CLASSIFICATION IS PART OF THE DATA
#
# The 24h run reported per-container memory and left the reader to decide what
# it meant. Two of the containers in that table (Keycloak, the Admin UI) were
# never exercised, and their ~1.3 GiB sat in the same column as the gateway's.
# Emitting a role alongside every sample makes "SUT memory" a query rather than
# an act of interpretation, and makes it obvious when something out of scope
# has crept back into the run.
#
# WHY HOST NUMBERS ARE HERE TOO
#
# Per-container RSS cannot distinguish "the gateway is growing" from "the VM is
# running out and everything is being squeezed". Free memory inside the Docker
# VM, restart counts, and the OOM-killed flag are what separate them, and none
# of the three were captured last time.
set -euo pipefail

OUT_DIR=""
INTERVAL="${SOAK_INTERVAL:-60}"
DURATION="${SOAK_DURATION:-0}" # 0 = until interrupted
PROJECT="${SOAK_PROJECT:-nexus-gateway}"

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT_DIR="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    --duration) DURATION="$2"; shift 2 ;;
    --project) PROJECT="$2"; shift 2 ;;
    -h|--help) awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[ -n "$OUT_DIR" ] || { echo "--out DIR is required" >&2; exit 2; }
mkdir -p "$OUT_DIR"
SAMPLES="$OUT_DIR/samples.jsonl"

# Roles are matched against the compose SERVICE name, not the container name,
# so they survive scaling and project renames. Anything unlisted is "sut":
# connectors the gateway starts at runtime are named from the catalog and
# cannot be enumerated ahead of time, and they are part of what is under test.
role_for() {
  case "$1" in
    nats|mock-bos) echo dependency ;;
    keycloak|admin-ui) echo out-of-scope ;;
    *) echo sut ;;
  esac
}

# docker stats renders "281.6MiB / 6.69GiB"; the report needs a number.
to_bytes() {
  awk -v s="$1" 'BEGIN {
    n = s + 0
    if (s ~ /KiB$/)      n *= 1024
    else if (s ~ /MiB$/) n *= 1048576
    else if (s ~ /GiB$/) n *= 1073741824
    else if (s ~ /kB$/)  n *= 1000
    else if (s ~ /MB$/)  n *= 1000000
    else if (s ~ /GB$/)  n *= 1000000000
    printf "%d", n
  }'
}

json_escape() { sed 's/\\/\\\\/g; s/"/\\"/g' <<<"$1"; }

sample_once() {
  local ts container_ids stats_out
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  container_ids="$(docker ps -q --filter "label=com.docker.compose.project=$PROJECT")"

  # VM-level memory, read from a container already in the run so the sampler
  # costs nothing per tick — a throwaway container every 60s for 72h is 4,000+
  # container starts polluting the very measurement being taken. Inside a
  # container /proc/meminfo reports the VM's figures (cgroups bound what a
  # container may use; they do not rewrite meminfo), which on native Linux are
  # the host's.
  #
  # NATS is probed first: it is alpine-based and certainly has `cat`, whereas
  # the gateway image may be distroless. Fall back to a one-shot container only
  # when nothing in the stack can be exec'd into.
  local meminfo="" probe
  probe="$(docker ps -q --filter "label=com.docker.compose.project=$PROJECT" \
             --filter "label=com.docker.compose.service=nats" | head -n1)"
  [ -n "$probe" ] || probe="$(head -n1 <<<"$container_ids")"
  if [ -n "$probe" ]; then
    meminfo="$(docker exec "$probe" cat /proc/meminfo 2>/dev/null || true)"
  fi
  case "$meminfo" in
    *MemAvailable*) ;;
    *) meminfo="$(docker run --rm --network none alpine:3 cat /proc/meminfo 2>/dev/null || true)" ;;
  esac

  local mem_total_kb mem_avail_kb swap_total_kb swap_free_kb
  mem_total_kb="$(awk '/^MemTotal:/ { print $2 }' <<<"$meminfo")"
  mem_avail_kb="$(awk '/^MemAvailable:/ { print $2 }' <<<"$meminfo")"
  swap_total_kb="$(awk '/^SwapTotal:/ { print $2 }' <<<"$meminfo")"
  swap_free_kb="$(awk '/^SwapFree:/ { print $2 }' <<<"$meminfo")"

  local containers_json="" total_mem=0
  if [ -n "$container_ids" ]; then
    # One stats call for the whole project; --no-stream so it returns instead
    # of streaming. Joined with inspect for restart/OOM/health, which stats
    # does not carry.
    stats_out="$(docker stats --no-stream --format '{{.ID}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.CPUPerc}}' $container_ids 2>/dev/null || true)"
    while IFS=$'\t' read -r cid memusage mempct cpupct; do
      [ -n "$cid" ] || continue
      local meta service name restarts oom health mem_bytes
      meta="$(docker inspect "$cid" --format '{{index .Config.Labels "com.docker.compose.service"}}|{{.Name}}|{{.RestartCount}}|{{.State.OOMKilled}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || true)"
      [ -n "$meta" ] || continue
      IFS='|' read -r service name restarts oom health <<<"$meta"
      mem_bytes="$(to_bytes "${memusage%% *}")"
      total_mem=$((total_mem + mem_bytes))
      containers_json="$containers_json,{\"service\":\"$(json_escape "$service")\",\"name\":\"$(json_escape "${name#/}")\",\"role\":\"$(role_for "$service")\",\"mem_bytes\":$mem_bytes,\"mem_pct\":\"${mempct}\",\"cpu_pct\":\"${cpupct}\",\"restarts\":${restarts:-0},\"oom_killed\":${oom:-false},\"health\":\"${health:-none}\"}"
    done <<<"$stats_out"
  fi

  printf '{"ts":"%s","vm":{"mem_total_kb":%s,"mem_available_kb":%s,"swap_total_kb":%s,"swap_free_kb":%s},"project":{"name":"%s","containers":%s,"mem_bytes":%s},"containers":[%s]}\n' \
    "$ts" \
    "${mem_total_kb:-0}" "${mem_avail_kb:-0}" "${swap_total_kb:-0}" "${swap_free_kb:-0}" \
    "$PROJECT" "$(grep -c . <<<"$container_ids" || true)" "$total_mem" \
    "${containers_json#,}" \
    >>"$SAMPLES"
}

echo "recording $PROJECT every ${INTERVAL}s → $SAMPLES"
[ "$DURATION" -gt 0 ] && echo "stopping after ${DURATION}s" || echo "stopping on interrupt"

started="$(date +%s)"
trap 'echo; echo "stopped after $(( $(date +%s) - started ))s, $(wc -l <"$SAMPLES" 2>/dev/null | tr -d " " || echo 0) samples"; exit 0' INT TERM

while :; do
  sample_once
  if [ "$DURATION" -gt 0 ] && [ $(( $(date +%s) - started )) -ge "$DURATION" ]; then
    break
  fi
  sleep "$INTERVAL"
done

echo "done: $(wc -l <"$SAMPLES" | tr -d ' ') samples in $SAMPLES"
