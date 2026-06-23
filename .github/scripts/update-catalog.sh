#!/usr/bin/env bash
# update-catalog.sh — update a connector entry in fixtures/catalog.json
# Usage: update-catalog.sh <connector-name> <image> <digest> <version> <catalog-file>
set -euo pipefail

CONNECTOR_NAME="$1"
IMAGE="$2"
DIGEST="$3"
VERSION="$4"
CATALOG_FILE="$5"

if ! command -v jq &>/dev/null; then
  echo "error: jq is required" >&2
  exit 1
fi

tmp=$(mktemp)
jq --arg name    "$CONNECTOR_NAME" \
   --arg image   "$IMAGE" \
   --arg digest  "$DIGEST" \
   --arg version "$VERSION" \
   'map(if .name == $name then .image = $image | .digest = $digest | .version = $version else . end)' \
   "$CATALOG_FILE" > "$tmp"
mv "$tmp" "$CATALOG_FILE"

echo "catalog: updated $CONNECTOR_NAME → $IMAGE@$DIGEST (v$VERSION)"
