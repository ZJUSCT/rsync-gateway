# rsync-gateway Helm chart

Deploys the rsync-gateway manager — a controller-runtime controller with the
embedded rsync-proxy data plane in one process — as a Deployment (default,
2 replicas, no leader election) or a DaemonSet (`workloadKind: DaemonSet`,
one manager per selected node, for topologies that need a local instance
everywhere), plus a LoadBalancer Service fronting the rsync listeners, a
ClusterIP Service exposing the admin/metrics endpoints, and the
ServiceAccount/ClusterRole/ClusterRoleBinding the manager needs. The
GatewayConfig and RsyncRoute CRDs ship in `crds/`.

## Install

The Gateway API CRDs are a cluster prerequisite:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
```

From a local checkout:

```bash
helm install rsync-gateway ./charts/rsync-gateway
```

or the published chart:

```bash
helm install rsync-gateway oci://ghcr.io/zjusct/charts/rsync-gateway --version <version>
```

User objects (GatewayClass + GatewayConfig, Gateway, RsyncRoute) are not
managed by this chart; see the walkthrough in the project
[README.gateway.md](https://github.com/ZJUSCT/rsync-gateway/blob/master/README.gateway.md).

## Values

| Key | Default | Description |
|---|---|---|
| `workloadKind` | `Deployment` | Manager workload kind: `Deployment` or `DaemonSet` (one pod per selected node; needs a local instance on every node) |
| `replicaCount` | `2` | Deployment replicas (every replica is self-converging; ignored for `DaemonSet`) |
| `daemonset.updateStrategy.type` | `RollingUpdate` | DaemonSet update strategy (ignored for `Deployment`) |
| `daemonset.updateStrategy.maxUnavailable` | `1` | DaemonSet RollingUpdate maxUnavailable, count or percentage string (ignored for `Deployment`) |
| `image.repository` | `ghcr.io/zjusct/rsync-gateway` | Image repository |
| `image.tag` | `""` | Image tag; empty = `.Chart.AppVersion` |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `rsync.bindAddrs` | `[":873"]` | rsync listener addresses; rendered as one repeatable `--bind` flag per entry |
| `rsync.service.type` | `LoadBalancer` | Service type fronting the rsync listeners |
| `rsync.service.port` | `873` | rsync Service port (targets the first listener) |
| `rsync.service.annotations` | `{}` | rsync Service annotations |
| `admin.addr` | `":9080"` | Admin HTTP listener address (`--admin`) |
| `admin.service.enabled` | `true` | Create the admin/metrics Service |
| `admin.service.type` | `ClusterIP` | admin Service type |
| `admin.service.ports.admin` | `9080` | admin Service port |
| `admin.service.ports.metrics` | `8080` | metrics Service port |
| `metricsBindAddress` | `":8080"` | controller-runtime metrics endpoint (`--metrics-bind-address`, `0` disables) |
| `healthProbeBindAddress` | `":8081"` | health probe endpoint (`--health-probe-bind-address`, `0` disables) |
| `gracePeriod` | `30s` | connection drain window on shutdown (`--grace-period`) |
| `terminationGracePeriodSeconds` | `60` | Pod termination grace period (kept above `gracePeriod`) |
| `resources` | `{}` | Container resources |
| `podSecurityContext` | nonroot + `RuntimeDefault` seccomp | Pod security context |
| `securityContext` | no privilege escalation, drop `ALL`, read-only root fs | Container security context |
| `nodeSelector` / `tolerations` | `{}` / `[]` | Pod scheduling (both workload kinds) |
| `affinity` | `{}` | Pod scheduling; Deployment-only — not rendered for `DaemonSet` (one pod per node is already guaranteed) |
| `rbac.serviceAccount.create` | `true` | Create the ServiceAccount |
| `rbac.serviceAccount.name` | `""` | Defaults to the release fullname |
| `rbac.clusterRole.name` | `""` | Defaults to `<fullname>-manager` |

## CRD caveat

The CRDs in `crds/` are installed by `helm install` but **not** updated by
`helm upgrade` — after a CRD change, apply them manually:

```bash
kubectl apply -f charts/rsync-gateway/crds/
```
