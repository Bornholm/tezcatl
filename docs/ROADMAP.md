# Roadmap d'implémentation du MVP

Ce document décline les phases de [PLAN.md](../PLAN.md) en tâches concrètes et trace les décisions techniques. Il est mis à jour au fil de l'implémentation.

## Décisions techniques

| Sujet | Décision |
|---|---|
| Module | `github.com/bornholm/tezcatl` |
| Go | ≥ 1.26.0 |
| Layout | `cmd/tezcatl`, `internal/core/{model,port,engine,processor,drain,detect,correlate,sink,state,window}`, `internal/adapter/<tech>`, `internal/{config,setup,command}` |
| Erreurs | `github.com/pkg/errors` (`WithStack`/`Wrap`) |
| Logs internes | `log/slog` structuré, sur stderr (stdout réservé aux événements) |
| CLI | `github.com/urfave/cli/v2` |
| Concurrence | canaux bornés + `errgroup` ; workers partitionnés par hash FNV de `Observation.PartitionKey()` = `Source` — toutes les modalités d'une source sont traitées séquentiellement par le même worker, ce qui donne au corrélateur une vue cohérente |
| Backpressure | bout en bout : sink lent → canal events plein → workers bloqués → canal observations plein → ingestion ralentie (flow control HTTP/2 côté gRPC) |
| Panne d'un sink | file bornée + retries en arrière-plan (`sink.Resilient`) ; drop compté et loggé, jamais bloquant |
| Panne d'un processor sur une observation | observation abandonnée, pipeline maintenu |
| Snapshots Drain3 | format JSON gzip propre (l'arbre est reconstruit depuis les clusters) ; compatibilité **comportementale** avec drain3, pas compatibilité du format jsonpickle |
| Golden tests | fixtures générées par `misc/drain3-golden/generate.py` avec le drain3 Python officiel (`pip install drain3`), y compris l'éviction LRU |
| Masques Drain3 | syntaxe RE2 (pas de lookaround) — utiliser `\b` |
| PostgreSQL | doit être joignable au démarrage (création du schéma) ; les coupures en cours d'exécution sont absorbées par le sink résilient |

## État des phases

### Phase 1 — Modèle de domaine et moteur commun ✅

`Observation`/`Event`/`Signal` (`core/model`), ports `Ingester`/`Processor`/`EventSink`/`StateStore`/`Flusher`/`Snapshotter` (`core/port`), moteur borné avec arrêt propre en cascade et ticks de flush (`core/engine`).

### Phase 2 — CLI et transport client/serveur ✅

Contrat protobuf `api/proto/tezcatl/v1` (client-streaming `IngestService.StreamObservations`), serveur multi-listeners unix/TCP (`adapter/grpc`), client avec reconnexion à backoff exponentiel, commandes `tezcatl server` et `tezcatl ingest logs|metrics`. `make generate` régénère le code (protoc + plugins installés dans `tools/bin`).

### Phase 3 — Mode standalone ✅

`tezcatl standalone logs|metrics` compose le même `setup.Runtime` que le serveur (seuls les ingesters diffèrent). `--metrics-from` permet l'ingestion multimodale locale via fichier ou FIFO.

### Phase 4 — Pipeline multimodal ✅

Processor de normalisation (validation, troncature, timestamps), parseur Prometheus text format (`format/prometheus`), ring buffer de contexte (`core/window`). Limitation connue : l'ingester stdin bloque sur `Read`, la fermeture du pipe reste le signal d'arrêt principal.

### Phase 5 — Port Go compatible Drain3 ✅

`core/drain` : arbre à profondeur fixe, similarité et wildcards, masquage RE2, LRU avec nettoyage paresseux de l'arbre, modes apprentissage (`AddLogMessage`) et inférence (`Match` never/fallback/always), extraction de paramètres, partitions indépendantes, snapshots gzip. Golden tests ligne à ligne contre le drain3 Python (corpus 168 lignes, avec et sans `max_clusters`).

### Phase 6 — Détection d'anomalies ✅

- Logs (`detect.LogDetector`) : nouveau template après apprentissage, template rare, pic de fréquence (buckets + baseline EWMA), disparition de template attendu (scan paresseux), marquages `normal`/`ignore`/`symptomatic`.
- Métriques (`detect.MetricDetector`) : Z-score sur moyenne/variance EWMA, seuils statiques configurables, dérive de tendance (EWMA rapide vs lente).
- Tous les signaux portent score, résumé et attributs explicatifs.

### Phase 7 — Corrélation multimodale ✅

`correlate.Correlator` : fenêtre par source, déduplication des signaux (occurrences comptées), confiance combinée `1-Π(1-score)`, sévérité dérivée, contexte avant/après attaché, attribut `multimodal`. Flush périodique via le ticker du moteur + flush final au drain.

### Phase 8 — Sinks et persistance ✅

- Sinks : stdout JSONL (défaut) ; PostgreSQL (`adapter/postgres`, table `tezcatl_events` avec payload JSONB) derrière `sink.Resilient` (file bornée, retries, drops comptés).
- État : `StateStore` fichiers avec écriture atomique (`adapter/fs`) ; `state.Manager` restaure au démarrage, sauvegarde périodiquement et au shutdown les snapshots du miner et des baselines des deux détecteurs.

### Phase 9 — Configuration et exploitation ✅

`internal/config` : YAML strict (clés inconnues rejetées), durées lisibles (`30s`), expansion `${VAR}` pour les secrets, validation au démarrage (y compris masques drain). Profils d'exemple dans `misc/config/{standalone,server}.yaml`. `internal/setup` compose le runtime identique serveur/standalone. Logs internes configurables (niveau, text/json).

### Phase 10 — Validation du MVP ✅

- unités : drain (golden inclus), détecteurs, corrélateur, parseur, ring, config, state, sink résilient ;
- intégration : moteur (flux, ordre par partition, annulation, backpressure, fuite de goroutines), gRPC client/serveur sur socket unix, runtime e2e (anomalie détectée, restauration sans faux positifs, corrélation multimodale) ;
- `go test -race ./...` vert ; benchmarks : miner ~190k msg/s single-thread, pipeline complet ~275k lignes/s (`make bench`).

Restes à faire (hors périmètre MVP) :

- [x] métriques de santé : compteurs internes du moteur (ingérées/traitées/rejetées/événements) loggés périodiquement (`logging.stats_interval`) et en résumé final
- [x] TLS sur les listeners : cibles `tls://host:port` servies avec `server.tls.{cert_file,key_file}`, clients avec `--tls-ca` (mTLS non couvert)
- [ ] `StateStore` PostgreSQL (aujourd'hui fichiers uniquement)
- [x] CI (`.github/workflows/ci.yml`) : `go vet` + `go test -race ./...`, et job `drain-golden` qui régénère les fixtures avec le drain3 Python (non épinglé, exprès) puis échoue à la moindre dérive avant de les rejouer avec le port Go

## Chantiers cas d'usage

Déclinaison de [USE_CASES.md](../USE_CASES.md) (dogfooding Dokku/systemd/Docker, puis Kubernetes). Le moteur est en place ; ces chantiers portent sur la couche d'entrée et la couche sémantique.

### C1 — Logs réels et rejeu d'incidents ✅

- [x] Identité canonique `service` + `environment` de première classe (`--service`/`--environment`, `Source = env/service` dérivé à la normalisation, champs protobuf dédiés)
- [x] Parsing structuré des logs (`processor.ParseLog`) : JSON (clés usuelles + journald `-o json` : MESSAGE/PRIORITY/__REALTIME_TIMESTAMP), niveaux normalisés, timestamps extraits (JSON string/epoch, RFC3339 en tête de ligne à la docker) — le mining porte sur le message extrait, plus sur l'enveloppe
- [x] Horloge de corrélation `wall|event` (`correlation.clock`) + flag `--replay` : les fenêtres expirent sur le watermark des timestamps d'observation, chronologie exacte au rejeu (les détecteurs étaient déjà en event-time)

Objectif de validation (à faire avec de vrais incidents) : rejouer trois incidents passés connus et vérifier que tezcatl rassemble automatiquement ce qu'un ingénieur cherche à la main. Le scénario-type de USE_CASES.md (déploiement 14:02 → anomalie SQL 14:05) est reproduit : événement critique horodaté 14:05:00 avec le déploiement en `related_changes` à −180 s.

### C2 — Métriques depuis l'API Prometheus ✅

- [x] `adapter/prometheus.Poller` : évaluation périodique de requêtes PromQL (`/api/v1/query`, vector et scalar), nom de métrique logique par requête, identité statique ou dérivée d'un label (`service_label`)
- [x] Activable en standalone comme en serveur (`metrics.prometheus` dans la config, composé par `setup.Runtime`)
- [x] Collecteurs intégrés sans infrastructure : `metrics.system` (CPU/mémoire/charge/disque via `/proc` et statfs, Linux) et `metrics.docker` (CPU/mémoire par conteneur + conteneurs par service via l'API Engine sur socket unix ; identité par label, `com.dokku.app-name` par défaut)
- [x] Collecte côté client : `tezcatl ingest host` exécute les deux collecteurs et pousse vers un serveur (local ou distant) — livré en unité systemd `tezcatl-metrics.service` dans le paquet tezcatl-dokku
- Note : un poller/collecteur actif rend le processus persistant (l'ingestion ne se termine plus à la fermeture de stdin) — arrêt par SIGINT/SIGTERM

### C3 — Modalité « changement » ✅

- [x] `ModalityChange` + `ChangeRecord` (type, version, résumé) dans le modèle et le protobuf
- [x] `tezcatl ingest change` (one-shot) et `--changes-from` (JSONL `{"time","type","version","summary"}`) en standalone
- [x] Le corrélateur attache les changements récents aux événements (`related_changes` avec `offset_seconds`, horizon `correlation.change_horizon`) — documenté comme **corrélation**, pas preuve
- [ ] Watcher de déploiements Kubernetes/Dokku alimentant automatiquement cette modalité (voir C4)

### C4 — Corrélation multi-sources (Kubernetes)

- [ ] Second niveau de regroupement (workload, namespace) au-dessus des sources
- [ ] Événements multi-services (`affected_services`), corrélation entre pods d'un même workload
- [ ] Ingestion des métadonnées Kubernetes (déploiements, redémarrages) — s'appuie sur C3

### C5 — Artefact d'incident enrichi

- [ ] Titre, chronologie ordonnée, distinction trigger/evidence/related_changes/hypothèses
- [ ] Résumé destiné à un humain ; export optionnel vers un LLM

### C6 — Sorties complémentaires 🟡

- [x] Sink webhook (`sinks.webhook` : POST JSON par événement, headers configurables pour les secrets, derrière le sink résilient)
- [ ] Sink SQLite (alternative légère à PostgreSQL pour le mode personnel)

## Post-MVP réalisé

- **Boucle de feedback dynamique** ✅ : marquages de templates modifiables au runtime, persistés avec l'état du détecteur (les marquages de la config restent la base, les marquages runtime les surchargent). `AdminService` gRPC servi sur les mêmes listeners que l'ingestion ; CLI `tezcatl templates` (liste partition/id/taille/marquage) et `tezcatl mark --template … --as normal|ignore|symptomatic` (`--clear` pour retirer), en live (`--target`) ou hors-ligne sur un état persisté (`--state-dir`).
- **Saisonnalité horaire** ✅ (`logs.detection.seasonality: hourly`, défaut) : baselines de fréquence par heure de la journée (un pic quotidien à 9 h n'est plus un faux positif après une nuit calme, le même burst à 3 h l'est) ; un template jamais vu à une heure connue-active n'est pas attendu (cron nocturne non signalé « missing » à midi, seuil `seasonal_min_observations`).
- **Flux de changements** ✅ : `tezcatl ingest changes` (JSONL sur stdin, pour hooks CI / `docker events` transformés) en plus du one-shot `ingest change` et de `--changes-from`.

## Après (non réalisé, dans l'ordre recommandé)

1. Validation sur 2-3 incidents réels rejoués (`--replay --changes-from`) → calibrer C4/C5
2. C4 corrélation multi-sources, C5 artefact d'incident enrichi (trigger/evidence/hypothèses)
3. Watcher de déploiements Kubernetes/Dokku (alimente la modalité change automatiquement)
4. Export/stimulus LLM (le format d'événement actuel en est déjà proche)
5. Voir [PLAN.md](../PLAN.md) pour le reste : traces OTel, détection avancée (ruptures, multivarié), industrialisation (mTLS, multi-tenant, HA) — non pertinents à l'échelle actuelle.
