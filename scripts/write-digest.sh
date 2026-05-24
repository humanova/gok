#!/usr/bin/env bash
# write-digest.sh — runs print-digest and writes output to /var/www/hmnv/eksi/
# Filename format: YYYY-MM-DD_HHMM.txt  (e.g. 2026-05-22_1400.txt)
# Lexicographic sort == chronological order in browser directory listings.

set -euo pipefail

PROJ="/home/humanova/projects/gok"
OUT_DIR="/var/www/hmnv/eksi"
FILENAME="$(date -u +'%Y-%m-%d_%H%M').txt"
OUTFILE="$OUT_DIR/$FILENAME"

cd "$PROJ"
"$PROJ/print-digest" "$OUTFILE"
