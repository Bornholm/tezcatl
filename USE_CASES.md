### Cela rend le projet beaucoup plus pertinent

Tu as déjà **deux terrains d’utilisation réels** :

1. tes applications Dokku, Docker et systemd ;
2. une application Kubernetes souffrant d’un problème concret de diagnostic.

Ce n’est plus seulement une idée abstraite d’AIOps : tu peux développer Tezcatl par **dogfooding**, à partir d’incidents réellement rencontrés.

---

### Le bon positionnement

Je ne chercherais pas à remplacer Prometheus, Loki ou OpenTelemetry. Tezcatl devrait être une **couche de corrélation et d’explication** au-dessus des données disponibles :

```text
journald ───────────┐
Docker/Dokku logs ──┤
Kubernetes logs ────┼──► Tezcatl ──► événements corrélés
Prometheus ─────────┤                 et explicables
déploiements ───────┘
```

Sa question centrale serait :

> « Qu’est-ce qui a changé, quels composants sont affectés et quelles observations permettent de l’expliquer ? »

---

### Deux modes de déploiement

#### Mode léger : projets personnels

Un processus unique, sans infrastructure obligatoire :

```bash
journalctl -fu mon-service | tezcatl ingest logs \
  --service mon-service \
  --environment production
```

Ou :

```bash
docker logs -f mon-conteneur 2>&1 |
  tezcatl ingest logs --service mon-app
```

Avec :

- stockage mémoire ou SQLite ;
- métriques récupérées depuis Prometheus ;
- sortie des anomalies sur `stdout` ;
- notification webhook facultative ;
- consommation mémoire contenue.

Ce mode peut déjà apporter une vraie valeur sur Dokku et systemd.

#### Mode Kubernetes

Une architecture centralisée :

```text
Pods / OpenTelemetry Collector
             │
             ▼
      Tezcatl Server
             │
      ┌──────┴──────┐
      ▼             ▼
 Prometheus     PostgreSQL
```

Les signaux doivent partager des attributs canoniques :

```text
service.name
service.version
deployment.environment
k8s.namespace.name
k8s.deployment.name
k8s.pod.name
trace_id
```

Sans cette normalisation, la corrélation sera fragile.

---

### Le véritable besoin Kubernetes

La difficulté n’est probablement pas de trouver tous les logs d’erreur, mais d’assembler une chronologie comme celle-ci :

```text
14:02  Déploiement de checkout:v1.8.2
14:04  Augmentation de la latence p95
14:05  Apparition d’un nouveau template de log SQL
14:06  Saturation du pool de connexions
14:07  Multiplication des redémarrages de pods
```

Tezcatl pourrait produire :

```json
{
  "title": "Dégradation du service checkout",
  "started_at": "2026-08-24T14:04:00Z",
  "severity": "critical",
  "affected_services": ["checkout", "payments"],
  "trigger": {
    "type": "metric_change",
    "signal": "http.server.request.duration.p95",
    "change_percent": 340
  },
  "related_changes": [
    {
      "type": "deployment",
      "version": "checkout:v1.8.2",
      "time_offset": "-2m"
    }
  ],
  "evidence": [
    "nouveau template de log: database connection timeout",
    "pool de connexions utilisé à 100%",
    "3 pods redémarrés"
  ],
  "hypotheses": [
    {
      "description": "épuisement du pool après le déploiement",
      "confidence": 0.81
    }
  ]
}
```

Il faut néanmoins distinguer **preuve**, **corrélation** et **hypothèse**. Une proximité temporelle avec un déploiement ne prouve pas qu’il est responsable.

---

### MVP adapté à tes besoins

#### Phase 1 — systemd, Docker et Dokku

- ingestion de logs depuis `stdin` ;
- parsing JSON et texte brut ;
- attribution obligatoire d’un service et d’un environnement ;
- extraction de templates ;
- détection de nouveaux templates et variations de fréquence ;
- fenêtres temporelles ;
- sortie JSON sur `stdout`.

Cette phase valide le moteur sans dépendre de Kubernetes.

#### Phase 2 — Prometheus

- interrogation périodique de l’API Prometheus ;
- détection de changements simples :
  - moyenne et écart-type glissants ;
  - EWMA ;
  - médiane et MAD ;
  - changement de tendance ;
- corrélation temporelle logs–métriques ;
- production d’un artefact d’incident.

#### Phase 3 — Kubernetes

- ingestion des métadonnées Kubernetes ;
- détection des déploiements, redémarrages et changements de configuration ;
- regroupement par namespace, workload et service ;
- corrélation entre pods appartenant au même workload ;
- déploiement par Helm chart.

#### Phase 4 — Explication

- chronologie de l’incident ;
- classement des preuves ;
- score de confiance explicable ;
- résumé destiné à un humain ;
- export optionnel vers un LLM.

---

### Ce qu’il ne faut pas faire initialement

- collecter directement tous les formats possibles ;
- reconstruire Prometheus ou Loki ;
- promettre une analyse automatique de cause racine ;
- commencer par un opérateur Kubernetes complexe ;
- utiliser un LLM pour décider si une anomalie existe ;
- développer une interface graphique avant d’avoir validé les résultats.

---

### Mon avis réaliste

**Pour tes projets personnels**, Tezcatl peut devenir rapidement utile, même avec une implémentation modeste.

**Pour la grosse application Kubernetes**, il peut avoir une valeur importante, mais la difficulté sera principalement organisationnelle et sémantique : qualité des logs, labels cohérents, métriques exploitables et identification des déploiements. Aucun algorithme ne compensera totalement une télémétrie incohérente.

La meilleure stratégie serait de sélectionner **trois incidents passés connus** et de construire le MVP pour qu’il puisse reconstituer leur chronologie. Tu obtiendras ainsi un objectif concret et un premier benchmark :

> Tezcatl rassemble-t-il automatiquement les éléments qu’un ingénieur doit actuellement chercher manuellement dans plusieurs outils ?

Si oui, même sans innovation algorithmique, le projet aura déjà une **valeur pratique démontrable**.