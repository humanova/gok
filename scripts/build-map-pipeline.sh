#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPORT_ROOT="$PROJECT_DIR/reports/maps"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
STAGING="$REPORT_ROOT/.building-$STAMP"
SNAPSHOT="$REPORT_ROOT/$STAMP"
BUILD_DIR="${TMPDIR:-/tmp}/gok-map-pipeline-$STAMP"

mkdir -p "$REPORT_ROOT" "$BUILD_DIR"
exec 9>/tmp/gok-map-pipeline.lock
if ! flock -n 9; then
	echo "map pipeline is already running" >&2
	exit 1
fi

cleanup() {
	rm -rf "$STAGING" "$BUILD_DIR"
}
trap cleanup EXIT

cd "$PROJECT_DIR"
mkdir -p "$STAGING"/{profile,graph,community-regions,node-regions,layout}

go build -o "$BUILD_DIR/profile-map" ./cmd/map/profile-map
go build -o "$BUILD_DIR/build-map" ./cmd/map/build-map
go build -o "$BUILD_DIR/reconcile-map" ./cmd/map/reconcile-map
go build -o "$BUILD_DIR/reconcile-map-nodes" ./cmd/map/reconcile-map-nodes
go build -o "$BUILD_DIR/layout-map" ./cmd/map/layout-map

"$BUILD_DIR/profile-map" --days 0 --out "$STAGING/profile"
"$BUILD_DIR/build-map" --profile "$STAGING/profile/topics.csv" --out "$STAGING/graph"
"$BUILD_DIR/reconcile-map" --clusters "$STAGING/graph/clusters.csv" --out "$STAGING/community-regions"
"$BUILD_DIR/reconcile-map-nodes" \
	--nodes "$STAGING/graph/nodes.csv" \
	--clusters "$STAGING/graph/clusters.csv" \
	--community-regions "$STAGING/community-regions/semantic-regions.json" \
	--out "$STAGING/node-regions"
"$BUILD_DIR/layout-map" \
	--nodes "$STAGING/graph/nodes.csv" \
	--edges "$STAGING/graph/edges.csv" \
	--community-regions "$STAGING/community-regions/semantic-regions.json" \
	--node-regions "$STAGING/node-regions/node-regions.json" \
	--out "$STAGING/layout"

node - "$STAGING" <<'NODE'
const fs = require('fs');
const root = process.argv[2];
const layout = JSON.parse(fs.readFileSync(`${root}/layout/summary.json`, 'utf8'));
const nodes = fs.readFileSync(`${root}/layout/layout.csv`, 'utf8').trim().split(/\r?\n/).length - 1;
if (nodes < 100 || layout.nodes !== nodes || layout.edge_to_random_ratio >= 0.15) {
  throw new Error(`layout validation failed: nodes=${nodes}, ratio=${layout.edge_to_random_ratio}`);
}
console.log(`validated layout: nodes=${nodes} ratio=${layout.edge_to_random_ratio.toFixed(3)}`);
NODE

printf '%s\n' "$STAMP" > "$STAGING/version"
mv "$STAGING" "$SNAPSHOT"
ln -s "$STAMP" "$REPORT_ROOT/.current-$STAMP"
mv -Tf "$REPORT_ROOT/.current-$STAMP" "$REPORT_ROOT/current"

echo "map snapshot published: $SNAPSHOT"