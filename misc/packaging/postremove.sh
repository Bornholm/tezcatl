#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
fi

# L'utilisateur et /var/lib/tezcatl (l'état appris) sont volontairement
# conservés : les supprimer est une décision d'exploitation.

exit 0
