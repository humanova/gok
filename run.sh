#!/bin/bash
echo $$ > /tmp/gok.pid
trap 'kill $(jobs -p) 2>/dev/null; rm -f /tmp/gok.pid; exit' INT TERM

while true; do ./gok >> log.txt 2>&1 && break; done &
while true; do ./digen >> log.txt 2>&1 && break; done &
wait