#!/bin/sh
set -e

# Chaque unité de tezcatl exécute /usr/bin/tezcatl. Remplacer le binaire
# ne change rien aux processus déjà lancés : ils continuent avec
# l'ancien code, et l'écart ne se voit qu'au moment où une commande
# neuve parle à un serveur ancien ("unknown method Forget for service
# tezcatl.v1.AdminService"). On redémarre donc ce qui tourne.
#
# Seulement ce qui tourne : sur une machine cliente, aucune de ces
# unités n'existe, et une unité arrêtée volontairement doit le rester.
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true

    units="tezcatl-server tezcatl-metrics tezcatl-journald"

    # Les unités d'ingestion Dokku sont instanciées par application.
    instances="$(systemctl list-units 'tezcatl-ingest@*' --state=active --no-legend 2>/dev/null | awk '{print $1}' || true)"
    if [ -n "$instances" ]; then
        units="$units $instances"
    fi

    for unit in $units; do
        if systemctl is-active --quiet "$unit" 2>/dev/null; then
            systemctl restart "$unit" || true
        fi
    done
fi

exit 0
