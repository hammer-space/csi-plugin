# Volume Imports

This guide describes how to import existing Hammerspace shares or files into Kubernetes using static PersistentVolumes. This is particularly useful for:
- **Disaster recovery** workflows,
- **Sharing storage across multiple Kubernetes clusters**,
- **Retaining data after PVC deletion**.

> This guide assumes basic understanding of Kubernetes [PersistentVolumes and PersistentVolumeClaims](https://kubernetes.io/docs/concepts/storage/persistent-volumes/).

## Prerequisites

- Hammerspace CSI driver (`com.hammerspace.csi`) is installed and running in the cluster.
- Hammerspace shares or backing files already exist.
- The required `StorageClass` resources (e.g., `hs-storage`, `hs-storage-file-backed`, `hs-storage-block`) are created and configured.

## Volume Import Scenarios

### 1. NFS-Backed Shares (ReadWriteMany)

NFS shares support simultaneous read-write access from multiple hosts, making them ideal for multi-cluster or multi-pod access.

> Ensure the [`persistentVolumeReclaimPolicy`](https://kubernetes.io/docs/tasks/administer-cluster/change-pv-reclaim-policy/) is set to `Retain` to avoid accidental data deletion when the PVC is removed.

#### PersistentVolume Definition

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  annotations:
    pv.kubernetes.io/provisioned-by: com.hammerspace.csi
  name: restored-nfs-pv-test
  labels:
    name: restored-nfs-pv-test
spec:
  accessModes:
    - ReadWriteMany
  capacity:
    storage: 1Gi
  csi:
    driver: com.hammerspace.csi
    fsType: nfs
    volumeAttributes:
      mode: Filesystem
    # Path to the existing HS share export
    volumeHandle: /test-restore
  persistentVolumeReclaimPolicy: Retain
  storageClassName: hs-storage
  volumeMode: Filesystem
```
##### PersistentVolumeClaim Definition

```yaml
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: nfs-restored
spec:
  accessModes:
    - ReadWriteMany
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: hs-storage
  selector:
    matchLabels:
      name: restored-nfs-pv-test

```

## 2. File-backed Mount Volumes

##### PersistentVolume Definition

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  annotations:
    pv.kubernetes.io/provisioned-by: com.hammerspace.csi
  name: restored-filebacked-pv
  labels:
    name: restored-filebacked-pv
spec:
  accessModes:
  - ReadWriteOnce
  capacity:
    storage: 1Gi
  csi:
    driver: com.hammerspace.csi
    fsType: ext4
    volumeAttributes:
      fsType: ext4
      mode: Filesystem
      mountBackingShareName: k8s-file-backed
      size: "1073741824"
    volumeHandle: /k8s-file-backed/csi-k8s-pvc-7b6d0529-5fb4-11ea-b1fd-42010a800016
  persistentVolumeReclaimPolicy: Retain
  storageClassName: hs-storage-file-backed
  volumeMode: Filesystem
```
##### PersistentVolumeClaim Definition
```yaml
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: filebacked-restored
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: hs-storage-file-backed
  selector:
    matchLabels:
      name: restored-filebacked-pv
```

## 3. File-backed Block Volumes
##### PersistentVolume Definition
```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  annotations:
    pv.kubernetes.io/provisioned-by: com.hammerspace.csi
  name: restored-block-pv
  labels:
    name: restored-block-pv
spec:
  accessModes:
  - ReadWriteOnce
  capacity:
    storage: 1Gi
  csi:
    driver: com.hammerspace.csi
    volumeAttributes:
      blockBackingShareName: k8s-block-storage
      mode: Block
      size: "1073741824"
    volumeHandle: /k8s-block-storage/csi-k8s-pvc-b55c132c-5f1a-11ea-b1fd-42010a800016 # /path_name_in_storageclass/file_name
  persistentVolumeReclaimPolicy: Retain
  storageClassName: hs-storage-block
  volumeMode: Block
```
##### PersistentVolumeClaim Definition
```yaml
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: nfs-restored
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Block
  resources:
    requests:
      storage: 1Gi
  storageClassName: hs-storage-block
  selector:
    matchLabels:
      name: restored-block-pv
```

## Cleanup
Deleting the PVC will not delete the underlying Hammerspace data due to the Retain policy.

To reuse or clean up manually, make sure you unbind the PVC and manage the data in Hammerspace directly.