#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true

    # Mise à jour : relance l'unité si elle était activée, pour prendre
    # le nouveau binaire. À l'installation initiale rien n'est activé :
    # ingérer tout le journal d'une machine est une décision, pas un
    # défaut.
    if systemctl is-enabled --quiet tezcatl-journald 2>/dev/null; then
        systemctl restart tezcatl-journald || true
    else
        echo "tezcatl-plugin-journald installé :"
        echo "  systemctl enable --now tezcatl-journald   # ingérer le journal systemd"
        echo "  réglages dans /etc/tezcatl/journald.json"
    fi
fi

exit 0
