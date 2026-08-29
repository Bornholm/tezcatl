# Tezcatl

> Tezcatl — « miroir », dérivé de Tezcatlipoca, le « miroir fumant »

Plateforme Go d'observabilité intelligente et multimodale : ingestion de logs et de métriques, découverte automatique de templates (port Go compatible Drain3, validé par golden tests contre l'implémentation Python), détection d'anomalies explicables, corrélation temporelle et production d'événements contextualisés exploitables par un opérateur ou un agent LLM.

Voir [PLAN.md](./PLAN.md) pour la vision complète et [docs/ROADMAP.md](./docs/ROADMAP.md) pour les décisions techniques et l'état d'avancement.

## Usage

Un binaire unique, trois modes :

```bash
# Traitement local autonome : stdin → détection → événements JSONL sur stdout
journalctl -o json -fu payment-api.service |
  tezcatl standalone logs \
    --config misc/config/standalone.yaml \
    --service payment-api --environment production

# Rejouer un incident passé avec une chronologie exacte (fenêtres de
# corrélation sur le temps des événements) et corréler les déploiements
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

# Signaler un déploiement (corrélé aux anomalies qui suivent)
tezcatl ingest change --target tcp://host:4242 \
  --service checkout --environment prod \
  --type deployment --change-version checkout:v1.8.2

# Boucle de feedback : inspecter ce qui a été appris, marquer le bruit
# (--target vaut unix:///run/tezcatl/tezcatl.sock par défaut, le socket
# du paquet ; surchargeable aussi par $TEZCATL_TARGET)
tezcatl templates
tezcatl metrics
tezcatl mark --template "connection reset by peer" --as ignore

# Ou en interactif (TUI façon k9s : marquage au clavier, filtre, baselines)
tezcatl top
```

Les marquages (`normal`, `ignore`, `symptomatic`) prennent effet immédiatement et sont persistés avec l'état. La détection apprend des baselines par heure de la journée (`seasonality: hourly`) : un cron nocturne ou un pic de trafic quotidien n'est pas une anomalie. Le transport accepte `unix://`, `tcp://` et `tls://` (certificat côté serveur, `--tls-ca` côté client).

Les logs JSON (dont `journalctl -o json`) sont parsés automatiquement : message, niveau et timestamp sont extraits, et la découverte de templates porte sur le message. Les métriques peuvent aussi être tirées de l'API Prometheus (plugin `tezcatl-source-prometheus`, requêtes PromQL périodiques via `plugins.sources.prometheus` dans la configuration).

Exemple d'événement produit (une ligne JSON par événement) :

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

L'état appris (templates Drain3, baselines des détecteurs) est persisté dans `state.dir` et restauré au redémarrage : un template déjà connu ne redéclenche pas d'anomalie.

## Déploiement

Des paquets Debian/Arch (`tezcatl`, `tezcatl-server`, `tezcatl-dokku`) et
une image de conteneur (`ghcr.io/bornholm/tezcatl`) sont publiés à chaque
release. Guides pas à pas :

- [Serveur Dokku/Ubuntu](./docs/deploy-dokku.md) — paquets, ingestion des
  logs par application, hook de déploiement ;
- [Kubernetes](./docs/deploy-kubernetes.md) — serveur en Deployment,
  métriques via Prometheus, logs de pods, events du cluster et
  déploiements/rollouts (modalité change) via le plugin
  `tezcatl-source-kubernetes`, changements enrichis depuis la CI ;
- [Tutoriel kubectl](./docs/tutorial-kubectl.md) — superviser un cluster
  depuis l'extérieur, sans rien y installer : analyse en direct, rejeu
  d'incident, events Kubernetes, Prometheus en port-forward.

## Configuration

Tout est configurable en YAML (validation stricte au démarrage, secrets via `${VAR}`) : listeners, buffers, workers, masques Drain3, période d'apprentissage, détecteurs, fenêtre de corrélation, persistance, sinks (stdout JSONL et/ou PostgreSQL). Profils d'exemple dans [misc/config](./misc/config).

## Développement

```bash
make build      # binaire dans bin/tezcatl
make test       # go test -race ./...
make bench      # benchmarks (miner, pipeline complet)
make generate   # régénère le code protobuf (protoc requis)
make tidy       # go mod tidy
```

Les golden tests de compatibilité Drain3 se régénèrent avec :

```bash
pip install drain3
python3 misc/drain3-golden/generate.py internal/core/drain/testdata/corpus.log \
  > internal/core/drain/testdata/golden.json
python3 misc/drain3-golden/generate.py internal/core/drain/testdata/corpus.log 4 \
  > internal/core/drain/testdata/golden_lru.json
```
