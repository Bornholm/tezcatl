# Tutoriel : superviser un cluster Kubernetes depuis l'extérieur

Ce tutoriel n'installe **rien dans le cluster**. Tezcatl tourne sur
votre poste de travail et lit le cluster à distance. C'est le moyen le
plus rapide d'évaluer l'outil sur un vrai système, avant un déploiement
en cluster ([deploy-kubernetes.md](./deploy-kubernetes.md)).

Deux façons de lire le cluster, et rien n'empêche de commencer par l'une
puis de passer à l'autre :

- **avec `kubectl`**, en pipant sa sortie dans tezcatl. Rien à installer
  de plus, vous voyez exactement ce qui entre, et ça marche avec
  n'importe quelle authentification que `kubectl` sait faire, EKS et GKE
  compris. En échange, `kubectl logs --selector` ne suit pas les pods
  créés après son démarrage, et chaque flux se pilote à la main ;
- **avec le plugin `kubernetes`**, qui parle directement à l'API server.
  Un seul processus suit les logs de tous les pods, y compris les
  nouveaux, les events du cluster et les rollouts, et dérive le nom de
  service de chaque pod tout seul. En échange, il ne sait pas se servir
  des plugins d'authentification `exec` que réclament EKS et GKE, et
  `kubectl proxy` devient alors le détour obligé.

Commencez par `kubectl` pour comprendre ce que tezcatl fait de vos logs.
Passez au plugin dès que vous voulez suivre plus d'une application.

Prérequis : un accès `kubectl` au cluster, et le binaire tezcatl
installé localement. Sur Debian, Ubuntu ou Arch, le script
d'installation suffit, la variante `client` n'installant que la CLI :

```bash
curl -fsSL https://raw.githubusercontent.com/bornholm/tezcatl/main/install.sh | sh -s -- --variant client
```

Ailleurs, prenez l'archive de la dernière
[release](https://github.com/bornholm/tezcatl/releases) :

```bash
VERSION=0.13.2
curl -fsSL "https://github.com/bornholm/tezcatl/releases/download/v${VERSION}/tezcatl_${VERSION}_linux_amd64.tar.gz" |
  tar -xz tezcatl && sudo install tezcatl /usr/local/bin/
```

## 1. Première analyse : suivre une application en direct

Le mode standalone lit les logs sur stdin et écrit les événements
(anomalies contextualisées) sur stdout, une ligne JSON par événement :

```bash
kubectl logs --follow --timestamps --all-containers \
    --selector app=checkout --max-log-requests 20 |
  tezcatl standalone logs \
    --service checkout --environment prod \
    --state-dir ~/.local/state/tezcatl
```

Ce qu'il se passe :

- `--timestamps` fait préfixer chaque ligne d'un horodatage RFC3339, que
  tezcatl extrait. Les détecteurs raisonnent sur l'heure réelle des
  événements, pas sur l'heure d'arrivée ;
- les logs JSON (message, niveau, timestamp) sont parsés
  automatiquement, la découverte de templates portant sur le message ;
- pendant les 5 premières minutes, la période d'apprentissage par
  défaut, tezcatl apprend en silence. Ensuite, tout template inédit,
  rare ou en pic de fréquence produit un événement. La disparition d'un
  template n'est signalée que si ses intervalles sont réguliers, parce
  que le silence d'un template qui arrive par rafales n'apprend rien ;
- `--state-dir` **persiste l'apprentissage entre les sessions**. À la
  prochaine exécution, les templates connus ne redéclenchent rien.

Laissez tourner, et dans un autre terminal provoquez du bruit,
redémarrez un pod ou coupez une dépendance, pour voir les événements
sortir.

## 2. Rejouer un incident passé

C'est l'exercice le plus instructif : donner à tezcatl les logs d'un
incident déjà survenu et comparer ce qu'il trouve avec ce que vous aviez
diagnostiqué à la main.

```bash
# Les logs des dernières 6 heures (sans --follow)
kubectl logs --since=6h --timestamps --all-containers \
    --selector app=checkout --max-log-requests 20 > incident.log

# L'historique des déploiements, reconstruit depuis les ReplicaSets :
# chaque révision devient un « changement » corrélable
kubectl get rs -l app=checkout -o json | jq -c '
  .items[] | {
    time: .metadata.creationTimestamp,
    type: "deployment",
    version: .metadata.name,
    summary: ("revision " + (.metadata.annotations["deployment.kubernetes.io/revision"] // "?"))
  }' > changes.jsonl

# Rejeu : --replay fait avancer les fenêtres de corrélation sur le temps
# des événements, la chronologie est exacte
cat incident.log |
  tezcatl standalone logs \
    --service checkout --environment prod \
    --replay --changes-from changes.jsonl \
    --state-dir ./replay-state | jq .
```

Les événements produits portent le timestamp réel de l'anomalie, les
signaux qui la composent, le contexte (logs avant et après) et, si un
déploiement précédait, `related_changes` avec l'écart en secondes. C'est
une proximité dans le temps, pas une preuve de causalité.

Astuce : utilisez un `--state-dir` jetable par rejeu. L'état appris sur
un incident n'a pas vocation à polluer votre état de suivi.

## 3. Les événements Kubernetes comme flux de logs

Les events du cluster (BackOff, Killing, FailedScheduling) se prêtent
bien au mining de templates. Reformatez-les en JSON minimal (`time`,
`level`, `msg`, que tezcatl sait parser) et chaque type d'événement
devient un template dont la nouveauté et la fréquence sont surveillées :

```bash
kubectl get events --all-namespaces --watch -o json | jq -c --unbuffered '
  {
    time: (.lastTimestamp // .eventTime // .metadata.creationTimestamp),
    level: (if .type == "Warning" then "warn" else "info" end),
    msg: (.involvedObject.kind + "/" + .involvedObject.name + " " + .reason + ": " + .message)
  }' |
  tezcatl standalone logs \
    --service k8s-events --environment prod \
    --state-dir ~/.local/state/tezcatl
```

Après apprentissage, un `Warning` d'un genre nouveau, une première
`FailedMount` sur un volume par exemple, sort immédiatement en
événement.

## 4. La même chose sans plomberie : le plugin kubernetes

Les trois sections précédentes assemblent à la main ce que le plugin
`kubernetes` fait en un processus. Il parle à l'API server sans passer
par `kubectl` et alimente tezcatl avec trois flux :

- les **logs de tous les pods**, un stream par conteneur, y compris les
  pods créés après son démarrage. Le nom de service vient des labels
  `app.kubernetes.io/name` puis `app`, sinon du workload propriétaire ;
- les **events du cluster**, sous l'identité `k8s-events`, avec des
  résumés de la forme
  `Pod/checkout-7d9f8b-abcde BackOff: Back-off restarting failed container`.
  C'est exactement ce que le `jq` de la section 3 reconstruit ;
- les **mises à jour de spec des workloads**, converties en changements
  corrélables : nouvelle image (`deployment`), `rollout restart`
  (`restart`), variation de réplicas (`scale`), le reste en `config`. Le
  churn de status d'un rollout est ignoré.

Installez-le une fois, avec le paquet `tezcatl-plugin-kubernetes` ou
depuis la release. Le dépôt publie plusieurs plugins, il faut donc
nommer celui que vous voulez :

```bash
tezcatl plugin install github.com/bornholm/tezcatl kubernetes
```

Sans configuration, le plugin lit `$KUBECONFIG` puis `~/.kube/config`,
et prend le contexte courant :

```yaml
# k8s.yaml
plugins:
  sources:
    kubernetes:
      enabled: true
      config:
        environment: prod        # sinon, le namespace de chaque pod
        # context: staging       # défaut : current-context
        # namespaces: [prod]     # défaut : tous
        # label_selector: app=checkout
        # no_logs: false         # couper un des trois flux
        # no_events: false
        # no_changes: false
state:
  dir: /home/moi/.local/state/tezcatl
```

```bash
tezcatl standalone logs --config k8s.yaml --service k8s < /dev/null
```

Deux bizarreries de cette ligne de commande méritent une explication.
`< /dev/null` ferme stdin, dont on n'a plus besoin puisque le plugin
alimente le pipeline, et le processus reste vivant tant que le plugin
tourne. `--service` reste obligatoire et ne sert qu'aux lignes lues sur
stdin, donc à rien ici : mettez ce que vous voulez, le plugin nomme
lui-même chaque pod et chaque event.

Sur un `checkout` qui se met à échouer, les trois flux se répondent :

```json
{"kind":"anomaly.correlated","source":"prod/checkout",
 "summary":"new log template after learning period: database connection timeout after 30s (+1 correlated signals)",
 "related_changes":[{"change":{"type":"deployment","version":"busybox:1.37"},"offset_seconds":20.6}]}
{"kind":"anomaly.log.new_template","source":"prod/k8s-events",
 "summary":"new log template after learning period: Pod/checkout-599789597c-xvsth BackOff: Back-off restarting failed container"}
```

Le premier vient des logs du pod, le second des events du cluster, et le
`related_changes` du troisième flux : le plugin a vu l'image passer à
`busybox:1.37` vingt secondes avant l'anomalie. Personne n'a eu à
déclarer ce déploiement.

Si votre cluster est un EKS ou un GKE, l'authentification passe par un
binaire externe que le plugin ne sait pas appeler. Lancez `kubectl
proxy` et pointez le plugin dessus :

```bash
kubectl proxy &   # écoute sur 127.0.0.1:8001
```

```yaml
        api_server: http://127.0.0.1:8001
```

Un token ou un certificat client, eux, fonctionnent directement
(`token`, `token_file`, `ca_file`). Le schéma complet des options est
documenté dans [misc/config/standalone.yaml](../misc/config/standalone.yaml).

## 5. Ajouter les métriques : Prometheus via port-forward

Si le cluster héberge un Prometheus, un port-forward suffit pour
corréler métriques et logs, toujours sans rien déployer. Le poller est
fourni par le plugin `tezcatl-source-prometheus`, à installer une fois
(paquet `tezcatl-plugin-prometheus`, ou
`tezcatl plugin install github.com/bornholm/tezcatl prometheus`) :

```bash
kubectl -n monitoring port-forward svc/prometheus-server 9090:80 &
```

```yaml
# tezcatl.yaml (local)
plugins:
  sources:
    prometheus:
      enabled: true
      config:
        url: http://127.0.0.1:9090
        interval: 30s
        environment: prod
        queries:
          - name: latency_p95_s
            query: histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{service="checkout"}[5m])) by (le))
            service: checkout
state:
  dir: /home/moi/.local/state/tezcatl
```

```bash
kubectl logs --follow --timestamps --selector app=checkout --max-log-requests 20 |
  tezcatl standalone logs --config tezcatl.yaml \
    --service checkout --environment prod
```

Les requêtes portent la même identité `prod/checkout` que les logs. Une
anomalie de latence et un nouveau template dans la même fenêtre
produisent **un seul événement corrélé**, marqué `multimodal`.

Rien n'interdit d'activer les deux plugins dans le même fichier de
configuration. Avec le plugin kubernetes pour les logs et celui de
Prometheus pour les métriques, un seul
`tezcatl standalone logs --config tezcatl.yaml --service k8s < /dev/null`
surveille un cluster entier depuis un terminal.

## 6. Faire le ménage entre deux sessions

Le feedback fonctionne aussi hors ligne, directement sur l'état
persisté, sans qu'aucun processus tourne :

```bash
tezcatl templates --state-dir ~/.local/state/tezcatl
tezcatl metrics --state-dir ~/.local/state/tezcatl   # baselines apprises

tezcatl mark --state-dir ~/.local/state/tezcatl \
  --template "connection reset by peer" --as ignore

tezcatl mark --state-dir ~/.local/state/tezcatl \
  --template "Pod/<*> BackOff: Back-off restarting failed container" --as symptomatic

# Une série de métrique qui n'apprend rien se fait taire pareillement
# (clé exacte, nom de métrique ou glob) :
tezcatl mark --state-dir ~/.local/state/tezcatl \
  --metric "pod_restarts" --as ignore
```

Les marquages sont relus à la session suivante. `ignore` supprime les
signaux du template, `symptomatic` en produit un à chaque occurrence.
Pour les métriques, seul `ignore` a un sens.

## 7. Injecter un déploiement en cours de session

Le plugin kubernetes détecte déjà les rollouts. Cette section ne sert
donc qu'à l'approche `kubectl`, ou à signaler un changement que l'API ne
voit pas, une migration de schéma ou un feature flag. Passez par une
FIFO :

```bash
mkfifo /tmp/tezcatl-changes

kubectl logs --follow --timestamps --selector app=checkout --max-log-requests 20 |
  tezcatl standalone logs --service checkout --environment prod \
    --changes-from /tmp/tezcatl-changes &

# ... au moment du déploiement :
kubectl rollout restart deploy/checkout
echo '{"type":"deployment","version":"checkout:rollout-manuel","summary":"rollout restart"}' > /tmp/tezcatl-changes
```

## Limites, et quand passer à autre chose

`kubectl logs --selector` ne suit pas les pods créés après son
démarrage. Après un rollout, ou dès qu'un conteneur meurt, les flux se
ferment et la commande se termine. Relancez-la, ou bouclez :

```bash
while true; do
  kubectl logs --follow --timestamps --all-containers --since=10s \
    --selector app=checkout --max-log-requests 20
  sleep 1
done | tezcatl standalone logs --service checkout --environment prod \
         --state-dir ~/.local/state/tezcatl
```

Le `--since` compte. Sans lui, chaque tour relit l'historique complet du
conteneur, et tezcatl compte plusieurs fois les mêmes lignes : sur un
pod en CrashLoop, j'ai vu un template atteindre 62 occurrences en trois
minutes alors qu'il n'était apparu que quatre fois. Les baselines de
fréquence deviennent alors du bruit.

Tout ça est la raison principale de passer au plugin de la section 4.

Les deux approches partagent une limite de fond : tout tourne sur votre
poste, donc la surveillance s'arrête quand vous fermez le terminal, et
l'état appris vit dans votre `~`. Pour une surveillance permanente,
déployez le serveur dans le cluster
([deploy-kubernetes.md](./deploy-kubernetes.md)). Même moteur, même
état, mêmes commandes de feedback, plus le journal d'événements
interrogeable (`tezcatl events`), les briefings d'incident
(`tezcatl incidents`) et la TUI (`tezcatl top`), qui demandent tous un
serveur en marche.
