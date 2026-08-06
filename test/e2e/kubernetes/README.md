# Kubernetes E2E Manifests

These manifests run CSI workflows against a live Kubernetes cluster. They are
intended for quick before/after checks while changing the driver.

## Volume Provisioning

The `provision/` tests verify that the CSI driver can provision a volume, bind
the PVC, and attach it to a pod. They do not require Kubernetes snapshot CRDs.

- `provision/file_backed_provision.yaml`: file-backed filesystem volume using
  `mountBackingShareName` and `fsType: ext4`.
- `provision/nfs_provision.yaml`: NFS filesystem volume using `fsType: nfs`.
- `provision/nfs_mount_backing_provision.yaml`: NFS directory-backed volume
  under a shared `mountBackingShareName`.
- `provision/block_provision.yaml`: raw block volume using
  `blockBackingShareName`.

Run all four provisioning scenarios with the helper script:

```sh
test/e2e/kubernetes/provision/provision_test.sh --kubectl "minikube kubectl --" --scenario all
```

Run one scenario:

```sh
test/e2e/kubernetes/provision/provision_test.sh --kubectl "minikube kubectl --" --scenario block --block-backing-share k8s-block-storage
```

Run the NFS mount backing scenario:

```sh
test/e2e/kubernetes/provision/provision_test.sh --kubectl "minikube kubectl --" --scenario nfs-mount-backing --nfs-backing-share k8s-nfs-storage
```

Validate the generated manifests without applying them:

```sh
test/e2e/kubernetes/provision/provision_test.sh --scenario all --render-only | minikube kubectl -- apply --dry-run=client -f -
```

Or apply a manifest directly:

```sh
minikube kubectl -- apply -f test/e2e/kubernetes/provision/file_backed_provision.yaml
minikube kubectl -- wait -n hs-file-provision-e2e --for=jsonpath='{.status.phase}'=Bound pvc/data --timeout=5m
minikube kubectl -- wait -n hs-file-provision-e2e --for=jsonpath='{.status.phase}'=Succeeded pod/file-provision-check --timeout=5m
minikube kubectl -- logs -n hs-file-provision-e2e pod/file-provision-check
```

## Snapshot/Restore

Each manifest creates its own namespace, StorageClass, VolumeSnapshotClass,
minimal RBAC, and a single Job. The Job creates a source PVC, writes a marker,
creates a VolumeSnapshot, restores a second PVC from it, and verifies the marker
from a reader pod.

### Scenarios

- `snapshot_restore/file_backed_snapshot_restore.yaml`: file-backed filesystem volume using
  `mountBackingShareName` and `fsType: ext4`.
- `snapshot_restore/nfs_share_snapshot_restore.yaml`: NFS share-backed filesystem volume.
  Restore clones into a directory inside the original source share and points
  the restored PVC at that cloned path.
- `snapshot_restore/block_snapshot_restore.yaml`: raw block volume using `blockBackingShareName`.

NFS volumes provisioned as directories with `mountBackingShareName` are not a
snapshot/restore scenario. The backend does not currently provide the required
recursive directory snapshot/clone operation.

### Run

```sh
minikube kubectl -- apply -f test/e2e/kubernetes/snapshot_restore/file_backed_snapshot_restore.yaml
minikube kubectl -- wait -n hs-file-snapshot-e2e --for=condition=Complete job/hs-file-snapshot-restore-e2e --timeout=10m
minikube kubectl -- logs -n hs-file-snapshot-e2e job/hs-file-snapshot-restore-e2e
```

For a live CSI snapshot/restore demo, use the self-contained helper script.
It embeds the manifest for the selected scenario and can run the supported
cases: `file-backed`, `nfs-share`, `block`, or
`all`.

```sh
test/e2e/kubernetes/snapshot_restore/snapshot_restore_test.sh --kubectl "kubectl" --scenario file-backed
```

To point it at a different Hammerspace backing share or objective:

```sh
test/e2e/kubernetes/snapshot_restore/snapshot_restore_test.sh \
  --kubectl "kubectl" \
  --scenario file-backed \
  --backing-share k8s-file-storage \
  --objectives keep-online
```

Additional examples:

```sh
test/e2e/kubernetes/snapshot_restore/snapshot_restore_test.sh --kubectl "kubectl" --scenario nfs-share
test/e2e/kubernetes/snapshot_restore/snapshot_restore_test.sh --kubectl "kubectl" --scenario block --block-backing-share k8s-block-storage
test/e2e/kubernetes/snapshot_restore/snapshot_restore_test.sh --kubectl "kubectl" --scenario all
```

For local minikube validation:

```sh
test/e2e/kubernetes/snapshot_restore/snapshot_restore_test.sh --kubectl "minikube kubectl --" --scenario file-backed
```

The best POC demo target is a real Tier 0 Kubernetes cluster that is already in
the Hammerspace data path, rather than minikube. Before the demo, verify that:

- Hammerspace CSI controller and node pods are healthy.
- `com.hammerspace.csi` is registered as a `CSIDriver`.
- Kubernetes snapshot CRDs and the snapshot controller are installed.
- Worker nodes can mount Hammerspace NFS v4.2 and have the filesystem tooling
  needed for the StorageClass `fsType`, currently `ext4`.
- The Hammerspace policy/share inputs in the manifest are valid for the Tier 0
  environment: `mountBackingShareName: k8s-file-storage` and
  `objectives: keep-online`.

If the snapshot CRDs are missing, install the upstream Kubernetes CSI snapshot
CRDs and snapshot controller first. Pick a version compatible with the target
cluster; `v8.5.0` supports Kubernetes 1.25+ and provides the
`snapshot.storage.k8s.io/v1` API used by these manifests:

```sh
SNAPSHOTTER_VERSION=v8.5.0
kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml"
kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml"
kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml"
kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml"
kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml"
```

For minikube, replace `kubectl` with `minikube kubectl --`.

A realistic timeline is about half a day if the Tier 0 cluster and CSI driver
are already deployed, one day if snapshot components or Hammerspace policy/share
setup still need validation, and two to three days if a fresh Tier 0 Kubernetes
environment or network/storage access has to be built from scratch.

To rerun a scenario, delete its Job and apply the manifest again:

```sh
minikube kubectl -- delete job hs-file-snapshot-restore-e2e -n hs-file-snapshot-e2e --ignore-not-found
minikube kubectl -- apply -f test/e2e/kubernetes/snapshot_restore/file_backed_snapshot_restore.yaml
```

Use the namespace and Job names from the target manifest for the other
scenarios.
