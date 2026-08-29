#!/bin/sh
# Installe ou met à jour tezcatl depuis les GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sh -s -- --variant dokku
#
# Variantes (jeux de paquets) :
#   client      tezcatl (CLI seule : ingest vers un serveur distant, mark/templates/top)
#   server      + tezcatl-server (unité systemd, /etc/tezcatl, utilisateur dédié)
#   docker      + tezcatl-plugin-host (métriques hôte + conteneurs Docker)
#   dokku       + tezcatl-dokku (ingestion des logs par app, hook post-deploy)
#   kubernetes  + tezcatl-plugin-kubernetes (events, logs de pods, changements
#               de workloads via kubeconfig — pour superviser un cluster depuis
#               cette machine ; en cluster, voir docs/deploy-kubernetes.md)
#
# Options / variables d'environnement :
#   --variant <v>    TEZCATL_VARIANT    variante (défaut : client)
#   --version <tag>  TEZCATL_VERSION    tag de release, ex. v0.7.0 (défaut : latest)
#   --force          TEZCATL_FORCE=1    réinstalle même si la version est déjà là
#   --download-only                     télécharge et vérifie sans installer
#                    TEZCATL_REPO       dépôt GitHub (défaut : bornholm/tezcatl)
#                    TEZCATL_PACKAGES   liste de paquets explicite (remplace la variante)
#                    TEZCATL_BASE_URL   miroir des artefacts (défaut : la release GitHub)
set -eu

REPO="${TEZCATL_REPO:-bornholm/tezcatl}"
VARIANT="${TEZCATL_VARIANT:-client}"
VERSION="${TEZCATL_VERSION:-}"
PACKAGES="${TEZCATL_PACKAGES:-}"
FORCE="${TEZCATL_FORCE:-0}"
DOWNLOAD_ONLY=0

usage() {
    cat <<'EOF'
Installe ou met à jour tezcatl depuis les GitHub Releases.

  curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sh -s -- --variant dokku

Variantes : client (défaut), server, docker, dokku, kubernetes.
Options : --variant <v>, --version <tag>, --force, --download-only.
Variables : TEZCATL_VARIANT, TEZCATL_VERSION, TEZCATL_FORCE, TEZCATL_REPO,
            TEZCATL_PACKAGES, TEZCATL_BASE_URL (détail en tête de script).
EOF
}

fail() {
    echo "erreur : $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
    --variant)
        [ $# -ge 2 ] || fail "--variant demande une valeur"
        VARIANT="$2"
        shift 2
        ;;
    --version)
        [ $# -ge 2 ] || fail "--version demande une valeur"
        VERSION="$2"
        shift 2
        ;;
    --force)
        FORCE=1
        shift
        ;;
    --download-only)
        DOWNLOAD_ONLY=1
        shift
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        fail "option inconnue : $1 (voir --help)"
        ;;
    esac
done

# --- Téléchargeur -----------------------------------------------------------

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL -o "$2" "$1"; }
    resolve_latest() {
        curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest"
    }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
    resolve_latest() {
        wget -q -O /dev/null -S "https://github.com/$REPO/releases/latest" 2>&1 |
            sed -n 's/^ *Location: \(.*\)/\1/p' | tail -1
    }
else
    fail "curl ou wget est requis"
fi

# --- Détection plateforme ---------------------------------------------------

if command -v dpkg >/dev/null 2>&1; then
    FORMAT=deb
    case "$(dpkg --print-architecture)" in
    amd64) ARCH=amd64 ;;
    arm64) ARCH=arm64 ;;
    armhf | armel) ARCH=armv6 ;;
    i386) ARCH=386 ;;
    *) fail "architecture Debian non couverte : $(dpkg --print-architecture)" ;;
    esac
elif command -v pacman >/dev/null 2>&1; then
    FORMAT=archlinux
    case "$(uname -m)" in
    x86_64) ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    armv6l | armv7l) ARCH=armv6 ;;
    i686) ARCH=386 ;;
    *) fail "architecture non couverte : $(uname -m)" ;;
    esac
else
    fail "ni dpkg ni pacman : seuls Debian/Ubuntu et Arch/Manjaro sont couverts"
fi

SUDO=""
if [ "$(id -u)" != 0 ] && [ "$DOWNLOAD_ONLY" = 0 ]; then
    command -v sudo >/dev/null 2>&1 || fail "lancez en root (ou installez sudo)"
    SUDO="sudo"
fi

# --- Paquets de la variante -------------------------------------------------

# Les méta-paquets (tezcatl-server, tezcatl-dokku) sont en architecture
# "all" ; tezcatl-dokku n'existe qu'en deb, comme Dokku.
if [ -z "$PACKAGES" ]; then
    case "$VARIANT" in
    client)
        PACKAGES="tezcatl"
        ;;
    server)
        PACKAGES="tezcatl tezcatl-server"
        ;;
    docker)
        PACKAGES="tezcatl tezcatl-server tezcatl-plugin-host"
        ;;
    dokku)
        [ "$FORMAT" = deb ] || fail "la variante dokku n'existe qu'en deb (comme Dokku)"
        PACKAGES="tezcatl tezcatl-server tezcatl-plugin-host tezcatl-dokku"
        ;;
    kubernetes)
        PACKAGES="tezcatl tezcatl-server tezcatl-plugin-kubernetes"
        ;;
    *)
        fail "variante inconnue : $VARIANT (client, server, docker, dokku, kubernetes)"
        ;;
    esac
fi

# --- Résolution de la version -----------------------------------------------

if [ -z "$VERSION" ]; then
    VERSION="$(resolve_latest || true)"
    VERSION="${VERSION##*/}"
    case "$VERSION" in
    v[0-9]*) ;;
    *) fail "impossible de résoudre la dernière release de github.com/$REPO" ;;
    esac
fi

# Tag v0.7.0 -> version 0.7.0 dans les noms de fichiers.
BARE_VERSION="${VERSION#v}"
BASE_URL="${TEZCATL_BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"

# --- Déjà à jour ? ------------------------------------------------------------

installed_version() {
    if [ "$FORMAT" = deb ]; then
        dpkg-query -W -f '${Version}' tezcatl 2>/dev/null || true
    else
        pacman -Q tezcatl 2>/dev/null | awk '{print $2}' || true
    fi
}

if [ "$FORCE" = 0 ] && [ "$DOWNLOAD_ONLY" = 0 ]; then
    # dpkg stocke les pré-versions avec un tilde (0.7.0~next).
    INSTALLED="$(installed_version | tr '~' '-')"
    case "$INSTALLED" in
    "$BARE_VERSION" | "$BARE_VERSION"-*)
        echo "tezcatl $INSTALLED est déjà installé (--force pour réinstaller)"
        exit 0
        ;;
    esac
fi

# --- Téléchargement et vérification -----------------------------------------

package_file() {
    arch="$ARCH"
    case "$1" in
    tezcatl-server | tezcatl-dokku) arch=all ;;
    esac

    if [ "$FORMAT" = deb ]; then
        echo "${1}_${BARE_VERSION}_linux_${arch}.deb"
    else
        echo "${1}_${BARE_VERSION}_linux_${arch}.pkg.tar.zst"
    fi
}

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT INT TERM

echo "tezcatl $VERSION ($FORMAT/$ARCH), variante $VARIANT : $PACKAGES"

fetch "$BASE_URL/checksums.txt" "$WORKDIR/checksums.txt" ||
    fail "checksums.txt introuvable sous $BASE_URL"

FILES=""
for package in $PACKAGES; do
    file="$(package_file "$package")"
    echo "  téléchargement de $file"
    fetch "$BASE_URL/$file" "$WORKDIR/$file" || fail "$file introuvable sous $BASE_URL"
    FILES="$FILES $file"
done

for file in $FILES; do
    grep -q "  $file\$" "$WORKDIR/checksums.txt" || fail "$file absent de checksums.txt"
done

(
    cd "$WORKDIR"
    for file in $FILES; do
        grep "  $file\$" checksums.txt
    done | sha256sum -c - >/dev/null
) || fail "vérification des sommes de contrôle échouée"
echo "  sommes de contrôle vérifiées"

if [ "$DOWNLOAD_ONLY" = 1 ]; then
    KEEP="$(pwd)"
    for file in $FILES; do
        cp "$WORKDIR/$file" "$KEEP/"
    done
    cp "$WORKDIR/checksums.txt" "$KEEP/"
    trap - EXIT INT TERM
    rm -rf "$WORKDIR"
    echo "paquets vérifiés déposés dans $KEEP (rien n'a été installé)"
    exit 0
fi

# --- Installation -------------------------------------------------------------

set --
for file in $FILES; do
    set -- "$@" "$WORKDIR/$file"
done

if [ "$FORMAT" = deb ]; then
    # apt-get résout les dépendances externes (dokku), dpkg celles du lot.
    if command -v apt-get >/dev/null 2>&1; then
        $SUDO apt-get install -y --allow-downgrades "$@"
    else
        $SUDO dpkg -i "$@"
    fi
else
    $SUDO pacman -U --noconfirm "$@"
fi

echo ""
echo "tezcatl $VERSION installé."
case "$VARIANT" in
server | docker | dokku | kubernetes)
    echo "  systemctl enable --now tezcatl-server        # serveur local (socket unix:///run/tezcatl/tezcatl.sock)"
    ;;
esac
case "$VARIANT" in
docker)
    echo "  activer plugins.sources.host dans /etc/tezcatl/server.yaml (métriques hôte + conteneurs)"
    ;;
dokku)
    echo "  systemctl enable --now tezcatl-metrics       # métriques hôte + conteneurs"
    echo "  systemctl enable --now tezcatl-ingest@<app>  # logs, par application Dokku"
    ;;
kubernetes)
    echo "  configurer plugins.sources.kubernetes dans /etc/tezcatl/server.yaml (kubeconfig)"
    echo "  ou : tezcatl ingest source kubernetes --plugin-config '{...}'"
    ;;
esac
echo "  tezcatl top                                   # inspecter ce qui est appris"
