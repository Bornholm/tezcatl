#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop 'tezcatl-ingest@*' 2>/dev/null || true
    systemctl stop tezcatl-metrics 2>/dev/null || true
    systemctl disable tezcatl-metrics 2>/dev/null || true
fi

exit 0
