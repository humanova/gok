#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPORT_ROOT="$PROJECT_DIR/reports/maps"
SOURCE="$REPORT_ROOT/current"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
STAGING="$REPORT_ROOT/.building-$STAMP"
SNAPSHOT="$REPORT_ROOT/$STAMP"
BUILD_DIR="${TMPDIR:-/tmp}/gok-map-layout-$STAMP"

if [[ ! -f "$SOURCE/graph/nodes.csv" || ! -f "$SOURCE/graph/edges.csv" || ! -f "$SOURCE/community-regions/semantic-regions.json" || ! -f "$SOURCE/node-regions/node-regions.json" ]]; then
	printf 'current map snapshot is missing layout inputs\n' >&2
	exit 1
fi

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
mkdir -p "$STAGING"
cp -a "$SOURCE/graph" "$SOURCE/community-regions" "$SOURCE/node-regions" "$STAGING/"

go build -o "$BUILD_DIR/layout-map" ./cmd/map/layout-map
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

echo "map layout snapshot published: $SNAPSHOT"