# Quickstart — Hammerspace CSI driver on Kubernetes

The shortest correct path from nothing to a running pod backed by Hammerspace
storage. Every step includes how to check it worked.

If you are already running and just want reference material, see
[`README.md`](./README.md) (parameters, examples) and [`SECRETS.md`](./SECRETS.md)
(credential handling).

**Before you start you need:**

- A Kubernetes cluster you can `kubectl apply` to, and its minor version
  (`kubectl version`). Manifests are bundled for **1.29** and **1.34–1.36**.
- A Hammerspace Anvil reachable from the cluster, and an account for the driver.
- NFS client support on every node (NFS v4.2 or v3), with the ports to the
  storage open.

---

## 1. Create the Anvil credentials

The driver reads its Anvil username, password, and endpoint from a Secret named
`com.hammerspace.csi.credentials` in `kube-system`.

Create it directly — do **not** edit and commit `example_secret.yaml`, which is a
`<PLACEHOLDER>` template:

```bash
kubectl create secret generic com.hammerspace.csi.credentials \
  --namespace kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-literal=password='<PLACEHOLDER>' \
  --from-literal=endpoint='https://<PLACEHOLDER>'
```

> Use a dedicated, least-privilege Anvil account rather than a full
> administrator — see [`SECRETS.md`](./SECRETS.md) for the role to create, plus
> GitOps-safe alternatives (Sealed Secrets, External Secrets Operator).

**Check it:**

```bash
kubectl -n kube-system get secret com.hammerspace.csi.credentials
```

---

## 2. Deploy the driver

Apply the manifest matching your cluster's minor version:

```bash
kubectl apply -f kubernetes-1.34/plugin.yaml   # or 1.35 / 1.36 / 1.29
```

**Check it** — the controller StatefulSet and the node DaemonSet should be
Running, with one node pod per worker:

```bash
kubectl -n kube-system get pods -l app=csi-provisioner
kubectl -n kube-system get pods -l app=csi-node
kubectl get csidrivers com.hammerspace.csi
```

If a pod is not Running, jump to [Troubleshooting](#troubleshooting).

---

## 3. Create a StorageClass

Pick **one** to start with. Most users want the first.

| You want | Use | `fsType` |
| --- | --- | --- |
| A shared filesystem many pods can mount at once (**start here**) | [`example_storage_class.yaml`](./example_storage_class.yaml) | `nfs` |
| A private filesystem for one pod, formatted ext4/xfs | [`example_storage_class_file_backed.yaml`](./example_storage_class_file_backed.yaml) | `ext4` / `xfs` |
| A raw block device | [`example_storage_class_block_device.yaml`](./example_storage_class_block_device.yaml) | — |

Edit the objectives/metadata to taste, then:

```bash
kubectl apply -f example_storage_class.yaml
```

**Check it:**

```bash
kubectl get storageclass hs-storage
```

See [README — StorageClass parameters](./README.md#storageclass-parameters) for
what every parameter does.

---

## 4. Create a volume and use it

A PersistentVolumeClaim asks for storage; a pod mounts it.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: myfilesystem
  namespace: default
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 100Gi
  storageClassName: hs-storage
---
apiVersion: v1
kind: Pod
metadata:
  name: my-app
  namespace: default
spec:
  containers:
    - name: my-app
      image: alpine
      command: ["sh", "-c", "echo hello > /data/test.txt && cat /data/test.txt && sleep 3600"]
      volumeMounts:
        - mountPath: /data
          name: data-dir
  volumes:
    - name: data-dir
      persistentVolumeClaim:
        claimName: myfilesystem
```

**Check it** — the PVC should reach `Bound` and the pod `Running`:

```bash
kubectl get pvc myfilesystem      # STATUS should be Bound
kubectl get pod my-app            # STATUS should be Running
kubectl logs my-app               # should print: hello
```

A share now exists on the Anvil, named per the storage class's
`volumeNameFormat` (`csi-pvc-<uuid>` by default).

That's the full loop. Everything below is optional.

---

## Common operations

### Grow a volume

The bundled storage classes set `allowVolumeExpansion: true`. Edit the claim's
requested size — shrinking is not supported:

```bash
kubectl patch pvc myfilesystem -p '{"spec":{"resources":{"requests":{"storage":"200Gi"}}}}'
kubectl get pvc myfilesystem      # CAPACITY updates once the resize completes
```

For **file-backed and block** volumes the filesystem is grown on the node, which
currently requires restarting the pod using the volume. Share-backed (`nfs`)
volumes need no restart.

### Snapshot a volume and restore it

Install the [external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter)
CRDs and controller (they are not part of Kubernetes itself), then:

```bash
kubectl apply -f example_snapshot_class.yaml
kubectl apply -f example_snapshot.yaml
kubectl get volumesnapshot data-snapshot    # wait for READYTOUSE=true
```

[`example_snapshot.yaml`](./example_snapshot.yaml) both snapshots `myfilesystem`
and restores it into a new, independent PVC via `dataSource`.

> Restoring a snapshot creates a **new** volume. Cloning an existing PVC
> directly (PVC-to-PVC `dataSource`) is not supported by this driver.

### Schedule pods onto data-portal nodes

The node plugin labels nodes with
`topology.csi.hammerspace.com/is-data-portal`, so you can prefer or require
data-portal nodes with normal `nodeAffinity`. See
[README — Example Topology Usage](./README.md#example-topology-usage).

---

## Troubleshooting

Work down this list; each step narrows where the problem is.

**PVC stuck in `Pending`**

```bash
kubectl describe pvc <name>                                    # events explain why
kubectl -n kube-system logs statefulset/csi-provisioner -c hs-csi-plugin-controller
```

Common causes: bad Anvil credentials or endpoint; the storage class's
`mountBackingShareName`/`blockBackingShareName` share cannot be created;
a requested objective does not exist on the cluster; or a file-backed volume
below the minimum size (xfs 300 MiB, ext4 20 MiB).

**Pod stuck in `ContainerCreating`**

```bash
kubectl describe pod <name>                                    # look for mount errors
kubectl -n kube-system logs daemonset/csi-node -c hs-csi-plugin-node
```

Usually the node cannot reach a data portal, or NFS is unavailable on the node.
Confirm the node has NFS client support and that the portal IP/FQDN resolves and
is reachable from the node.

**Authentication failures in the controller log**

Re-check the Secret's three keys and that the account can log in to the Anvil:

```bash
kubectl -n kube-system get secret com.hammerspace.csi.credentials \
  -o jsonpath='{.data.endpoint}' | base64 -d
```

Credentials are injected as environment variables at pod start, so after
changing the Secret you must restart the driver:

```bash
kubectl -n kube-system rollout restart statefulset/csi-provisioner
kubectl -n kube-system rollout restart daemonset/csi-node
```

**Snapshot never becomes ready**

Confirm the external-snapshotter CRDs and controller are installed
(`kubectl get crd volumesnapshots.snapshot.storage.k8s.io`), then
`kubectl describe volumesnapshot <name>`.

**Still stuck?** The driver logs at debug level by default, so the container
logs above already carry the detail — including every Anvil REST call it makes.
For metrics and tracing, see [`docs/observability.md`](../../docs/observability.md).
