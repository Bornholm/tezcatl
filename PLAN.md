# Tezcatl - Résumé exécutif

> Tezcatl - Miroir, dérivé de Tezcatlipoca, le « miroir fumant »

## Vision et objectifs finaux

Le projet vise à créer une **plateforme Go d’observabilité intelligente et multimodale**, capable d’ingérer en continu des données provenant d’applications et d’infrastructures arbitraires :

- logs textuels ou structurés ;
- métriques numériques compatibles Prometheus/OpenMetrics ;
- ultérieurement, traces et événements externes.

La plateforme devra apprendre progressivement le fonctionnement normal de chaque système, détecter les comportements inhabituels, corréler les signaux provenant de plusieurs modalités et produire des **événements contextualisés exploitables par un opérateur ou un agent LLM**.

```text
Logs ─────┐
Métriques ├──► Normalisation ─► Apprentissage ─► Détection
Traces ───┘                                         │
                                                    ▼
                                      Corrélation multimodale
                                                    │
                                                    ▼
                                      Événements contextualisés
                                                    │
                                      ┌─────────────┴─────────────┐
                                      ▼                           ▼
                                  Opérateurs                   Agent LLM
```

L’objectif à terme n’est donc pas uniquement d’identifier une ligne de log inhabituelle, mais de produire un **stimulus ciblé**, contenant :

- le symptôme détecté ;
- son niveau de confiance et sa sévérité ;
- les logs contigus pertinents ;
- les métriques corrélées ;
- les templates concernés ;
- l’écart par rapport au comportement normal ;
- les exceptions ou règles métier définies dynamiquement.

---

## Principes d’architecture

Le produit sera développé sous la forme d’un **binaire Go unique**, proposant trois modes :

```bash
# Serveur centralisé
tezcatl server --config config.yaml

# Client d’ingestion distant
tail -F application.log |
  tezcatl ingest logs \
    --target unix:///run/tezcatl.sock \
    --source payment-api

# Traitement local autonome
tail -F application.log |
  tezcatl standalone logs \
    --config tezcatl.yaml \
    --source payment-api
```

Les modes serveur et standalone utiliseront exactement le même moteur :

```text
Mode distribué :
stdin ─► CLI ─► gRPC Unix/TCP ─► Engine ─► Events

Mode standalone :
stdin ─► CLI ────────────────► Engine ─► Events
```

Le standalone appellera directement le moteur, sans démarrer artificiellement un serveur gRPC local.

---

# Périmètre du MVP

Le MVP doit valider toute la chaîne fonctionnelle :

1. ingestion de logs par pipe sur `stdin` ;
2. ingestion de métriques ;
3. transport gRPC streaming sur socket Unix ou TCP ;
4. exécution locale en mode standalone ;
5. découverte automatique de templates avec un port Go compatible Drain3 ;
6. apprentissage d’un comportement normal élémentaire ;
7. détection d’anomalies logs et métriques ;
8. corrélation temporelle multimodale ;
9. génération d’événements contextualisés ;
10. émission sur `stdout` ou stockage PostgreSQL ;
11. configuration complète en YAML ;
12. persistance et restauration de l’état d’apprentissage.

---

# Phases d’implémentation du MVP

## Phase 1 — Modèle de domaine et moteur commun

Construire les abstractions stables :

- `Observation` pour représenter les entrées multimodales ;
- `Event` pour représenter les résultats ;
- `Engine` pour exécuter le pipeline ;
- `Processor` pour les étapes de traitement ;
- `Ingester` pour les sources ;
- `EventSink` pour les destinations ;
- `StateStore` pour l’état persistant.

L’objectif est de découpler le moteur des transports, du stockage et des formats d’entrée.

---

## Phase 2 — CLI et transport client/serveur

Implémenter :

- la lecture des logs depuis `stdin` ;
- le client gRPC en streaming ;
- le serveur gRPC ;
- les listeners Unix et TCP ;
- la gestion de la backpressure ;
- l’arrêt propre ;
- la reconnexion élémentaire du client.

À la fin de cette phase :

```text
stdin → client → gRPC → serveur → stdout
```

doit fonctionner de bout en bout.

---

## Phase 3 — Mode standalone

Composer le même moteur directement dans le CLI :

```text
stdin → ingestion locale → Engine → stdout/PostgreSQL
```

Le comportement fonctionnel et la configuration doivent être identiques à ceux du mode serveur, à l’exception des paramètres réseau.

---

## Phase 4 — Pipeline multimodal

Mettre en place :

- validation et normalisation ;
- routage logs/métriques ;
- canaux bornés ;
- workers partitionnés ;
- propagation des annulations ;
- fenêtres de contexte temporelles ;
- conservation des entrées précédant et suivant un signal.

Cette phase constitue le socle pour l’ajout futur de traces et d’autres modalités.

---

## Phase 5 — Port Go compatible Drain3

Implémenter les capacités essentielles de Drain3 :

- arbre DRAIN à profondeur fixe ;
- découverte automatique de templates ;
- masquage configurable des variables ;
- création et mise à jour des clusters ;
- modes apprentissage et inférence ;
- extraction des paramètres ;
- limitation du nombre de clusters avec éviction LRU ;
- partitions indépendantes par environnement et service ;
- snapshot, compression et restauration.

Chaque partition sera traitée séquentiellement, tandis que différentes partitions pourront être traitées parallèlement.

Le port devra être validé par des **golden tests** comparant ses résultats à ceux de l’implémentation Python officielle.

---

## Phase 6 — Détection d’anomalies

### Pour les logs

Produire des signaux à partir de :

- nouveaux templates après la période d’apprentissage ;
- templates rares ;
- hausse inhabituelle de fréquence ;
- disparition d’un template attendu ;
- changements rapides de templates ;
- clusters explicitement marqués comme normaux, ignorés ou symptomatiques.

### Pour les métriques

Déployer des détecteurs simples et interprétables :

- moyenne et variance glissantes ;
- Z-score ;
- EWMA ;
- quantiles ;
- dérive de tendance ;
- dépassement de seuil configurable.

L’objectif du MVP est la robustesse et l’explicabilité, pas la sophistication statistique maximale.

---

## Phase 7 — Corrélation multimodale

Corréler les anomalies dans une fenêtre temporelle définie :

```text
Anomalie log
   + hausse de latence
   + saturation d’un pool
   + logs contigus
        │
        ▼
Événement contextualisé unique
```

Le corrélateur devra :

- regrouper les signaux liés à une même source ;
- réduire les doublons ;
- calculer un score de confiance ;
- attacher le contexte avant et après l’anomalie ;
- conserver les observations pertinentes pour une future analyse LLM.

---

## Phase 8 — Sinks et persistance

Implémenter deux sorties :

- **stdout JSON Lines**, activée par défaut ;
- **PostgreSQL**, via une interface de connecteur générique.

Séparer strictement :

- `EventSink`, qui publie les événements ;
- `StateStore`, qui conserve l’état Drain3 et les baselines.

La défaillance temporaire de PostgreSQL ne devra pas bloquer indéfiniment l’ensemble du pipeline.

---

## Phase 9 — Configuration et exploitation

Centraliser dans un fichier YAML :

- listeners Unix/TCP et TLS ;
- capacité des buffers ;
- nombre de workers ;
- parsing des logs ;
- masques Drain3 ;
- paramètres d’apprentissage ;
- partitions ;
- détecteurs ;
- fenêtres de corrélation ;
- persistance ;
- sinks.

Ajouter :

- validation stricte au démarrage ;
- logs internes structurés ;
- métriques de santé ;
- profils de configuration standalone et serveur ;
- gestion des secrets par variables d’environnement.

---

## Phase 10 — Validation du MVP

Les tests devront couvrir :

- unités et composants ;
- pipeline complet ;
- client/serveur gRPC ;
- mode standalone ;
- restauration après snapshot ;
- compatibilité Drain3 ;
- backpressure ;
- reconnexion ;
- arrêt avec files non vides ;
- indisponibilité PostgreSQL ;
- tests de concurrence avec `go test -race ./...` ;
- benchmarks de débit et de consommation mémoire.

---

# Critères de réussite du MVP

Le MVP sera considéré comme validé s’il peut :

1. recevoir un flux continu de logs par `stdin` ;
2. fonctionner localement ou via gRPC Unix/TCP ;
3. découvrir et persister les templates sans configuration préalable ;
4. ingérer des métriques en parallèle ;
5. détecter des anomalies explicables ;
6. joindre les observations contiguës pertinentes ;
7. corréler logs et métriques ;
8. produire un événement stable en JSONL ou PostgreSQL ;
9. reprendre après redémarrage sans perdre son modèle ;
10. fonctionner sous charge sans fuite de goroutines ni croissance mémoire non bornée.

---

# Étapes après le MVP

## 1. Boucle de feedback dynamique

Ajouter une API permettant de marquer au runtime un événement ou un template comme :

- normal ;
- à ignorer ;
- exception temporaire ;
- symptomatique ;
- critique ;
- associé à un incident connu.

Ces retours devront influencer les scores futurs sans modifier brutalement l’apprentissage de Drain3.

## 2. Intégration avec un agent LLM

Construire un générateur de stimuli sélectionnant uniquement :

- l’événement principal ;
- les observations causalement ou temporellement proches ;
- les métriques les plus discriminantes ;
- l’historique d’incidents similaires ;
- les changements récents de configuration ou de déploiement.

Le LLM devra agir comme couche d’analyse et d’explication, et non comme détecteur primaire.

## 3. Nouvelles modalités

Étendre `Observation` à :

- traces OpenTelemetry ;
- événements Kubernetes ;
- changements de configuration ;
- déploiements CI/CD ;
- événements système et réseau.

## 4. Détection avancée

Introduire progressivement :

- saisonnalité ;
- détection de ruptures ;
- modèles multivariés ;
- relations causales ;
- regroupement d’incidents ;
- adaptation automatique des baselines.

## 5. Industrialisation

Ajouter :

- authentification et autorisation ;
- TLS/mTLS ;
- gestion multi-tenant ;
- quotas ;
- stockage distribué ;
- haute disponibilité ;
- partitionnement horizontal ;
- déploiement Kubernetes ;
- observabilité interne complète.

---

### Priorités recommandées

```text
1. Pipeline fiable et borné
2. Mode standalone et transport gRPC
3. Compatibilité Drain3
4. Persistance robuste
5. Détection explicable
6. Corrélation multimodale
7. Feedback utilisateur
8. Intégration LLM
9. Modèles avancés
10. Distribution à grande échelle
```

La décision structurante consiste à construire d’abord un **moteur déterministe, explicable et indépendant du LLM**. Drain3 fournit la compréhension structurelle des logs ; les détecteurs numériques caractérisent les métriques ; le corrélateur transforme ces signaux en événements riches. Le LLM pourra ensuite exploiter ces événements sans devoir interpréter l’intégralité des flux bruts.