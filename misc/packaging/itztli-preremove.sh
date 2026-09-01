#!/bin/sh
set -e

# deb : $1 vaut "remove" à la désinstallation, "upgrade" lors d'une mise
# à jour — ne rien stopper dans ce cas, le postinstall du nouveau paquet
# redémarre l'unité.
case "${1:-}" in
upgrade | failed-upgrade | deconfigure)
    exit 0
    ;;
esac

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop tezcatl-itztli 2>/dev/null || true
    systemctl disable tezcatl-itztli 2>/dev/null || true
fi

exit 0
