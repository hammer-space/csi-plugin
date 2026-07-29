# Kubernetes Installation/Configuration

This directory contains example manifests for deploying the plugin to Kubernetes.

**New here? Start with [`QUICKSTART.md`](./QUICKSTART.md)** — credentials, driver
deployment, and a first working volume, with a check after every step.

The examples are meant to be customized and applied individually, not all at
once (`example_secret.yaml` is a placeholder template, and `example_snapshot.yaml`
expects an existing PVC).

| File | What it is |
| --- | --- |
| [`example_secret.yaml`](./example_secret.yaml) | Anvil credentials template — see [`SECRETS.md`](./SECRETS.md) |
| [`example_storage_class.yaml`](./example_storage_class.yaml) | Shared filesystem (NFS) volumes — the usual starting point |
| [`example_storage_class_file_backed.yaml`](./example_storage_class_file_backed.yaml) | File-backed ext4/xfs volumes |
| [`example_storage_class_block_device.yaml`](./example_storage_class_block_device.yaml) | Raw block volumes |
| [`example_fqdn_storage_class.yaml`](./example_fqdn_storage_class.yaml) | FQDN-addressed storage, with a runnable PVC + Pod |
| [`example_snapshot_class.yaml`](./example_snapshot_class.yaml) | VolumeSnapshotClass |
| [`example_snapshot.yaml`](./example_snapshot.yaml) | Snapshot a volume and restore it into a new one |
| `kubernetes-<major>.<minor>/plugin.yaml` | The driver itself |

## Anvil credentials

The driver authenticates to Anvil with an administrative user stored in a Secret
named `com.hammerspace.csi.credentials`. **`example_secret.yaml` is a template
full of `<PLACEHOLDER>` values — do not `kubectl apply` it as-is, and do not
commit a filled-in copy.** The recommended path is to create the Secret
imperatively:

```bash
kubectl create secret generic com.hammerspace.csi.credentials \
  --namespace kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-literal=password='<PLACEHOLDER>' \
  --from-literal=endpoint='https://<PLACEHOLDER>'
```

For a least-privilege Anvil service account, least-privilege Kubernetes RBAC,
GitOps-safe storage (Sealed Secrets), and external secret managers (External
Secrets Operator / Secrets Store CSI Driver), see [`SECRETS.md`](./SECRETS.md).


## Plugin Updates

To deploy updates to the plugin, simply change the image tag ```hammerspaceinc/csi-plugin``` of the StatefulSet and DaemonSet to the new plugin image, make any other update to environment variables, and reapply the yaml files.

If you are using ```hammerspaceinc/csi-plugin:latest``` you must delete all the existing plugin pods so the new image is pulled and the pods are recreated automatically. Otherwise, changing the image tag will trigger an update to occur. Ex. ```hammerspaceinc/csi-plugin:v1.2.9``` -> ```hammerspaceinc/csi-plugin:v1.3.0```
## Kubernetes Cluster Prerequisites
Kubernetes documentation for CSI support can be found [here](https://kubernetes-csi.github.io/)

* There is no single blanket minimum Kubernetes version — the required version is
  specific to the manifest you deploy. Per-minor manifests live under
  `deploy/kubernetes/kubernetes-<major>.<minor>/plugin.yaml`, and each targets that
  Kubernetes minor, so apply the one matching your `kubectl`/cluster version.
  Bundled: **1.29** and **1.34 / 1.35 / 1.36**. **1.34–1.36 are the currently
  supported + validated set**; those three manifests include the observability
  wiring (a host-networked metrics port and OTel env vars) — see
  [`docs/observability.md`](../../docs/observability.md) and
  [`deploy/monitoring/README.md`](../monitoring/README.md). Manifests for
  Kubernetes minors older than 1.29 have been moved to [`archive/`](./archive/);
  they are unsupported and pin their contemporary driver image. For a minor with
  no bundled manifest, copy the nearest lower version and bump sidecar tags.
* BlockVolume support requires kubelet has the [feature gates](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/) BlockVolume and CSIBlockVolume set to true.
    Example in /var/lib/kubelet/config.yaml
    ```yaml
    ...
    featureGates:
      BlockVolume: true
      CSIBlockVolume: true
      VolumeSnapshotDataSource: true
    ...
    ```
* Topology support requires v1.14+ ``Topology`` and ``CSINodeInfo``
    Example in /var/lib/kubelet/config.yaml
    ```yaml
    ...
    featureGates:
      CSINodeInfo: true  # On by default in kubernetes 1.14+
      Topology: true
    ...
    ```
* Volume expansion support requires v1.14+ ``ExpandCSIVolumes`` and ``ExpandInUsePersistentVolumes``
    Example in /var/lib/kubelet/config.yaml
    ```yaml
    ...
    featureGates:
      ExpandCSIVolumes: true
      ExpandInUsePersistentVolumes: true
    ...
    ```
* VolumeSnapshot support requires the ``VolumeSnapshotDataSource`` feature flag
    Example in /var/lib/kubelet/config.yaml
    ```yaml
    ...
    featureGates:
      VolumeSnapshotDataSource: true
    ...
* Each host should have support for NFS v4.2 or v3 with the relevant network ports open between the host and storage

### NOTE on Google Kubernetes Engine
GKE does not allow the creation of ClusterRoles
that are more powerful than the given user. An insecure workaround to this is
to give the user creating the role cluster-admin privileges.

```bash
kubectl create clusterrolebinding i-am-root --clusterrole=cluster-admin --user=<current user>
```

## Choosing a volume type

The `fsType` parameter decides how a volume is provisioned. Everything else
follows from that choice.

| | **Share-backed** (`fsType: nfs`, the default) | **File-backed** (`fsType: ext4` or `xfs`) | **Block** (`volumeMode: Block`) |
| --- | --- | --- | --- |
| What it is | A Hammerspace share, mounted over NFS | A file on a backing share, formatted and loop-mounted | A file on a backing share, exposed as a raw device |
| Shared between pods | Yes — `ReadWriteMany` | No — one pod at a time | No — one pod at a time |
| Typical use | Shared data, the common case | A pod that needs POSIX/local filesystem semantics | Databases and apps that manage their own format |
| Needs a backing share | No | Yes — `mountBackingShareName` | Yes — `blockBackingShareName` |
| Expansion | Online | Requires restarting the pod | Requires restarting the pod |
| Minimum size | — | xfs 300 MiB, ext4 20 MiB | — |
| At high volume counts | One share per volume, so the Anvil management API is the limit | **Scales to thousands** | **Scales to thousands** |
| Example | [`example_storage_class.yaml`](./example_storage_class.yaml) | [`example_storage_class_file_backed.yaml`](./example_storage_class_file_backed.yaml) | [`example_storage_class_block_device.yaml`](./example_storage_class_block_device.yaml) |

If you are not sure, use share-backed (`fsType: nfs`) — unless you are
provisioning very large numbers of volumes, where file-backed scales better
because the volumes are files inside a single share rather than a share each.
See [`docs/file-backed-performance.md`](../../docs/file-backed-performance.md).
`ext3` is not supported.

## StorageClass parameters

All parameters are optional and are set under `parameters:` in a StorageClass.
Values are always strings (quote numbers and booleans).

| Parameter | Default | Description |
| --- | --- | --- |
| `fsType` | `nfs` | `nfs` for share-backed volumes; `ext4` or `xfs` for file-backed. Selects the volume type — see above. |
| `objectives` | none | Comma-separated Hammerspace objectives to apply in addition to the cluster defaults. Ex: `"keep-online,archive"` |
| `objectiveTarget` | `share` | File-backed volumes only. `share` applies objectives to the backing share only and provisions fastest; `file` or `both` also applies them per file, for per-volume placement policy. |
| `mountBackingShareName` | none | File-backed volumes: the share that holds the backing files. Auto-created if missing; never deleted by the driver. |
| `blockBackingShareName` | none | Block volumes: as above, for raw block backing files. |
| `exportOptions` | none | NFS export rules as `;`-separated `<subnet>,<accessPermissions>,<rootSquash>` triples. Ex: `"*,RW,false; 172.16.0.0/20,RO,true"` |
| `volumeNameFormat` | `csi-%s` | Naming pattern for provisioned shares. Must contain `%s` exactly once and no `/`. |
| `additionalMetadataTags` | none | Comma-separated `key=value` metadata applied to created shares and files. |
| `comment` | `Created by CSI driver` | Share description on the Anvil. Max 255 characters. |
| `deleteDelay` | `-1` | Milliseconds Hammerspace waits before actually deleting a share after the volume is deleted. `-1` uses the cluster default (86400000 = 24h); `0` purges immediately. |
| `cacheEnabled` | `false` | Enable Hammerspace caching for the share. |
| `fqdn` | none | Address storage via this FQDN instead of a data-portal IP. Must resolve from the controller and node pods, or it is ignored with a warning. See [the FQDN example](./example_fqdn_storage_class.yaml). |

Storage-class fields outside `parameters:` behave as they do for any CSI driver
— `reclaimPolicy`, `volumeBindingMode`, `allowVolumeExpansion`, and
`mountOptions` (used for the NFS mount).

## Example Usage

### Create a Filesystem Volume
Example PVC

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
```

### Create an Application Using the Filesystem Volume
Example Pod
```yaml
kind: Pod
apiVersion: v1
metadata:
  name: my-app
spec:
  containers:
    - name: my-app
      image: alpine
      volumeMounts:
      - mountPath: "/data"
        name: data-dir
      command: [ "ls", "-al", "/data" ]
  volumes:
    - name: data-dir
      persistentVolumeClaim:
        claimName: myfilesystem
```

### Create a Raw Volume
Example PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mydevice
  namespace: default
spec:
  volumeMode: Block
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 100Gi
  storageClassName: hs-storage
```

### Create an Application Using the Raw Volume
Example Pod
```yaml
kind: Pod
apiVersion: v1
metadata:
  name: my-app
spec:
  containers:
    - name: my-app
      image: alpine
      volumeDevices:
      - devicePath: "/dev/xvda"
        name: data-dir
      command: [ "stat", "/dev/xvda" ]
  volumes:
    - name: data-dir
      persistentVolumeClaim:
        claimName: mydevice
```

### Create a Snapshot

Requires the [external-snapshotter](https://github.com/kubernetes-csi/external-snapshotter)
CRDs and controller, plus a VolumeSnapshotClass
([`example_snapshot_class.yaml`](./example_snapshot_class.yaml)).

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: data-snapshot
spec:
  volumeSnapshotClassName: hs-snapshots
  source:
    persistentVolumeClaimName: mydevice
```

The driver snapshots share-backed volumes with a share snapshot and file-backed
volumes with a file snapshot (freezing the source filesystem briefly for a
crash-consistent image); there is nothing to configure.

### Restore a Snapshot

Reference the snapshot as a PVC `dataSource`. This provisions a **new**,
independent volume — see [`example_snapshot.yaml`](./example_snapshot.yaml) for
the snapshot and restore together.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mydevice-restored
spec:
  accessModes:
  - ReadWriteMany
  resources:
    requests:
      storage: 100Gi
  storageClassName: hs-storage
  dataSource:
    name: data-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
```

Cloning a PVC directly (PVC-to-PVC `dataSource`) is not supported.

## Example Topology Usage

### Create an Application Using the Filesystem Volume, only schedule to nodes that are data-portals
Example Pod
```yaml
kind: Pod
apiVersion: v1
metadata:
  name: my-app
spec:
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: topology.csi.hammerspace.com/is-data-portal
            operator: In
            values:
            - "true"
  containers:
    - name: my-app
      image: alpine
      volumeMounts:
      - mountPath: "/data"
        name: data-dir
      command: [ "ls", "-al", "/data" ]
  volumes:
    - name: data-dir
      persistentVolumeClaim:
        claimName: myfilesystem
```
### Create an Application Using the Filesystem Volume, *prefer* scheduling to nodes that are data-portals
Example Pod
```yaml
kind: Pod
apiVersion: v1
metadata:
  name: my-app
spec:
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: topology.csi.hammerspace.com/is-data-portal
            operator: In
            values:
            - "true"
            - "false"
      preferredDuringSchedulingIgnoredDuringExecution:
      - preference:
          matchExpressions:
            - key: topology.csi.hammerspace.com/is-data-portal
              operator: In
              values:
              - "true"
        weight: 1
  containers:
    - name: my-app
      image: alpine
      volumeMounts:
      - mountPath: "/data"
        name: data-dir
      command: [ "ls", "-al", "/data" ]
  volumes:
    - name: data-dir
      persistentVolumeClaim:
        claimName: myfilesystem
```
### This example demonstrates how to use Fully Qualified Domain Names (FQDN) with the Hammerspace CSI Plugin for file-backed storage.

Example File-Backed StorageClass
The following StorageClass definition shows how to configure a file-backed filesystem volume with an FQDN parameter.

```yaml
# Example File-Backed StorageClass
# Define a StorageClass for file-backed Filesystem volumes with the Hammerspace CSI Plugin
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: hs-file-backed
provisioner: com.hammerspace.csi
parameters:
  fsType: "ext4"
  mountBackingShareName: k8s-file-storage
  objectives: "keep-online"
  volumeNameFormat: "csi-%s"
  additionalMetadataTags: "storageClassName=hs-file-backed,fsType=file"
  comment: "My share description"
  cacheEnabled: "true"
  fqdn: "storage-server.example.com"
allowVolumeExpansion: true
```

Configuring CoreDNS for FQDN Support
To use an FQDN, update your Kubernetes CoreDNS configuration. This ensures the FQDN resolves correctly within your cluster.

### Steps to Update CoreDNS
* Edit the CoreDNS ConfigMap
Modify the Corefile in the kube-system namespace to include the desired FQDN mapping under the hosts plugin section.

```json
{
    "Corefile": ".:53 {
        log
        errors
        health {
            lameduck 5s
        }
        ready
        kubernetes cluster.local in-addr.arpa ip6.arpa {
            pods insecure
            fallthrough in-addr.arpa ip6.arpa
            ttl 30
        }
        prometheus :9153
        hosts {
            <some-ip> storage-server.example.com
            192.168.49.1 host.minikube.internal
            fallthrough
        }
        forward . /etc/resolv.conf {
            max_concurrent 1000
        }
        cache 30
        loop
        reload
        loadbalance
    }
}

```
Apply the Updated ConfigMap
Save your changes and apply the updated ConfigMap:

```bash
kubectl apply -f <updated-configmap-file>
Restart CoreDNS
```

Roll out a restart of the CoreDNS deployment to apply the new configuration:
```bash
kubectl -n kube-system rollout restart deployment coredns
```

- Verifying the Configuration
Confirm that the StorageClass is correctly applied:
```bash
kubectl get storageclass hs-file-backed
```

Verify the CoreDNS configuration using the following command:
```bash
kubectl -n kube-system logs -l k8s-app=kube-dns
```

Test FQDN resolution within your cluster:

```bash
nslookup storage-server.example.com
```
By following these steps, you can configure Kubernetes to support FQDN in your StorageClass YAML, ensuring smooth operations with Hammerspace CSI.