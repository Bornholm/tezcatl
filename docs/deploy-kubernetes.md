# Déployer tezcatl sur Kubernetes

Cette procédure déploie le serveur tezcatl dans un cluster, le branche
sur Prometheus, remonte les logs des pods et les events du cluster via
le plugin kubernetes, et signale les déploiements depuis la CI. Elle
s'appuie sur l'image publiée à chaque release :
`ghcr.io/bornholm/tezcatl:<version>` (distroless statique, amd64/arm64,
le binaire en entrypoint).

> **Périmètre actuel** : pas encore d'opérateur (voir la roadmap,
> chantier C4). Le déploiement ci-dessous est volontairement simple :
> un serveur central, les métriques par l'API Prometheus, et un
> collecteur unique (plugin `tezcatl-source-kubernetes`) pour les logs
> de pods et les events. C'est suffisant pour valider la valeur avant
> d'industrialiser.

## 1. Le serveur

Namespace, configuration, état persistant, Deployment (1 réplique — le
serveur est mono-instance à ce stade) et Service :

```yaml
# tezcatl.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: tezcatl
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: tezcatl-config
  namespace: tezcatl
data:
  server.yaml: |
    server:
      listen:
        - tcp://0.0.0.0:4242
    logs:
      detection:
        learning_period: 15m
    correlation:
      window: 30s
    state:
      dir: /var/lib/tezcatl/state
      save_interval: 30s
    sinks:
      stdout:
        enabled: true   # les événements sortent dans les logs du pod
    logging:
      level: info
      format: json
      stats_interval: 1m
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: tezcatl-state
  namespace: tezcatl
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tezcatl
  namespace: tezcatl
spec:
  replicas: 1
  strategy:
    type: Recreate   # une seule instance doit détenir l'état
  selector:
    matchLabels: { app: tezcatl }
  template:
    metadata:
      labels: { app: tezcatl }
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        fsGroup: 65532   # droits d'écriture sur le PVC
      containers:
        - name: tezcatl
          image: ghcr.io/bornholm/tezcatl:0.1.0   # épingler la version
          args: ["server", "--config", "/etc/tezcatl/server.yaml"]
          ports:
            - containerPort: 4242
          volumeMounts:
            - name: config
              mountPath: /etc/tezcatl
              readOnly: true
            - name: state
              mountPath: /var/lib/tezcatl
          resources:
            requests: { cpu: 100m, memory: 128Mi }
            limits: { memory: 512Mi }
      volumes:
        - name: config
          configMap: { name: tezcatl-config }
        - name: state
          persistentVolumeClaim: { claimName: tezcatl-state }
---
apiVersion: v1
kind: Service
metadata:
  name: tezcatl
  namespace: tezcatl
spec:
  selector: { app: tezcatl }
  ports:
    - port: 4242
      targetPort: 4242
```

```bash
kubectl apply -f tezcatl.yaml
kubectl -n tezcatl logs deploy/tezcatl -f | jq .   # les événements
```

Le serveur est joignable depuis le cluster à
`tcp://tezcatl.tezcatl:4242` ; l'état appris survit aux redémarrages via
le PVC.

## 2. Métriques : brancher Prometheus

C'est l'entrée la plus rentable sur Kubernetes — aucun agent à déployer,
tezcatl interroge l'API Prometheus existante. Dans la ConfigMap :

```yaml
    metrics:
      prometheus:
        enabled: true
        # kube-prometheus-stack : http://prometheus-operated.monitoring:9090
        url: http://prometheus-server.monitoring:9090
        interval: 30s
        environment: prod
        queries:
          - name: latency_p95_s
            query: histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket[5m])) by (le, service))
            service_label: service
          - name: pod_restarts
            query: sum(increase(kube_pod_container_status_restarts_total[5m])) by (namespace)
            service_label: namespace
      detection:
        z_threshold: 3
        # thresholds:
        #   - metric: latency_p95_s
        #     max: 1.5
```

`service_label` fait de chaque valeur du label une source distincte —
les anomalies de métriques se corrèlent alors avec les logs et
déploiements du même service.

## 3. Logs, events et déploiements : le plugin kubernetes

Le plugin `tezcatl-source-kubernetes` parle directement à l'API server
(pas de kubectl) : il suit les events du cluster (BackOff, Killing,
FailedScheduling… — un flux de logs sous l'identité `k8s-events`), les
logs de **tous** les pods, y compris ceux créés après son démarrage —
la limite de `kubectl logs --selector` ne s'applique pas — et les
mises à jour de spec des workloads (Deployments, StatefulSets,
DaemonSets), converties en **changements** corrélables : nouvelle
image (`type: deployment`, version `checkout:v1.8.2`),
`kubectl rollout restart` (`restart`), variation de réplicas
(`scale`), autre modification de spec (`config`). Le churn de status
d'un rollout (generation inchangée) est ignoré. Le service est dérivé
des labels (`app.kubernetes.io/name`, `app`) puis du workload
propriétaire, et le namespace devient l'environnement sauf override.

Un collecteur unique dans le cluster : RBAC en lecture seule + un pod
qui exécute `tezcatl ingest source kubernetes` vers le serveur (les
binaires sont téléchargés depuis la release, l'image tezcatl étant
distroless) :

```yaml
# collector.yaml — namespace tezcatl
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tezcatl-collector
  namespace: tezcatl
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: tezcatl-collector
rules:
  - apiGroups: [""]
    resources: [pods, events]
    verbs: [get, list, watch]
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets]
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: tezcatl-collector
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: tezcatl-collector
subjects:
  - kind: ServiceAccount
    name: tezcatl-collector
    namespace: tezcatl
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tezcatl-collector
  namespace: tezcatl
spec:
  replicas: 1
  selector:
    matchLabels: { app: tezcatl-collector }
  template:
    metadata:
      labels: { app: tezcatl-collector }
    spec:
      serviceAccountName: tezcatl-collector
      initContainers:
        - name: fetch
          image: curlimages/curl:8.10.1
          command: ["sh", "-c"]
          args:
            - VERSION=0.5.0 &&
              curl -fsSL https://github.com/bornholm/tezcatl/releases/download/v${VERSION}/tezcatl_${VERSION}_linux_amd64.tar.gz
              | tar -xz -C /opt/tezcatl tezcatl &&
              curl -fsSL https://github.com/bornholm/tezcatl/releases/download/v${VERSION}/tezcatl-source-kubernetes_${VERSION}_linux_amd64.tar.gz
              | tar -xz -C /opt/tezcatl tezcatl-source-kubernetes
          volumeMounts:
            - { name: bin, mountPath: /opt/tezcatl }
      containers:
        - name: collect
          image: busybox:1.36
          command: ["/opt/tezcatl/tezcatl"]
          args:
            - ingest
            - source
            - --target=tcp://tezcatl.tezcatl:4242
            - --plugins-dir=/opt/tezcatl
            - '--source-config={"environment":"prod"}'
            - kubernetes
          volumeMounts:
            - { name: bin, mountPath: /opt/tezcatl }
      volumes:
        - name: bin
          emptyDir: {}
```

Les identifiants in-cluster (token du serviceaccount, CA) sont
autodétectés ; la config JSON du plugin permet de restreindre
(`namespaces`, `label_selector`), de couper une des deux sources
(`no_events`, `no_logs`) ou de changer la dérivation d'identité
(`service_labels`) — schéma complet dans `misc/config/standalone.yaml`.
Depuis un poste de travail, le même plugin fonctionne sans rien
déployer : sans configuration il lit `$KUBECONFIG` puis
`~/.kube/config` (`--source-config='{"context":"prod"}'` pour choisir
un contexte ; tokens et certificats clients sont supportés, pas les
plugins d'authentification `exec` type EKS/GKE — dans ce cas,
`kubectl proxy` puis
`--source-config='{"api_server":"http://127.0.0.1:8001"}'`).

### Variante : un forwarder kubectl par application

L'ancienne approche reste valable pour ne suivre qu'une application
sans droits cluster : RBAC minimal + un pod qui pipe `kubectl logs`
vers `tezcatl ingest logs` :

```yaml
# forwarder-checkout.yaml — à créer dans le namespace de l'application
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tezcatl-forwarder
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: tezcatl-forwarder
rules:
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, watch]
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tezcatl-forwarder
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: tezcatl-forwarder
subjects:
  - kind: ServiceAccount
    name: tezcatl-forwarder
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tezcatl-forward-checkout
spec:
  replicas: 1
  selector:
    matchLabels: { app: tezcatl-forward-checkout }
  template:
    metadata:
      labels: { app: tezcatl-forward-checkout }
    spec:
      serviceAccountName: tezcatl-forwarder
      initContainers:
        - name: fetch-tezcatl
          image: curlimages/curl:8.10.1
          command: ["sh", "-c"]
          args:
            - curl -fsSL https://github.com/bornholm/tezcatl/releases/download/v0.1.0/tezcatl_0.1.0_linux_amd64.tar.gz
              | tar -xz -C /opt/tezcatl tezcatl
          volumeMounts:
            - { name: bin, mountPath: /opt/tezcatl }
      containers:
        - name: forward
          image: bitnami/kubectl:1.31
          command: ["sh", "-c"]
          args:
            - kubectl logs --follow --timestamps --all-containers
              --selector app=checkout --max-log-requests 20 |
              /opt/tezcatl/tezcatl ingest logs
              --target tcp://tezcatl.tezcatl:4242
              --service checkout --environment prod
          volumeMounts:
            - { name: bin, mountPath: /opt/tezcatl }
      volumes:
        - name: bin
          emptyDir: {}
```

Limites assumées de ce forwarder : les pods créés *après* son démarrage
ne sont suivis qu'à son prochain redémarrage (le processus se termine
quand tous les flux se ferment — après un rollout par exemple — et
Kubernetes le relance, ce qui ré-attache tout) ; réservez-le aux
applications qui comptent. `--timestamps` fournit l'heure exacte, et les
logs JSON sont parsés automatiquement.

## 4. Signaler les déploiements depuis la CI

Le plugin détecte déjà les rollouts au niveau du cluster (section 3).
Un signalement depuis la CI reste utile pour porter une version plus
riche que le tag d'image (SHA du commit, lien de pipeline) ou couvrir
des changements que l'API ne voit pas (migration de schéma, feature
flag). Après chaque déploiement, un job one-shot dans le cluster
(l'image sert de client) :

```bash
kubectl run tezcatl-change-$RANDOM --rm -i --restart=Never \
  --image=ghcr.io/bornholm/tezcatl:0.1.0 -- \
  ingest change \
    --target tcp://tezcatl.tezcatl:4242 \
    --service checkout --environment prod \
    --type deployment \
    --change-version "checkout:${CI_COMMIT_SHA}" \
    --summary "deploy via CI"
```

Toute anomalie dans le quart d'heure suivant sortira avec ce déploiement
en `related_changes`.

## 5. Boucle de feedback

Depuis un poste de travail (binaire installé localement), via
port-forward :

```bash
kubectl -n tezcatl port-forward svc/tezcatl 4242 &

tezcatl templates --target tcp://127.0.0.1:4242
tezcatl mark --target tcp://127.0.0.1:4242 \
  --template "connection reset by peer" --as ignore
```

Les marquages sont persistés dans le PVC avec le reste de l'état.

## 6. Sorties

- **stdout** (défaut) : les événements sont dans les logs du pod, donc
  dans votre stack de logs existante (Loki, etc.) ;
- **PostgreSQL** : section `sinks.postgres` de la ConfigMap, DSN via une
  variable d'environnement injectée d'un Secret
  (`env` + `${TEZCATL_POSTGRES_DSN}` dans le YAML) ;
- **webhook** : un POST JSON par événement vers votre système d'alerte.

## Limites et suite

Une seule réplique (l'état n'est pas partagé) et pas de TLS entre pods
(rajouter `tls://` + certificat si le trafic sort du cluster). La suite
naturelle (roadmap C4) : regroupement par workload/namespace,
événements multi-services et corrélation des déploiements collectés
automatiquement — ce déploiement minimal sert précisément à valider ce
qui mérite d'y être industrialisé.
