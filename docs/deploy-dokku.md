# Déployer tezcatl sur un serveur Dokku (Ubuntu)

Cette procédure installe le serveur tezcatl sur l'hôte Dokku, branche les
logs des applications, signale les déploiements comme changements et met
en place la boucle de feedback. Les paquets sont téléchargés depuis les
[releases GitHub](https://github.com/bornholm/tezcatl/releases).

## 1. Installation des paquets

Quatre paquets Debian sont publiés à chaque release :

- **`tezcatl`**, le binaire (`/usr/bin/tezcatl`) : ingestion, mode
  standalone, commandes de feedback ;
- **`tezcatl-server`**, l'intégration système du serveur : unité
  systemd, configuration `/etc/tezcatl`, utilisateur dédié ;
- **`tezcatl-plugin-host`**, le plugin de métriques hôte et conteneurs
  Docker (requis par l'unité `tezcatl-metrics`) ;
- **`tezcatl-dokku`**, l'intégration Dokku : unité d'ingestion par
  application, collecteur de métriques et hook de déploiement.

Le plus simple est le script d'installation (variante `dokku`), qui
télécharge la dernière release, vérifie les sommes de contrôle et
installe les quatre paquets. Le relancer plus tard met à jour :

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sudo sh -s -- --variant dokku

sudo systemctl enable --now tezcatl-server
```

Ou manuellement :

```bash
VERSION=0.7.0  # voir la dernière release

for pkg in "tezcatl_${VERSION}_linux_amd64" "tezcatl-server_${VERSION}_linux_all" \
           "tezcatl-plugin-host_${VERSION}_linux_amd64" "tezcatl-dokku_${VERSION}_linux_all"; do
  curl -fsSLO "https://github.com/bornholm/tezcatl/releases/download/v${VERSION}/${pkg}.deb"
done

# apt résout les dépendances tezcatl-server/tezcatl-dokku → tezcatl
sudo apt install ./tezcatl*.deb

sudo systemctl enable --now tezcatl-server
```

La configuration installée démarre telle quelle :

- socket unix `/run/tezcatl/tezcatl.sock` et TCP `127.0.0.1:4242` ;
- état appris (templates, baselines) dans `/var/lib/tezcatl/state`,
  préservé aux redémarrages et aux mises à jour ;
- événements émis en JSON dans le journal systemd :

```bash
journalctl -u tezcatl-server -o cat -f | jq .
```

`/etc/tezcatl/server.yaml` est marqué `noreplace` : vos modifications
survivent aux mises à jour du paquet.

## 2. Ingestion des logs des applications

Le paquet `tezcatl-dokku` installe l'unité *template*
`tezcatl-ingest@.service` : une instance par application, qui pipe
`dokku logs -t` vers l'ingestion. `Restart=always` réattache après
chaque redéploiement (le pipe se ferme avec l'ancien conteneur, systemd
relance).

```bash
sudo systemctl enable --now tezcatl-ingest@mon-app
sudo systemctl enable --now tezcatl-ingest@autre-app
```

La cible et l'environnement se règlent dans `/etc/tezcatl/ingest.env`
(préservé aux mises à jour), utile pour envoyer vers un serveur
distant :

```bash
# /etc/tezcatl/ingest.env
TEZCATL_TARGET=tls://tezcatl.example.net:4243
TEZCATL_ENVIRONMENT=production
```

Notes :

- `dokku logs` préfixe chaque ligne d'un timestamp RFC3339, que
  tezcatl extrait ; si l'application logge en JSON, il en tire aussi le
  message et le niveau ;
- au redémarrage d'une unité, `dokku logs -t` peut rejouer quelques
  lignes récentes : léger double comptage sans conséquence.

## 3. Signaler les déploiements

`tezcatl-dokku` installe aussi un plugin Dokku (activé automatiquement à
l'installation) dont le trigger `post-deploy` déclare chaque déploiement
comme *changement* : les anomalies qui suivent un `git push dokku`
sortiront avec le déploiement attaché (`related_changes`, offset en
secondes). Le hook utilise la même cible que les unités d'ingestion et
ne peut jamais faire échouer un déploiement.

```bash
dokku plugin:list   # doit lister « tezcatl »
```

## 4. Exploitation

**Apprentissage.** Le premier quart d'heure par application établit la
base (`learning_period: 15m`) ; la saisonnalité horaire (crons, trafic
quotidien) s'affine sur 2 à 3 jours.

**Boucle de feedback.** Inspecter ce qui a été appris et marquer le
bruit, sans redémarrer. Les marquages sont persistés avec l'état :

```bash
tezcatl templates --target unix:///run/tezcatl/tezcatl.sock

tezcatl mark --target unix:///run/tezcatl/tezcatl.sock \
  --template "connection reset by peer" --as ignore

tezcatl mark --target unix:///run/tezcatl/tezcatl.sock \
  --template "database connection timeout after <NUM>s" --as symptomatic
```

**Notifications.** Pour être prévenu plutôt que de lire le journal,
activer le sink webhook dans `/etc/tezcatl/server.yaml` (un POST JSON
par événement, vers n'importe quel endpoint, et un petit relais vers
ntfy ou Gotify convient), le secret dans `/etc/tezcatl/server.env` :

```yaml
sinks:
  webhook:
    enabled: true
    url: https://exemple.net/hooks/tezcatl
    headers:
      Authorization: Bearer ${TEZCATL_WEBHOOK_TOKEN}
```

```bash
sudo systemctl restart tezcatl-server
```

**Métriques.** Le paquet `tezcatl-dokku` installe l'unité
`tezcatl-metrics.service`, qui exécute le plugin source « host »
(paquet `tezcatl-plugin-host`, installé automatiquement par dépendance) :
il échantillonne l'hôte (CPU, mémoire, charge, disque) et les conteneurs
Docker (CPU et mémoire par conteneur, nombre de conteneurs par
application) puis pousse le tout vers la même cible que les logs. L'identité des conteneurs vient du
label Dokku `com.dokku.app-name` : les métriques d'une application se
corrèlent avec ses logs.

```bash
sudo systemctl enable --now tezcatl-metrics
```

Comme les unités d'ingestion, la cible se règle dans
`/etc/tezcatl/ingest.env`, donc le collecteur fonctionne aussi vers un
serveur distant. Ne pas activer en double : si vous utilisez cette
unité, laissez le plugin `host` désactivé dans `plugins.sources` de
`/etc/tezcatl/server.yaml` (et réciproquement). Si un Prometheus existe
par ailleurs, le plugin `prometheus` (paquet `tezcatl-plugin-prometheus`,
`plugins.sources.prometheus`) reste disponible côté serveur.

## 5. Mise à jour

```bash
VERSION=0.2.0
for pkg in "tezcatl_${VERSION}_linux_amd64" "tezcatl-server_${VERSION}_linux_all" "tezcatl-dokku_${VERSION}_linux_all"; do
  curl -fsSLO "https://github.com/bornholm/tezcatl/releases/download/v${VERSION}/${pkg}.deb"
done
sudo apt install ./tezcatl_*.deb ./tezcatl-server_*.deb ./tezcatl-dokku_*.deb
sudo systemctl restart tezcatl-server
```

L'état appris (`/var/lib/tezcatl/state`) et la configuration sont
conservés : pas de réapprentissage après mise à jour. La désinstallation
(`apt remove`) les conserve aussi, volontairement.

## Dépannage

| Symptôme | Piste |
|---|---|
| Aucun événement | Normal pendant l'apprentissage ; vérifier l'arrivée des logs avec les compteurs (`journalctl -u tezcatl-server \| grep "pipeline stats"`) |
| `tezcatl-ingest@app` redémarre en boucle | `dokku logs app -t` fonctionne-t-il à la main ? L'app a-t-elle des conteneurs actifs ? |
| Trop de bruit | `tezcatl templates` puis `tezcatl mark … --as ignore` ; allonger `learning_period` |
| Événement sans `related_changes` après un deploy | Vérifier le hook : `dokku plugin:list`, et l'exécuter à la main |
