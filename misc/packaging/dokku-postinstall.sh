#!/bin/sh
set -e

# Active le plugin Dokku si Dokku est installé (les triggers sont lus
# depuis plugins/enabled).
if [ -d /var/lib/dokku/plugins/enabled ] && [ ! -e /var/lib/dokku/plugins/enabled/tezcatl ]; then
    ln -s /var/lib/dokku/plugins/available/tezcatl /var/lib/dokku/plugins/enabled/tezcatl
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true

    # Mise à jour : relance les unités d'ingestion activées (une par
    # application) et le collecteur de métriques, pour prendre le
    # nouveau binaire — et rattraper les arrêts faits par les anciens
    # preremove qui stoppaient aussi sur upgrade.
    for link in /etc/systemd/system/multi-user.target.wants/tezcatl-ingest@*.service; do
        [ -e "$link" ] || continue
        systemctl restart "$(basename "$link")" 2>/dev/null || true
    done

    if systemctl is-enabled --quiet tezcatl-metrics 2>/dev/null; then
        systemctl restart tezcatl-metrics || true
    fi
fi

echo "tezcatl-dokku installé :"
echo "  systemctl enable --now tezcatl-ingest@<app>   # logs, par application Dokku"
echo "  systemctl enable --now tezcatl-metrics        # métriques hôte + conteneurs"
echo "  cible et environnement dans /etc/tezcatl/ingest.env"

exit 0
