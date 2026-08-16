#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAP_NAME="monthly" # name for the map snapshot
EDGE_DAYS=548       # 1.5 years
SKIP_DURABILITY=false

usage() {
	echo "usage: $0 [--map-name NAME] [--edge-days DAYS] [--skip-durability]" >&2
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--map-name|--name)
			[[ $# -ge 2 ]] || { usage; exit 2; }
			MAP_NAME="$2"
			shift 2
			;;
		--edge-days)
			[[ $# -ge 2 ]] || { usage; exit 2; }
			EDGE_DAYS="$2"
			shift 2
			;;
		--skip-durability)
			SKIP_DURABILITY=true
			shift
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			usage
			exit 2
			;;
	esac
done

[[ "$MAP_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || { echo "invalid map name: $MAP_NAME" >&2; exit 2; }
[[ "$EDGE_DAYS" =~ ^[0-9]+$ ]] && (( EDGE_DAYS >= 30 )) || { echo "edge days must be an integer of at least 30" >&2; exit 2; }

REPORT_ROOT="$PROJECT_DIR/reports/maps/$MAP_NAME"
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
BUILD_MAP_ARGS=(--edge-days "$EDGE_DAYS" --profile "$STAGING/profile/topics.csv" --out "$STAGING/graph")
if [[ "$SKIP_DURABILITY" == true ]]; then
	BUILD_MAP_ARGS+=(--skip-durability)
fi
"$BUILD_DIR/build-map" "${BUILD_MAP_ARGS[@]}"
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
if (nodes < 100 || layout.nodes !== nodes) {
	throw new Error(`layout validation failed: nodes=${nodes}, layout_nodes=${layout.nodes}`);
}
console.log(`validated layout: nodes=${nodes} ratio=${layout.edge_to_random_ratio.toFixed(3)}`);

NODE

printf '%s\n' "$STAMP" > "$STAGING/version"
mv "$STAGING" "$SNAPSHOT"
ln -s "$STAMP" "$REPORT_ROOT/.current-$STAMP"
mv -Tf "$REPORT_ROOT/.current-$STAMP" "$REPORT_ROOT/current"

echo "map snapshot published: $SNAPSHOT"