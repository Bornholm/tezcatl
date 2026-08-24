# Tezcatl

> Tezcatl — « miroir », dérivé de Tezcatlipoca, le « miroir fumant »

Plateforme Go d'observabilité intelligente et multimodale : ingestion de logs et de métriques, découverte automatique de templates (port Go compatible Drain3, validé par golden tests contre l'implémentation Python), détection d'anomalies explicables, corrélation temporelle et production d'événements contextualisés exploitables par un opérateur ou un agent LLM.

Voir [PLAN.md](./PLAN.md) pour la vision complète et [docs/ROADMAP.md](./docs/ROADMAP.md) pour les décisions techniques et l'état d'avancement.

## Usage

Un binaire unique, trois modes :

```bash
# Traitement local autonome : stdin → détection → événements JSONL sur stdout
tail -F application.log |
  tezcatl standalone logs \
    --config misc/config/standalone.yaml \
    --source payment-api

# Serveur centralisé (gRPC unix/TCP)
tezcatl server --config misc/config/server.yaml

# Client d'ingestion distant
tail -F application.log |
  tezcatl ingest logs \
    --target unix:///run/tezcatl.sock \
    --source payment-api

# Métriques (format texte Prometheus), en standalone ou vers un serveur
tezcatl standalone metrics --source payment-api < metrics.txt
tezcatl ingest metrics --target tcp://host:4242 --source payment-api < metrics.txt
```

Exemple d'événement produit (une ligne JSON par événement) :

```json
{
  "kind": "anomaly.correlated",
  "source": "payment-api",
  "severity": "critical",
  "confidence": 0.98,
  "summary": "new log template after learning period: FATAL pool exhausted (+1 correlated signals)",
  "signals": [
    {"kind": "log.new_template", "score": 0.8, "attributes": {"template": "FATAL pool exhausted"}},
    {"kind": "metric.threshold", "score": 0.9, "attributes": {"metric": "pool_usage_percent", "value": "97"}}
  ],
  "context": {"before": ["…"], "after": ["…"]},
  "attributes": {"multimodal": "true", "signal_count": "2"}
}
```

L'état appris (templates Drain3, baselines des détecteurs) est persisté dans `state.dir` et restauré au redémarrage : un template déjà connu ne redéclenche pas d'anomalie.

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
