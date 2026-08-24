#!/bin/sh
set -e

# Utilisateur système dédié ; StateDirectory/RuntimeDirectory sont créés
# par systemd au démarrage du service.
if ! getent passwd tezcatl >/dev/null 2>&1; then
    useradd --system \
        --home-dir /var/lib/tezcatl \
        --no-create-home \
        --shell /usr/sbin/nologin \
        tezcatl 2>/dev/null ||
        useradd --system \
            --home-dir /var/lib/tezcatl \
            --no-create-home \
            --shell /sbin/nologin \
            tezcatl
fi

# Les secrets de server.env ne doivent être lisibles que par le service.
if [ -f /etc/tezcatl/server.env ]; then
    chown root:tezcatl /etc/tezcatl/server.env
    chmod 0640 /etc/tezcatl/server.env
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    echo "tezcatl-server installé : systemctl enable --now tezcatl-server"
fi

exit 0
