# hammerspace-csi

Hammerspace CSI driver for Kubernetes

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Hammerspace |  |  |

## Source Code

* <https://github.com/hammer-space/csi-plugin>

## Install

The driver authenticates to the Anvil management endpoint with a Secret. Create
it first (see [`deploy/kubernetes/SECRETS.md`](../../deploy/kubernetes/SECRETS.md)
for least-privilege guidance):

```bash
kubectl create secret generic com.hammerspace.csi.credentials \
  --namespace kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-literal=password='<PLACEHOLDER>' \
  --from-literal=endpoint='https://<anvil>'
```

Then install the chart:

```bash
helm install hs charts/hammerspace-csi \
  --namespace kube-system \
  --set hammerspace.endpoint=https://<anvil> \
  --set hammerspace.existingSecret=com.hammerspace.csi.credentials
```

> **Do not run this chart alongside the raw `deploy/kubernetes/kubernetes-1.*`
> manifests on the same cluster.** Both own the cluster-singleton
> `CSIDriver/com.hammerspace.csi` object and will fight each other. Migrating
> from the raw manifests? See [`MIGRATION.md`](./MIGRATION.md) first.

## Upgrade

```bash
helm upgrade hs charts/hammerspace-csi \
  --namespace kube-system \
  --reuse-values \
  --set image.tag=v1.3.1
```

`helm upgrade` will **fail** if the new values change an immutable field on an
object that already exists in the cluster — see
[Immutability caveats](#immutability-caveats) below for which fields those are
and how to work around it.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| commonAnnotations | object | `{}` | Extra annotations applied to every resource this chart renders. |
| commonLabels | object | `{}` | Extra labels applied to every resource this chart renders. |
| controller.affinity | object | `{}` | `affinity` for the controller StatefulSet. |
| controller.attacher.enabled | bool | `true` | Run the `csi-attacher` sidecar on the controller. PRESERVES TODAY'S BEHAVIOR (on), matching `csiDriver.attachRequired` defaulting to `true`. Set `false` together with `csiDriver.attachRequired: false` to drop the no-op attacher (the driver has no volume-attach step). |
| controller.nodeSelector | object | `{}` | `nodeSelector` for the controller StatefulSet. |
| controller.replicaCount | int | `1` | Replica count for the controller StatefulSet. |
| controller.resources | object | `{}` | Resource requests/limits for the controller's driver container. Empty preserves today's behavior (no requests/limits set). |
| controller.sidecars.attacher.image | string | `"registry.k8s.io/sig-storage/csi-attacher"` |  |
| controller.sidecars.attacher.tag | string | `"v4.4.0"` |  |
| controller.sidecars.provisioner.image | string | `"registry.k8s.io/sig-storage/csi-provisioner"` |  |
| controller.sidecars.provisioner.tag | string | `"v3.6.0"` |  |
| controller.sidecars.resizer.image | string | `"registry.k8s.io/sig-storage/csi-resizer"` |  |
| controller.sidecars.resizer.tag | string | `"v1.10.1"` |  |
| controller.sidecars.snapshotter.image | string | `"registry.k8s.io/sig-storage/csi-snapshotter"` |  |
| controller.sidecars.snapshotter.tag | string | `"v6.2.1"` |  |
| controller.tolerations | list | `[]` | `tolerations` for the controller StatefulSet. |
| csiDriver.attachRequired | string | `nil` | `CSIDriver.spec.attachRequired`. `null` omits the field (today's behavior; Kubernetes then defaults it to `true`). IMMUTABLE on the CSIDriver object once created — changing this on an existing release requires deleting the `CSIDriver/com.hammerspace.csi` object first. |
| csiDriver.fsGroupPolicy | string | `""` | `CSIDriver.spec.fsGroupPolicy`. Empty omits the field (today's behavior). IMMUTABLE on the CSIDriver object once created — changing this on an existing release requires deleting the `CSIDriver/com.hammerspace.csi` object first. |
| csiDriver.podInfoOnMount | bool | `true` | `CSIDriver.spec.podInfoOnMount`. |
| csiDriver.requiresRepublish | bool | `true` | `CSIDriver.spec.requiresRepublish`. |
| csiDriver.storageCapacity | bool | `true` | `CSIDriver.spec.storageCapacity`. |
| envConfig.mountCheckTimeout | string | `"30s"` | Timeout for the driver's mount-check probe. |
| envConfig.unmountRetryCount | string | `"5"` | Number of unmount retries before the driver gives up. |
| envConfig.unmountRetryInterval | string | `"1s"` | Delay between unmount retries. |
| freezer.enabled | bool | `true` | Grant the node ServiceAccount `pods/exec` (needed for filesystem-freeze snapshot support). PRESERVES TODAY'S BEHAVIOR (on). Set `false` to drop the grant if freeze/snapshot isn't used. |
| fullnameOverride | string | `""` | Override the fully-qualified app name used to build resource names. Defaults to the chart name. |
| hammerspace.dataPortalMountPrefix | string | `""` | Optional prefix prepended to Data Portal mount paths. |
| hammerspace.endpoint | string | `""` | Anvil management endpoint URL, e.g. `https://anvil.example.com`. Required unless supplied via the `endpoint` key of `hammerspace.existingSecret`. |
| hammerspace.existingSecret | string | `"com.hammerspace.csi.credentials"` | Name of a pre-existing Secret holding `username`, `password`, and `endpoint` keys for Anvil auth. Used as-is unless `hammerspace.secret.create` is `true`. |
| hammerspace.logLevel | string | `""` | Driver log level (e.g. `DEBUG`, `INFO`). Empty uses the driver's built-in default. |
| hammerspace.secret.create | bool | `false` | Render a Secret from `username`/`password`/`endpoint` below instead of using `existingSecret`. Dev/test convenience only — prefer an externally-managed Secret in production. |
| hammerspace.secret.password | string | `""` | Anvil password. Only used when `hammerspace.secret.create` is `true`. |
| hammerspace.secret.username | string | `""` | Anvil username. Only used when `hammerspace.secret.create` is `true`. |
| hammerspace.tlsVerify | bool | `false` | Verify the Anvil TLS certificate. PRESERVES TODAY'S BEHAVIOR (off). Set `true` (and ensure the Anvil cert is trusted) to harden. |
| image.pullPolicy | string | `"Always"` | Image pull policy for the driver image. |
| image.pullSecrets | list | `[]` | `imagePullSecrets` for the driver image. |
| image.repository | string | `"hammerspaceinc/csi-plugin"` | Driver image repository. |
| image.tag | string | `""` | Driver image tag. Defaults to `.Chart.AppVersion` when empty. |
| metrics.controllerPort | int | `9090` | `hostPort`/listen port for controller metrics. |
| metrics.enabled | bool | `true` | Expose Prometheus metrics via a `hostPort` on the controller and node containers. |
| metrics.nodePort | int | `9091` | `hostPort`/listen port for node metrics. |
| metrics.otlpEndpoint | string | `""` | OTLP collector endpoint, used when `metrics.tracesExporter` is `otlp`. |
| metrics.serviceMonitor.enabled | bool | `false` | Render a Prometheus Operator `ServiceMonitor`. NOTE: the driver exposes metrics via `hostPort` only (no metrics Service), so this alone will not let the ServiceMonitor discover any targets — see the README's metrics/ServiceMonitor caveat before enabling. |
| metrics.tracesExporter | string | `"none"` | OpenTelemetry traces exporter (`none`, `console`, or `otlp`). |
| nameOverride | string | `""` | Override the chart name used to build resource names (see `fullnameOverride`). |
| namespaceOverride | string | `"kube-system"` | Namespace the driver's resources are installed into. Set to `""` to install into `.Release.Namespace` instead. |
| node.kubeletDir | string | `"/var/lib/kubelet"` | Path to the kubelet's root directory on each node. Change only if kubelet is configured with a non-default `--root-dir`. |
| node.registrar.image | string | `"registry.k8s.io/sig-storage/csi-node-driver-registrar"` |  |
| node.registrar.tag | string | `"v2.9.0"` |  |
| node.resources | object | `{}` | Resource requests/limits for the node driver container. Empty preserves today's behavior (no requests/limits set). |
| node.tolerations | list | `[]` | `tolerations` for the node DaemonSet. |
| priorityClassName | string | `""` | `priorityClassName` applied to the controller and node pods. |
| rbac.create | bool | `true` | Create the ClusterRoles/ClusterRoleBindings the driver needs. |
| securityContext.seccompProfile.enabled | bool | `false` | Set `securityContext.seccompProfile.type: RuntimeDefault` on driver containers. Off by default to preserve today's behavior; opt-in hardening. |
| serviceAccount.controllerName | string | `"csi-provisioner"` | Name of the controller ServiceAccount. |
| serviceAccount.create | bool | `true` | Create the controller and node ServiceAccounts. |
| serviceAccount.nodeName | string | `"csi-node"` | Name of the node ServiceAccount. |
| snapshotClass.deletionPolicy | string | `"Delete"` | `deletionPolicy` for the rendered VolumeSnapshotClass (`Delete` or `Retain`). |
| snapshotClass.enabled | bool | `false` | Render a VolumeSnapshotClass. |
| snapshotClass.parameters | object | `{}` | Extra `parameters` for the rendered VolumeSnapshotClass. |
| storageClasses | list | `[]` | Extra StorageClasses to render. See `ci/block-values.yaml` for an example entry. |

## Immutability caveats

Some fields this chart renders are immutable on Kubernetes once the object is
created. Changing the corresponding value and running `helm upgrade` will
**error** (Kubernetes rejects the patch) rather than silently apply — you must
delete the object first, then re-run `helm upgrade` (or `helm install`) to
recreate it.

| Value | Immutable field | Object |
| --- | --- | --- |
| `csiDriver.attachRequired` | `spec.attachRequired` | `CSIDriver/com.hammerspace.csi` |
| `csiDriver.fsGroupPolicy` | `spec.fsGroupPolicy` | `CSIDriver/com.hammerspace.csi` |

```bash
kubectl delete csidriver com.hammerspace.csi
helm upgrade hs charts/hammerspace-csi --namespace kube-system --reuse-values \
  --set csiDriver.attachRequired=false
```

Deleting the `CSIDriver` object briefly stops kubelet from routing new mount
requests to the driver; existing mounted volumes are unaffected. Pods that try
to mount a Hammerspace volume during the gap will retry once the object comes
back.

The controller StatefulSet's and node DaemonSet's `spec.selector.matchLabels`
are also immutable. This chart never changes them via values, so they're only
a concern for a manually-forked chart — if you do fork the templates, don't
parameterize `selector.matchLabels`, or plan on a delete/recreate of the
StatefulSet/DaemonSet the same way as the `CSIDriver`.

## Metrics and the `ServiceMonitor`

The driver exposes Prometheus metrics via a **`hostPort`** on each container
(controller `:9090`, node `:9091`) — it does **not** create a metrics
`Service`. Setting `metrics.serviceMonitor.enabled=true` renders a Prometheus
Operator `ServiceMonitor`, but the `ServiceMonitor`'s `endpoints[].port`
selects a named port on a `Service`, and none exists for metrics, so **no
targets will be discovered**. This toggle is opt-in and currently incomplete
on its own.

For working Prometheus scraping today, use the scrape-config approach in
[`deploy/monitoring/`](../../deploy/monitoring/README.md), which targets the
`hostPort`s directly.

## Defaults preserve today's behavior

This chart's default values reproduce the behavior of the raw
`deploy/kubernetes/kubernetes-1.*` manifests: TLS verification off, the
`csi-attacher` sidecar on, no seccomp profile, and no CPU/memory
requests/limits. Installing with defaults is a drop-in replacement for the
manifest-based deployment.

Opt-in hardening, once you've validated it against your cluster:

| Value | Effect |
| --- | --- |
| `hammerspace.tlsVerify` | Verify the Anvil TLS certificate. |
| `controller.attacher.enabled` + `csiDriver.attachRequired` | Drop the no-op `csi-attacher` sidecar (set both to `false` together). |
| `securityContext.seccompProfile.enabled` | Set `RuntimeDefault` seccomp on driver containers. |
| `controller.resources` / `node.resources` | Set CPU/memory requests and limits on the driver containers. |
| `freezer.enabled` | Set `false` to drop the node ServiceAccount's `pods/exec` grant if you don't use freeze/snapshot. |
