# Tutoriel : superviser un cluster Kubernetes depuis l'extérieur, avec kubectl

Ce tutoriel n'installe **rien dans le cluster** : tezcatl tourne sur
votre poste de travail et tout passe par `kubectl`. C'est le moyen le
plus rapide d'évaluer l'outil sur un vrai système — diagnostic ad hoc,
rejeu d'un incident passé, apprentissage progressif — avant un
déploiement dans le cluster ([deploy-kubernetes.md](./deploy-kubernetes.md)).

Prérequis : un accès `kubectl` au cluster, et le binaire tezcatl
installé localement. Sur Debian/Ubuntu ou Arch, le script d'installation
suffit — la variante `client` n'installe que la CLI :

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
  tezcatl extrait — les détecteurs raisonnent sur l'heure réelle des
  événements, pas sur l'heure d'arrivée ;
- les logs JSON (message, niveau, timestamp) sont parsés automatiquement,
  la découverte de templates porte sur le message ;
- pendant les 5 premières minutes (période d'apprentissage par défaut),
  tezcatl apprend silencieusement les templates ; ensuite, tout template
  inédit, rare ou en pic de fréquence produit un événement. La
  disparition, elle, n'est signalée que pour les templates dont les
  intervalles sont réguliers : le silence d'un template qui arrive par
  rafales n'apprend rien ;
- `--state-dir` **persiste l'apprentissage entre les sessions** : à la
  prochaine exécution, les templates connus ne redéclenchent rien.

Laissez tourner, et dans un autre terminal provoquez du bruit (redémarrez
un pod, coupez une dépendance) pour voir les événements sortir.

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
signaux qui la composent, le contexte (logs avant/après) et, si un
déploiement précédait, `related_changes` avec l'écart en secondes —
proximité temporelle, pas preuve de causalité.

Astuce : utilisez un `--state-dir` jetable par rejeu (l'état appris sur
un incident n'a pas vocation à polluer votre état de suivi).

## 3. Les événements Kubernetes comme flux de logs

Les events du cluster (BackOff, Killing, FailedScheduling…) se prêtent
très bien au mining de templates. En les reformatant en JSON minimal
(`time`/`level`/`msg`, que tezcatl sait parser), chaque type d'événement
devient un template dont la nouveauté ou la fréquence est surveillée :

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

Après apprentissage, un `Warning` d'un genre nouveau (première
`FailedMount` sur un volume, par exemple) sort immédiatement en
événement.

## 4. Ajouter les métriques : Prometheus via port-forward

Si le cluster héberge un Prometheus, un port-forward suffit pour
corréler métriques et logs — toujours sans rien déployer. Le poller
est fourni par le plugin `tezcatl-source-prometheus`, à installer une
fois (paquet `tezcatl-plugin-prometheus`, ou
`tezcatl plugin install github.com/bornholm/tezcatl prometheus` — le
dépôt publie plusieurs plugins, il faut nommer celui qu'on veut) :

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

Les requêtes portent la même identité `prod/checkout` que les logs : une
anomalie de latence et un nouveau template dans la même fenêtre
produisent **un seul événement corrélé**, marqué `multimodal`.

Note : avec le poller actif, le processus ne s'arrête plus à la fin de
stdin — arrêt par Ctrl-C.

## 5. Faire le ménage entre deux sessions

Le feedback fonctionne aussi hors-ligne, directement sur l'état
persisté (aucun processus en cours requis) :

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

Les marquages sont relus à la session suivante : `ignore` supprime les
signaux du template, `symptomatic` en produit un à chaque occurrence.
Pour les métriques, seul `ignore` a un sens.

## 6. Injecter un déploiement en cours de session

Pour corréler un déploiement que vous faites *pendant* qu'une session
tourne, passez par une FIFO :

```bash
mkfifo /tmp/tezcatl-changes

kubectl logs --follow --timestamps --selector app=checkout --max-log-requests 20 |
  tezcatl standalone logs --service checkout --environment prod \
    --changes-from /tmp/tezcatl-changes &

# ... au moment du déploiement :
kubectl rollout restart deploy/checkout
echo '{"type":"deployment","version":"checkout:rollout-manuel","summary":"rollout restart"}' > /tmp/tezcatl-changes
```

## Limites de l'approche kubectl

`kubectl logs --selector` ne suit pas les pods créés après son
démarrage : après un rollout, les flux se ferment et la commande se
termine — relancez-la (ou bouclez avec `while true; do …; done`). Pour
une supervision permanente, passez au déploiement dans le cluster
([deploy-kubernetes.md](./deploy-kubernetes.md)) : même moteur, même
état, mêmes commandes de feedback.
