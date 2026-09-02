# Tezcatl

> Tezcatl, « miroir », de Tezcatlipoca, le « miroir fumant »

Tezcatl lit vos logs et vos métriques, apprend ce qui est normal, et signale ce qui ne l'est pas.

Il regroupe les lignes de log en templates avec un port Go de [Drain3](https://github.com/logpai/Drain3), vérifié par golden tests contre l'implémentation Python. Il compare chaque métrique à la baseline qu'il a apprise pour elle. Quand plusieurs signaux tombent dans la même fenêtre de temps, il les rassemble en un seul événement qui cite ses preuves, et y attache les déploiements récents. Chaque événement sort en JSON, lisible par un opérateur comme par un agent LLM.

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
tezcatl incidents --since 24h --format markdown   # rapport auto-descriptif
tezcatl incidents --since 24h --format json       # structure brute

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

```bash
# Oublier ce qui a été appris sur des partitions qui ne reviendront pas
tezcatl forget --dry-run 'production/session-*'   # ce que ça toucherait
tezcatl forget 'production/session-*'             # puis pour de vrai
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

Tezcatl persiste ce qu'il apprend, templates Drain3 et baselines, dans `state.dir` et le recharge au démarrage. Un redémarrage ne relance donc pas une salve de fausses alertes sur des templates déjà connus. Tout apprentissage ne mérite pas d'être gardé, cela dit : des unités qui ne reviendront jamais sous le même nom, ou des lignes ingérées par erreur, laissent des templates qu'aucun marquage n'enlève. `tezcatl forget` les efface par partition, avec `--dry-run` pour voir avant. Les marquages survivent : faire taire un motif est une décision, pas un apprentissage.

Un détecteur n'a aucune mémoire de ce qu'il vient de dire : sans amortissement, un template qui déborde toutes les vingt minutes produit un signal toutes les vingt minutes. Le silence après un rapport double à chaque redite (`dampening.cooldown`), une aggravation le coupe, et un motif qui se tait assez longtemps repart de zéro. Sur l'instance de dogfooding, cela retire un quart des événements d'une journée sans en faire taire un seul définitivement.

Enfin, `critical` demande une corroboration et pas seulement un écart : deux modalités qui concordent, un changement déclaré juste avant, ou un jugement humain déjà posé (template symptomatique, seuil configuré). Un z-score solitaire, aussi extrême soit-il, s'arrête à `warning`. Autrement dit, la statistique seule ne crie jamais.

`tezcatl incidents` remonte d'un cran : il assemble les anomalies en récits. Deux événements appartiennent au même incident quand ils sont **apparentés**, pas simplement voisins dans le temps : même service, même changement corrélé, ou assez simultanés pour être un seul événement qui se propage (le même cycle de collecte). Un incident se termine après un silence (`--gap`) et ne dépasse jamais une durée plafond (`--max-duration`), parce qu'un service qui déraille toute la nuit est un état chronique, pas une histoire. Sans ce critère de parenté, une machine qui produit une anomalie toutes les huit minutes voit sa nuit entière fondue en un seul « incident » de cinq heures, ce qui n'apprend rien.

La sortie `--format markdown` est faite pour être donnée telle quelle à un agent LLM. Elle explique son propre schéma avant de présenter les données : ce qu'est un déclencheur par opposition à une preuve, que `<NUM>` et `<*>` sont des masques et non des valeurs, que les preuves sont agrégées, que les changements associés sont une corrélation et rien d'autre. Elle énonce aussi ce que tezcatl **ne sait pas** : il ignore la causalité, il ne voit que ce qu'on lui donne, un détecteur silencieux n'est pas un système sain, et les frontières d'un incident sont une heuristique. Un agent qui lit ces données sans ces avertissements invente du sens qui n'y est pas.

`tezcatl top` ouvre trois onglets sur un serveur en marche. L'onglet **événements** suit le flux en direct et rejoue l'historique persisté, avec le détail des signaux déclencheurs et des changements corrélés sous `entrée`. L'onglet **templates** marque au clavier ce qui est du bruit, sans recopier le template. L'onglet **metrics** trie les séries par écart à leur baseline et permet de faire taire une série avec `i`.

[Itztli](./docs/itztli.md) est le pendant web de `tezcatl top` : un binaire séparé et optionnel qui sert les mêmes données dans un navigateur. Derniers incidents en écran d'accueil, détail avec déclencheur, preuves et changements corrélés, marquage des templates et des métriques, et en option un bouton « Explain » qui fait lire le rapport d'un incident à un LLM. L'accès se protège par un mot de passe unique ou par OIDC.

## Déploiement

Les sources actives sont des plugins : `host` (CPU, mémoire, disque, conteneurs Docker), `kubernetes` (events, logs de pods, rollouts), `prometheus` (requêtes PromQL), `journald` (le journal systemd) et `scaleway` (conteneurs serverless, via le CLI `scw` pour la découverte et Cockpit pour les données).

Chaque release publie des paquets Debian et Arch (`tezcatl`, `tezcatl-server`, les plugins, `tezcatl-dokku`, `tezcatl-itztli`) et une image de conteneur, `ghcr.io/bornholm/tezcatl`. Le script [install.sh](./install.sh) installe le jeu de paquets d'une variante et vérifie les sommes de contrôle avant d'installer quoi que ce soit :

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sh -s -- --variant dokku
```

Cinq variantes. `client` n'installe que la CLI et suffit pour envoyer des logs vers un serveur distant. `server` ajoute l'unité systemd, `/etc/tezcatl` et l'utilisateur dédié. `docker` ajoute le plugin de métriques hôte et conteneurs. `dokku` ajoute l'ingestion des logs par application et le hook de déploiement. `kubernetes` ajoute le plugin qui surveille un cluster depuis cette machine.

Sur une machine systemd, le plugin `journald` s'ajoute à la variante choisie, quelle qu'elle soit : le journal est la source de logs qui va de soi. Son unité reste à activer, parce qu'ingérer tout le journal d'une machine est une décision et pas un défaut. Utilisez `--no-journald` pour ne pas l'installer du tout. Lisant tout le journal, il en exclut par défaut les unités de tezcatl : le serveur écrit ses événements sur stdout, systemd les enregistre, et un plugin qui les relit les rend au serveur, qui en fait des événements. Le cycle de vie des unités reste visible, puisque c'est systemd qui le journalise.

Il regroupe aussi les unités jetables sous le nom de ce qu'elles sont : chaque connexion SSH crée `session-2174.scope`, chaque conteneur un `docker-<hex>.scope`, et prises au mot ces unités deviennent autant de services dont les baselines n'ont jamais le temps de mûrir. Sur l'instance de dogfooding, elles représentaient deux templates appris sur trois, et 100 des 130 partitions. Elles sont désormais lues comme `session`, `user`, `docker` ; l'unité exacte reste sur l'observation, en attribut `journald.unit`. Réglage : `"collapse_transient_units": false` dans `/etc/tezcatl/journald.json`.

```bash
systemctl enable --now tezcatl-journald   # réglages dans /etc/tezcatl/journald.json
```

L'interface web s'ajoute de la même façon à n'importe quelle variante, avec `--itztli` : elle aussi est un choix, pas un défaut, parce qu'elle expose le serveur en HTTP derrière un mot de passe ou un fournisseur d'identité ([guide](./docs/itztli.md)).

Relancer le même script met à jour vers la dernière release, et ne fait rien si la version est déjà installée. Utilisez `--version vX.Y.Z` pour épingler une version, `--download-only` pour récupérer les paquets vérifiés sans les installer.

[Prise en main](./docs/getting-started.md) explique les concepts à un
administrateur système qui n'a pas à connaître les statistiques : ce
qu'est un template, ce qu'est une baseline, pourquoi un z-score énorme
peut ne rien vouloir dire, et ce que tezcatl ne sait pas faire. Vingt
minutes, dont cinq de manipulation, sans rien installer d'autre que le
binaire.

Trois guides pas à pas :

- [Serveur Dokku/Ubuntu](./docs/deploy-dokku.md) couvre les paquets, l'ingestion des logs par application et le hook de déploiement.
- [Kubernetes](./docs/deploy-kubernetes.md) couvre le serveur en Deployment, les métriques via Prometheus, les logs de pods, les events du cluster et les rollouts vus par le plugin `tezcatl-source-kubernetes`.
- [Tutoriel Kubernetes](./docs/tutorial-kubernetes.md) montre comment superviser un cluster depuis l'extérieur sans rien y installer, par `kubectl` ou par le plugin `kubernetes` : analyse en direct, rejeu d'incident, events, rollouts, et Prometheus branché sur les mêmes identités de service.

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
