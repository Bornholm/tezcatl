# Tezcatl

> Tezcatl, « miroir », de Tezcatlipoca, le « miroir fumant »

Tezcatl lit vos logs et vos métriques, apprend ce qui est normal, et signale ce qui ne l'est pas.

Il regroupe les lignes de log en templates avec un port Go de Drain3, vérifié par golden tests contre l'implémentation Python. Il compare chaque métrique à la baseline qu'il a apprise pour elle. Quand plusieurs signaux tombent dans la même fenêtre de temps, il les rassemble en un seul événement qui cite ses preuves, et y attache les déploiements récents. Chaque événement sort en JSON, lisible par un opérateur comme par un agent LLM.

## Usage

Un binaire, trois modes.

```bash
# Traitement local autonome : stdin, détection, événements JSONL sur stdout
journalctl -o cat -fu payment-api.service |
  tezcatl standalone logs \
    --config misc/config/standalone.yaml \
    --service payment-api --environment production

# Rejouer un incident passé avec sa chronologie exacte : les fenêtres de
# corrélation expirent sur le temps des événements, pas sur l'horloge
cat incident.log |
  tezcatl standalone logs --service checkout --environment prod \
    --replay --changes-from deployments.jsonl

# Serveur centralisé (gRPC unix/TCP)
tezcatl server --config misc/config/server.yaml

# Clients d'ingestion distants
docker logs -f payment-api 2>&1 |
  tezcatl ingest logs \
    --target unix:///run/tezcatl/tezcatl.sock \
    --service payment-api --environment production

tezcatl ingest metrics --target tcp://host:4242 --service payment-api < metrics.txt

# Signaler un déploiement, pour le corréler aux anomalies qui suivent
tezcatl ingest change --target tcp://host:4242 \
  --service checkout --environment prod \
  --type deployment --change-version checkout:v1.8.2

# Relire les événements passés (journal local du serveur, JSONL)
tezcatl events --since 24h | jq .summary

# Assembler les anomalies d'une période en briefings d'incident :
# déclencheur, propagation, preuves agrégées, changements corrélés
tezcatl incidents --since 24h
tezcatl incidents --since 24h --format json   # pour un agent LLM

# Boucle de feedback : inspecter ce qui a été appris, marquer le bruit.
# --target vaut unix:///run/tezcatl/tezcatl.sock par défaut, le socket
# du paquet, et lit aussi $TEZCATL_TARGET
tezcatl templates
tezcatl metrics
tezcatl mark --template "connection reset by peer" --as ignore
tezcatl mark --metric "docker.memory.used_percent" --as ignore  # ou une clé de série, ou un glob

# Ou en interactif : TUI façon k9s. Trois onglets, événements en direct,
# marquage au clavier, filtre, baselines apprises
tezcatl top
```

Les marquages `normal`, `ignore` et `symptomatic` prennent effet tout de suite et sont persistés avec l'état. La détection apprend une baseline par heure de la journée (`seasonality: hourly`), donc un cron nocturne ou un pic de trafic quotidien ne déclenche rien, alors que le même pic à 3 h du matin est signalé. Le transport accepte `unix://`, `tcp://` et `tls://`, avec certificat côté serveur et `--tls-ca` côté client.

Tezcatl parse les logs JSON tout seul : il extrait le message, le niveau et le timestamp, puis fait porter la découverte de templates sur le message plutôt que sur l'enveloppe. Il reconnaît des formes, pas des produits, et cherche les noms de clés que les journaliseurs JSON ont en commun. Un flux qui nomme les siennes autrement, comme `journalctl -o json`, se déclare en trois lignes dans `logs.parsing` (voir `standalone.yaml`). Mieux encore, une source peut remplir elle-même le message et le niveau : le serveur les prend tels quels, et la connaissance du format reste dans le plugin qui lit ce format.

Voici un événement produit, une ligne JSON par événement :

```json
{
  "kind": "anomaly.correlated",
  "source": "prod/checkout",
  "service": "checkout",
  "environment": "prod",
  "timestamp": "2026-08-24T14:05:00Z",
  "severity": "critical",
  "confidence": 0.92,
  "summary": "new log template after learning period: database connection timeout after 30s (+1 correlated signals)",
  "signals": [
    {"kind": "log.new_template", "score": 0.8, "attributes": {"template": "database connection timeout after <NUM>s"}},
    {"kind": "metric.threshold", "score": 0.9, "attributes": {"metric": "pool_usage_percent", "value": "97"}}
  ],
  "related_changes": [
    {"change": {"type": "deployment", "version": "checkout:v1.8.2"}, "offset_seconds": -180}
  ],
  "context": {"before": ["…"], "after": ["…"]},
  "attributes": {"multimodal": "true", "signal_count": "2"}
}
```

Le `related_changes` dit qu'un déploiement a eu lieu 180 secondes avant l'anomalie. C'est une corrélation, pas une preuve de causalité, et Tezcatl s'arrête là volontairement.

Tezcatl persiste ce qu'il apprend, templates Drain3 et baselines, dans `state.dir` et le recharge au démarrage. Un redémarrage ne relance donc pas une salve de fausses alertes sur des templates déjà connus.

`tezcatl top` ouvre trois onglets sur un serveur en marche. L'onglet **événements** suit le flux en direct et rejoue les 500 derniers, avec le détail des signaux déclencheurs et des changements corrélés sous `entrée`. L'onglet **templates** marque au clavier ce qui est du bruit, sans recopier le template. L'onglet **metrics** trie les séries par écart à leur baseline, ce qui sert à régler les planchers `min_deltas`. L'historique des événements vit en mémoire dans le serveur, donc il repart à zéro à chaque redémarrage.

## Déploiement

Chaque release publie des paquets Debian et Arch (`tezcatl`, `tezcatl-server`, les plugins, `tezcatl-dokku`) et une image de conteneur, `ghcr.io/bornholm/tezcatl`. Le script [install.sh](./install.sh) installe le jeu de paquets d'une variante et vérifie les sommes de contrôle avant d'installer quoi que ce soit :

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sh -s -- --variant dokku
```

Cinq variantes. `client` n'installe que la CLI et suffit pour envoyer des logs vers un serveur distant. `server` ajoute l'unité systemd, `/etc/tezcatl` et l'utilisateur dédié. `docker` ajoute le plugin de métriques hôte et conteneurs. `dokku` ajoute l'ingestion des logs par application et le hook de déploiement. `kubernetes` ajoute le plugin qui surveille un cluster depuis cette machine.

Relancer le même script met à jour vers la dernière release, et ne fait rien si la version est déjà installée. Utilisez `--version vX.Y.Z` pour épingler une version, `--download-only` pour récupérer les paquets vérifiés sans les installer.

Trois guides pas à pas :

- [Serveur Dokku/Ubuntu](./docs/deploy-dokku.md) couvre les paquets, l'ingestion des logs par application et le hook de déploiement.
- [Kubernetes](./docs/deploy-kubernetes.md) couvre le serveur en Deployment, les métriques via Prometheus, les logs de pods, les events du cluster et les rollouts vus par le plugin `tezcatl-source-kubernetes`.
- [Tutoriel kubectl](./docs/tutorial-kubectl.md) montre comment superviser un cluster depuis l'extérieur sans rien y installer : analyse en direct, rejeu d'incident, events, Prometheus en port-forward.

## Configuration

Tout se configure en YAML. La validation est stricte au démarrage, une clé inconnue est une erreur, et `${VAR}` permet de sortir les secrets du fichier. Vous réglez les listeners, les buffers, le nombre de workers, les masques Drain3, la période d'apprentissage, les détecteurs, la fenêtre de corrélation, la persistance et les sinks (stdout JSONL, PostgreSQL, webhook). Les profils d'exemple sont dans [misc/config](./misc/config), et `standalone.yaml` documente toutes les options avec leurs valeurs par défaut.

## Développement

```bash
make build      # binaire dans bin/tezcatl
make test       # go test -race ./...
make bench      # benchmarks (miner, pipeline complet)
make generate   # régénère le code protobuf (protoc requis)
make tidy       # go mod tidy
```

Pour régénérer les golden tests de compatibilité Drain3 :

```bash
pip install drain3
python3 misc/drain3-golden/generate.py internal/core/drain/testdata/corpus.log \
  > internal/core/drain/testdata/golden.json
python3 misc/drain3-golden/generate.py internal/core/drain/testdata/corpus.log 4 \
  > internal/core/drain/testdata/golden_lru.json
```

La CI les régénère avec le drain3 Python non épinglé, puis échoue à la moindre dérive avant de les rejouer avec le port Go.
