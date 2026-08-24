#!/bin/sh
set -e

# Retire le lien d'activation du plugin s'il est devenu orphelin.
if [ -L /var/lib/dokku/plugins/enabled/tezcatl ] && [ ! -e /var/lib/dokku/plugins/enabled/tezcatl ]; then
    rm -f /var/lib/dokku/plugins/enabled/tezcatl
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi

exit 0
