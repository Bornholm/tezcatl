#!/bin/sh
set -e

# Utilisateur système dédié : itztli n'a besoin d'aucun accès local, il
# parle au serveur tezcatl en TCP (ou unix, en l'ajoutant au groupe
# tezcatl).
if ! getent passwd itztli >/dev/null 2>&1; then
    useradd --system \
        --home-dir /nonexistent \
        --no-create-home \
        --shell /usr/sbin/nologin \
        itztli 2>/dev/null ||
        useradd --system \
            --home-dir /nonexistent \
            --no-create-home \
            --shell /sbin/nologin \
            itztli
fi

# Le mot de passe d'itztli.env ne doit être lisible que par le service.
if [ -f /etc/tezcatl/itztli.env ]; then
    chown root:itztli /etc/tezcatl/itztli.env
    chmod 0640 /etc/tezcatl/itztli.env
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true

    # Mise à jour : redémarre l'unité activée pour prendre le nouveau
    # binaire ; à l'installation initiale rien n'est encore activé.
    if systemctl is-enabled --quiet tezcatl-itztli 2>/dev/null; then
        systemctl restart tezcatl-itztli || true
    else
        echo "tezcatl-itztli installé :"
        echo "  1. définissez ITZTLI_PASSWORD dans /etc/tezcatl/itztli.env"
        echo "  2. systemctl enable --now tezcatl-itztli"
        echo "  réglages dans /etc/tezcatl/itztli.yaml (http://127.0.0.1:8484)"
    fi
fi

exit 0
