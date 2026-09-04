# rsync-gateway

**rsync-gateway** is a Kubernetes-native reverse proxy for the rsync
protocol. It extends [Gateway API](https://gateway-api.sigs.k8s.io/) with an
implementation-specific listener protocol and a custom route resource, so
that rsync modules can be published, routed and secured with plain
Kubernetes objects — no per-host config files, no reload webhooks.

It is built as a fork of [ustclug/rsync-proxy](#relationship-to-rsync-proxy):
the battle-tested rsync relay of rsync-proxy becomes the embedded *data
plane*, and a [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
manager becomes the *control plane* that watches the cluster and continuously
converges the relay's routing table.

## Architecture

```
                        Kubernetes API server
                                 |
        watch: GatewayClass, Gateway, GatewayConfig,
               RsyncRoute, Service, ReferenceGrant
                                 |
   +-----------------------------v------------------------------+
   |  rsync-gateway pod (each replica is identical)              |
   |                                                             |
   |  +---------------------------------------------------+      |
   |  | control plane (controller-runtime manager)        |      |
   |  |                                                   |      |
   |  |  every event -> one global rebuild:               |      |
   |  |   1. read snapshot from cache                     |      |
   |  |   2. claim GatewayClasses, bind RsyncRoutes       |      |
   |  |   3. resolve module conflicts (older route wins)  |      |
   |  |   4. build upstream table                         |      |
   |  +------------------------+--------------------------+      |
   |                           | Apply(cfg) hot-swap             |
   |  +------------------------v--------------------------+      |
   |  | data plane (embedded pkg/server, rsync-proxy)     |      |
   |  |                                                   |      |
   |  |  :873   rsync  <-- static listeners, bound once   |      |
   |  |  :9080  admin (/metrics, /status)                 |      |
   |  +------------------------+--------------------------+      |
   +----------------------------|--------------------------------+
                                | relay by module name
                +---------------v----------------+
                |   backend rsyncd Services       |
                |   <svc>.<ns>.svc:<port>         |
                +--------------------------------+
```

There is no leader election in v0: every replica watches the same resources,
computes the same desired state and applies an idempotent table to its own
data plane. Status writes are identical across replicas, so last-write-wins
is benign.

## Gateway API extension model

rsync-gateway follows the Gateway API extension conventions:

| Concept | Value |
|---|---|
| API group | `gateway.rsync.zjusct.io` (version `v1alpha1`) |
| Listener protocol | `rsync.zjusct.io/rsync` (implementation-specific `ProtocolType`) |
| GatewayClass controllerName | `gateway.rsync.zjusct.io/rsync-gateway` |
| Route resource | `RsyncRoute` |
| Parameters resource | `GatewayConfig` (cluster-scoped, referenced by `GatewayClass.spec.parametersRef`) |

### GatewayClass + GatewayConfig

A `GatewayClass` whose `spec.controllerName` is ours is *claimed*. Its
`parametersRef` must point at a `GatewayConfig` that exists and validates;
otherwise the class is reported `Accepted=False` with reason
`InvalidParameters`, and no Gateway of that class is managed.

`GatewayConfig` carries the data-plane knobs (all optional; omitted fields
mirror rsync-proxy defaults):

```yaml
apiVersion: gateway.rsync.zjusct.io/v1alpha1
kind: GatewayConfig
metadata:
  name: rsync-gateway-config
spec:
  motd: "Welcome"
  relayIdleTimeout: 600s      # GEP-2257 durations
  relayMaxDuration: 2h
  tcpKeepAlive: 45s
  dialTimeout: 5s
  perIPMaxActiveConnections: 4
  maxActiveConnections: 16    # per-upstream defaults
  maxQueuedConnections: 64
  throughputFloor:
    minBytes: 65536
    window: 60s               # default: 60s when minBytes > 0
    grace: 30s                # default: the window
```

### Gateway

Only listeners whose `protocol` is `rsync.zjusct.io/rsync` are managed;
status entries of all other listeners are left untouched. The listener port
must be part of the gateway binary's static `--bind` set (see
[limitations](#v0-limitations)).

### RsyncRoute

`RsyncRoute` is a namespaced route: `parentRefs` select Gateways
(defaults like HTTPRoute: group/kind default to Gateway, namespace defaults
to the route's), `moduleNames` lists the rsync modules to publish (defaults
to the route's own name), and `backendRefs` lists the Services receiving the
relayed traffic (the `port` field is **required**; only core Services are
supported; weight is only honored as zero/non-zero).

```yaml
apiVersion: gateway.rsync.zjusct.io/v1alpha1
kind: RsyncRoute
metadata:
  name: mirror
  namespace: rsync-gateway-samples
spec:
  parentRefs:
  - name: rsync
  moduleNames: [ubuntu, ubuntu-cd]
  backendRefs:
  - name: mirror-rsyncd
    port: 873
```

Cross-namespace backends require a `ReferenceGrant` in the backend's
namespace (`from: gateway.rsync.zjusct.io/RsyncRoute` in the route's
namespace — the group of the *referring* resource per GEP-724 —
`to: core/Service`).

## Quick start with kind

```bash
# 1. Build and load the image
make gateway-docker
kind load docker-image ghcr.io/zjusct/rsync-gateway:latest

# 2. Install the Gateway API CRDs (once per cluster)
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml

# 3. Install the chart (from a local checkout; image.tag=latest matches the
#    image loaded into kind above)
helm install rsync-gateway ./charts/rsync-gateway --set image.tag=latest

#    ... or the published chart from GHCR:
# helm install rsync-gateway oci://ghcr.io/zjusct/charts/rsync-gateway --version <version>
```

The chart deploys the manager Deployment (2 replicas by default), the rsync
LoadBalancer Service and the admin/metrics ClusterIP Service. Then create
the four user objects — a GatewayClass claiming this controller via
`controllerName` and pointing at a GatewayConfig via `parametersRef`, the
cluster-scoped GatewayConfig, a Gateway with an rsync listener, and an
RsyncRoute publishing modules to a backend Service that already exists:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: rsync-gateway
spec:
  controllerName: gateway.rsync.zjusct.io/rsync-gateway
  parametersRef:
    group: gateway.rsync.zjusct.io
    kind: GatewayConfig
    name: rsync-gateway-config
---
apiVersion: gateway.rsync.zjusct.io/v1alpha1
kind: GatewayConfig
metadata:
  name: rsync-gateway-config
spec: {}                     # all fields optional
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: rsync
  namespace: default
spec:
  gatewayClassName: rsync-gateway
  listeners:
  - name: rsync
    port: 873                # must be one of the chart's rsync.bindAddrs
    protocol: rsync.zjusct.io/rsync
---
apiVersion: gateway.rsync.zjusct.io/v1alpha1
kind: RsyncRoute
metadata:
  name: mirror
  namespace: default
spec:
  parentRefs:
  - name: rsync
  moduleNames: [ubuntu]      # defaults to the route name when omitted
  backendRefs:
  - name: mirror-rsyncd      # an existing Service exposing rsyncd
    port: 873
```

Watch it converge, then point a real rsync client at the Service fronting
the gateway replicas:

```bash
kubectl get rsyncroutes.gateway.rsync.zjusct.io -A
kubectl get gateways.gateway.networking.k8s.io -A

rsync rsync@<gateway-address>::ubuntu   # module list / browse
rsync -av rsync@<gateway-address>::ubuntu/ /tmp/out
```

The admin listener (default `:9080`) serves the upstream rsync-proxy
endpoints: `/status`, `/metrics`, `/upstream-modules`, `/telegraf`.

Route binding, module-conflict and status semantics are documented above
(see [Gateway API extension model](#gateway-api-extension-model) and
[Status and condition semantics](#status-and-condition-semantics)).

## Status and condition semantics

| Object | Condition | Meaning |
|---|---|---|
| GatewayClass | `Accepted=True/Accepted` | `parametersRef` resolves to a valid GatewayConfig |
| GatewayClass | `Accepted=False/InvalidParameters` | missing/mismatched `parametersRef`, or missing/invalid GatewayConfig |
| Gateway | `Accepted=True`, `Programmed=True` | class claimed and accepted; routes are programmed |
| Gateway listener | `Accepted=True/Accepted` | rsync listener without TLS and with parseable `allowedRoutes` |
| Gateway listener | `Accepted=False/TLSNotSupported` | listener sets TLS (not supported in v0) |
| Gateway listener | `Programmed=True/Programmed` | listener port is in the static bind set |
| Gateway listener | `Programmed=False/RestartRequired` | port not bound by this process (v0 limitation) |
| RsyncRoute (per parent) | `Accepted=True/Accepted` | route bound to the Gateway |
| RsyncRoute (per parent) | `Accepted=False/NoMatchingParent` | Gateway missing, class not claimed/accepted, no rsync listener, or `sectionName` matches none |
| RsyncRoute (per parent) | `Accepted=False/NotAllowedByListeners` | `allowedRoutes` kinds/namespaces block the route |
| RsyncRoute (per parent) | `Accepted=False/ModuleConflicted` | one of the route's modules is claimed by an older route on the same Gateway (creation timestamp, tie-break namespace/name) |
| RsyncRoute (per parent) | `ResolvedRefs=True/ResolvedRefs` | every backendRef resolves |
| RsyncRoute (per parent) | `ResolvedRefs=False/BackendNotFound` | Service missing or port absent |
| RsyncRoute (per parent) | `ResolvedRefs=False/RefNotPermitted` | cross-namespace backend without a ReferenceGrant |
| RsyncRoute (per parent) | `ResolvedRefs=False/InvalidKind` | backendRef is not a core Service |
| GatewayConfig | `Accepted=True/Accepted` | configuration applied to the data plane |
| GatewayConfig | `Accepted=False/Invalid` | data-plane apply failed (requeued with backoff) |

Module conflicts: when two routes bound to the same Gateway claim the same
module, the older route (creation timestamp, ties broken by
namespace/name) wins; the newer route's parent entry gets
`Accepted=False/ModuleConflicted` naming the winner. Conflicts are scoped
per Gateway.

## v0 limitations

* **Static bind ports.** The data plane binds its rsync listeners once at
  startup (`--bind`, repeatable). A Gateway listener on any other port is
  reported `Programmed=False/RestartRequired` instead of being bound
  dynamically.
* **Single routing domain.** All rsync listeners of all Gateways share one
  module namespace: rsync has no SNI or host header, so two Gateways serving
  the same module name would conflict. Bind the same module once per cluster
  (per routing domain).
* **TLS is not supported.** Listeners with `tls` set are rejected with
  `TLSNotSupported`.
* **Weights are ignored.** Every backend with weight > 0 joins the routing
  pool with identical treatment; `weight: 0` excludes a backend. Backend
  selection is a stable hash by client IP (upstream rsync-proxy behavior).
* **PROXY protocol is not configurable.** The data plane never sends PROXY
  protocol headers to backends in v0.
* **Hostnames are ignored.** rsync carries no host information; listener
  `hostname` fields are neither matched nor enforced.
* **No leader election.** All replicas reconcile and write statuses;
  this is safe because the computation is deterministic.

## Roadmap

* Dynamic listener provisioning (close the RestartRequired gap).
* TLS termination on rsync listeners.
* Weighted backend pools and PROXY protocol support.
* Leader election plus status write sharding for very large fleets.
* Conformance tests against the Gateway API implementation criteria.

## Relationship to rsync-proxy

rsync-gateway is a fork of [ustclug/rsync-proxy](https://github.com/ustclug/rsync-proxy)
(the Go module path stays `github.com/ustclug/rsync-proxy`). The upstream
project remains a standalone rsync reverse proxy configured by a TOML file;
its documentation is kept as [README.md](README.md) at the repository root,
prefixed with a fork notice and containing a few fork-specific additions.

On top of upstream we added only:

* `api/v1alpha1` — the RsyncRoute and GatewayConfig CRD types.
* `internal/gateway` — binding, module-conflict and status semantics.
* `internal/translate` — GatewayConfig/upstream table to `pkg/server.Config`
  translation.
* `internal/dataplane` — embeds `pkg/server` with hot-swappable tables.
* `internal/controller` — the global-rebuild controller.
* `cmd/rsync-gateway` — the combined binary.
* `charts/`, `Dockerfile.gateway`, and the `gateway*` Makefile targets.

Everything under `cmd/`, `pkg/`, `contrib/` and `test/` is upstream code and
kept behavior-compatible. The upstreamable pieces are the commits that
extended `pkg/server` for embedders (exported `Server.ApplyConfig`, added
`Server.Drain`, allowed an empty upstream table); the Kubernetes-specific
layers above are rsync-gateway-specific and not intended for upstream.

## License

MIT, same as upstream. See [LICENSE](LICENSE).
