# Migrating from the raw manifests to the Helm chart

This chart's default values render the **same objects, with the same
names and namespace**, as the raw
[`deploy/kubernetes/kubernetes-1.*/plugin.yaml`](../../deploy/kubernetes/)
manifests: `CSIDriver/com.hammerspace.csi`, `StatefulSet/csi-provisioner`,
`DaemonSet/csi-node`, their ServiceAccounts, ClusterRoles/Bindings, the
`Service/csi-provisioner` headless service, and `ConfigMap/csi-env-config`,
all in `kube-system`. That's intentional — it makes migration a drop-in
swap of *how* the driver is deployed, not *what* gets deployed.

## Do not run both at once

**Never `kubectl apply` the raw manifests and `helm install`/`helm upgrade`
this chart against the same cluster at the same time.** They both claim
ownership of the same objects — most importantly the cluster-scoped,
singleton `CSIDriver/com.hammerspace.csi` — and only one management path can
be authoritative:

- If the raw-manifest objects already exist and you `helm install`, Helm
  will refuse: it doesn't own them (no Helm release metadata on them) and
  errors with something like `Unable to continue with install: CSIDriver
  "com.hammerspace.csi" in namespace "" exists and cannot be imported into
  the current release`.
- If you install the chart first and later someone (or an old script, a
  stale GitOps sync, etc.) `kubectl apply`s the raw `plugin.yaml`, `kubectl`
  doesn't know about Helm either — it will happily overwrite the
  Helm-managed fields. The next `helm upgrade` then fights that drift, and
  the two paths can flap the same objects back and forth.

Pick one path per cluster. This guide assumes you're moving from the raw
manifests to the chart.

## Migration steps

1. **Record any customizations** you've made to the raw manifests (a
   non-default image tag, `TLS_VERIFY`, `LOG_LEVEL`, resource
   requests/limits, `kubeletDir`, etc.) so you can carry them into
   `values.yaml` or `--set` flags — see the [values reference](./README.md)
   for the equivalent chart value for each.

2. **Leave the credentials Secret and your StorageClasses/VolumeSnapshotClasses
   alone.** They aren't part of `plugin.yaml` and the next step doesn't touch
   them. The chart's default `hammerspace.existingSecret` already points at
   the conventional Secret name (`com.hammerspace.csi.credentials`), so an
   existing Secret with that name is picked up automatically.

3. **Delete the raw-manifest driver objects**, either with the original file:

   ```bash
   kubectl delete -f deploy/kubernetes/kubernetes-1.36/plugin.yaml
   ```

   or, if you no longer have that exact manifest to hand, by kind/name
   directly:

   ```bash
   kubectl -n kube-system delete statefulset csi-provisioner
   kubectl -n kube-system delete daemonset csi-node
   kubectl -n kube-system delete service csi-provisioner
   kubectl -n kube-system delete configmap csi-env-config
   kubectl -n kube-system delete serviceaccount csi-provisioner csi-node
   kubectl -n kube-system delete clusterrolebinding csi-provisioner csi-node csi-provisioner-volumeattachment-status-binding
   kubectl -n kube-system delete clusterrole csi-provisioner csi-node
   kubectl delete csidriver com.hammerspace.csi
   ```

   This is a brief control-plane gap, not a data-path one: kubelet stops
   being able to route *new* mount/unmount/provision requests to the driver
   until it's redeployed, but filesystems already mounted on nodes are
   unaffected (the driver isn't in the data path for an existing mount).

4. **Verify the cluster is clean** before installing:

   ```bash
   kubectl get csidriver com.hammerspace.csi        # should be NotFound
   kubectl -n kube-system get statefulset csi-provisioner  # should be NotFound
   kubectl -n kube-system get daemonset csi-node            # should be NotFound
   ```

5. **Install the chart** with the values that reproduce your prior
   customizations (defaults alone reproduce a stock `plugin.yaml` install):

   ```bash
   helm install hs charts/hammerspace-csi \
     --namespace kube-system \
     --set hammerspace.endpoint=https://<anvil> \
     --set hammerspace.existingSecret=com.hammerspace.csi.credentials
   ```

6. **Verify.** `com.hammerspace.csi` is recreated with the same name, so
   existing PersistentVolumes, PersistentVolumeClaims, and StorageClasses
   that reference it keep working without recreation — confirm the new
   controller/node pods are `Running` and that an existing PVC still mounts
   in a test pod.

7. **Manage the driver exclusively via `helm upgrade` from here on**, and
   stop applying `deploy/kubernetes/kubernetes-1.*/plugin.yaml` to this
   cluster. If you use GitOps, remove the raw manifest from whatever synced
   it so it can't silently reapply.

## See also

- [`README.md`](./README.md) — full values reference, the immutability
  caveats for `CSIDriver.spec.attachRequired`/`fsGroupPolicy`, and the
  metrics/`ServiceMonitor` caveat.
- [`deploy/kubernetes/README.md`](../../deploy/kubernetes/README.md) — the
  manifest this chart replaces.
