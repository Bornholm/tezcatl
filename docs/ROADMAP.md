# Roadmap d'implémentation du MVP

Ce document décline les phases de [PLAN.md](../PLAN.md) en tâches concrètes et trace les décisions techniques. Il est mis à jour au fil de l'implémentation.

## Décisions techniques

| Sujet | Décision |
|---|---|
| Module | `github.com/bornholm/tezcatl` |
| Go | ≥ 1.26.0 |
| Layout | `cmd/tezcatl`, `internal/core/{model,port,engine,processor}`, `internal/adapter/<tech>`, `internal/command` |
| Erreurs | `github.com/pkg/errors` (`WithStack`/`Wrap`) |
| Logs internes | `log/slog` structuré |
| CLI | `github.com/urfave/cli/v2` |
| Concurrence | canaux bornés + `errgroup` ; workers partitionnés par hash FNV de `Observation.PartitionKey()` — une partition est toujours traitée par le même worker, donc séquentiellement |
| Panne d'un sink | loggée, n'interrompt pas le pipeline (cf. Phase 8) |
| Panne d'un processor sur une observation | observation abandonnée, pipeline maintenu |

## État des phases

### Phase 1 — Modèle de domaine et moteur commun ✅

- [x] `internal/core/model` : `Observation` (multimodale : log, métrique, trace à venir), `Event`, `Signal`, `Context`
- [x] `internal/core/port` : `Ingester`, `Processor`, `EventSink`, `StateStore`
- [x] `internal/core/engine` : pipeline borné `ingesters → dispatch partitionné → workers → sinks`, arrêt propre en cascade (fermeture des canaux), test de flux et test d'ordre par partition sous `-race`
- [x] `internal/core/processor` : processor `debug` (transforme chaque observation en événement `debug.observation`) pour valider la chaîne de bout en bout — sera désactivé par défaut quand la détection existera
- [x] Tranche verticale : `stdin → Engine → stdout JSONL` via `tezcatl standalone logs`

### Phase 2 — CLI et transport client/serveur ⏳

- [ ] Définir le contrat protobuf (`api/proto/tezcatl/v1`) : `IngestService.StreamObservations` (client-streaming), messages `Observation`/`Ack`
- [ ] `internal/adapter/grpc/server` : serveur gRPC branché sur le canal d'ingestion de l'Engine, listeners Unix et TCP
- [ ] `internal/adapter/grpc/client` : client streaming avec reconnexion élémentaire (backoff) et arrêt propre
- [ ] Backpressure : le serveur ne lit le stream qu'à la vitesse d'absorption du canal borné
- [ ] Commandes `tezcatl server` et `tezcatl ingest logs --target unix:///…`

### Phase 3 — Mode standalone ⏳

- [x] Squelette `tezcatl standalone logs` (ingestion stdin → Engine → stdout)
- [ ] Aligner la composition sur la configuration YAML commune (mêmes profils que le serveur, hors réseau)
- [ ] `tezcatl standalone metrics` une fois l'ingestion de métriques disponible

### Phase 4 — Pipeline multimodal

- [ ] Processor de validation/normalisation (timestamps, source obligatoire, troncature)
- [ ] Ingestion de métriques (format ligne `name value [labels]` + scrape/remote-write plus tard)
- [ ] Fenêtres de contexte temporelles : ring buffer par partition conservant N observations avant/après un signal
- [ ] Limitation : l'ingester stdin bloque sur `Read` — la fermeture du pipe reste le signal d'arrêt principal ; à revisiter ici

### Phase 5 — Port Go compatible Drain3

- [ ] `internal/core/drain` : arbre à profondeur fixe, similarité, clusters, masquage configurable, LRU, modes apprentissage/inférence, extraction de paramètres
- [ ] Partitions indépendantes (env/service) — s'appuie sur le partitionnement du moteur
- [ ] Snapshot/restore via `port.StateStore` (JSON compressé, format compatible Drain3)
- [ ] Golden tests contre l'implémentation Python officielle (fixtures générées par script sous `misc/drain3-golden`)

### Phase 6 — Détection d'anomalies

- [ ] Logs : nouveaux templates post-apprentissage, templates rares, hausse de fréquence, disparition, marquage de clusters
- [ ] Métriques : moyenne/variance glissantes, Z-score, EWMA, quantiles, dérive, seuils
- [ ] Chaque détecteur produit des `Signal` explicables (score + résumé + attributs)

### Phase 7 — Corrélation multimodale

- [ ] Corrélateur fenêtré par source : regroupement, déduplication, score de confiance, attachement du contexte avant/après
- [ ] Production d'`Event` contextualisés uniques

### Phase 8 — Sinks et persistance

- [x] Sink stdout JSON Lines (défaut)
- [ ] Sink PostgreSQL (connecteur générique, file bornée + drop loggé si indisponible)
- [ ] `StateStore` fichier local (snapshots Drain3 + baselines), puis PostgreSQL

### Phase 9 — Configuration et exploitation

- [ ] `internal/config` : chargement YAML, validation stricte au démarrage, profils standalone/serveur, secrets via env
- [ ] Métriques de santé internes

### Phase 10 — Validation du MVP

- [x] Tests unitaires moteur sous `-race`
- [ ] Tests bout en bout standalone et client/serveur, restauration snapshot, backpressure, reconnexion, arrêt files non vides, indisponibilité PostgreSQL
- [ ] Benchmarks débit/mémoire (`make bench`)

## Ordre de travail recommandé

1. Phase 4 (validation + métriques + fenêtres de contexte) pour solidifier le socle multimodal
2. Phase 2 (gRPC) pour figer le contrat de transport
3. Phase 5 (Drain3) — le gros morceau, isolable dans `internal/core/drain`
4. Phases 6 → 7 → 8 (PostgreSQL) → 9 → 10
