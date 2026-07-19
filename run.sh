#!/bin/bash
set -euo pipefail

PROJ_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Reap stale processes that still run a deleted executable from this repo.
# If a live gok/digen process from this repo exists, refuse to start to
# prevent duplicate schedulers.
cleanup_and_guard_processes() {
	local deleted_found=0
	local live_found=0
	local pid exe target

	for target in "$PROJ_DIR/gok" "$PROJ_DIR/digen"; do
		for pid_dir in /proc/[0-9]*; do
			pid="${pid_dir##*/}"

			# Skip if process disappeared between globbing and inspection.
			[[ -e "$pid_dir/exe" ]] || continue

			exe="$(readlink "$pid_dir/exe" 2>/dev/null || true)"
			[[ -n "$exe" ]] || continue

			if [[ "$exe" == "$target (deleted)" ]]; then
				echo "killing stale process pid=$pid exe='$exe'"
				kill "$pid" 2>/dev/null || true
				deleted_found=1
				continue
			fi

			if [[ "$exe" == "$target" ]]; then
				echo "existing process detected pid=$pid exe='$exe'"
				live_found=1
			fi
		done
	done

	if [[ "$deleted_found" -eq 1 ]]; then
		# Give killed stale processes a moment to exit before continuing.
		sleep 1
	fi

	if [[ "$live_found" -eq 1 ]]; then
		echo "refusing to start: gok/digen already running from $PROJ_DIR" >&2
		echo "stop existing processes first, then rerun run.sh" >&2
		exit 1
	fi
}

cleanup_and_guard_processes

exec 9>/tmp/gok-run.lock
if ! flock -n 9; then
	echo "gok is already running; refusing to start a second scheduler" >&2
	exit 1
fi

echo $$ > /tmp/gok.pid
trap 'kill $(jobs -p) 2>/dev/null; rm -f /tmp/gok.pid; exit' INT TERM

(exec 9>&-; while true; do ./gok >> log.txt 2>&1 && break; done) &
(exec 9>&-; while true; do ./digen >> log.txt 2>&1 && break; done) &
wait