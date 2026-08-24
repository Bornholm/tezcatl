# Tezcatl

> Tezcatl — « miroir », dérivé de Tezcatlipoca, le « miroir fumant »

Plateforme Go d'observabilité intelligente et multimodale : ingestion de logs et de métriques, découverte automatique de templates (port Go compatible Drain3), détection d'anomalies explicables, corrélation temporelle et production d'événements contextualisés exploitables par un opérateur ou un agent LLM.

Voir [PLAN.md](./PLAN.md) pour la vision complète et [docs/ROADMAP.md](./docs/ROADMAP.md) pour l'état d'avancement de l'implémentation.

## Usage

```bash
# Serveur centralisé
tezcatl server --config config.yaml

# Client d'ingestion distant
tail -F application.log |
  tezcatl ingest logs \
    --target unix:///run/tezcatl.sock \
    --source payment-api

# Traitement local autonome
tail -F application.log |
  tezcatl standalone logs \
    --source payment-api
```

## Développement

```bash
make build   # binaire dans bin/tezcatl
make test    # go test -race ./...
make tidy    # go mod tidy
```
