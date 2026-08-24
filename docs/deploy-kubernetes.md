# Déployer tezcatl sur Kubernetes

Cette procédure déploie le serveur tezcatl dans un cluster, le branche
sur Prometheus, remonte les logs d'applications choisies et signale les
déploiements depuis la CI. Elle s'appuie sur l'image publiée à chaque
release : `ghcr.io/bornholm/tezcatl:<version>` (distroless statique,
amd64/arm64, le binaire en entrypoint).

> **Périmètre actuel** : pas encore d'opérateur ni de collecteur
> DaemonSet (voir la roadmap, chantier C4). Le déploiement ci-dessous
> est volontairement simple : un serveur central, les métriques par
> l'API Prometheus, et les logs des applications *critiques* via un
> forwarder. C'est suffisant pour valider la valeur avant d'industrialiser.

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

## 3. Logs des applications critiques

En attendant le collecteur natif, un *forwarder* par application suit
les pods d'un sélecteur et pousse vers le serveur. RBAC minimal + un pod
qui pipe `kubectl logs` vers `tezcatl ingest logs` (le binaire est
téléchargé depuis la release, l'image tezcatl étant distroless) :

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

Après chaque déploiement, un job one-shot dans le cluster (l'image sert
de client) :

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

Une seule réplique (l'état n'est pas partagé), pas de TLS entre pods
(rajouter `tls://` + certificat si le trafic sort du cluster), et la
collecte de logs reste par-application. La suite naturelle (roadmap
C4) : regroupement par workload/namespace, événements multi-services et
collecteur de logs natif — ce déploiement minimal sert précisément à
valider ce qui mérite d'y être industrialisé.
