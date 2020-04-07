# Volume Imports
This guide describes adding existing Hammerspace shares, or files contained in a Hammerspace share, as a PersistentVolume in Kubernetes. This guide assumes the reader has basic knowledge of PersistentVolumes and PersistentVolumeClaims in Kubernetes ([Docs](https://kubernetes.io/docs/concepts/storage/persistent-volumes/))

Using existing storage can be helpful for Disaster Recovery or simply using the same storage for applications across multiple Kubernetes clusters.


## NFS share Backed Volumes
NFS shares allow multiple hosts to mount them as ReadWrite at the same time. This means you can create a volume either separately from Kubernetes or in a cluster, then consume it in multiple clusters by creating a PersistentVolume object that points to the backing storage.

It is critical that the [persistentVolumeReclaimPolicy](https://kubernetes.io/docs/tasks/administer-cluster/change-pv-reclaim-policy/) on your PV is set to "Retain" so that when a bound PVC is deleted, the HS plugin will not delete the HS share out from under any other consumers.

The following example will import an existing HS share (with path `/test-restore`) as a Kubernetes PersistentVolume which is then bound to a PersistentVolumeClaim.

##### PersistentVolume Definition
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
    # Points to the export path of the share in Hammerspace
    volumeHandle: /test-restore
  persistentVolumeReclaimPolicy: Retain
  # This storage class must exist and should represent the characteristics of the existing share
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

## File-backed Mount Volumes
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

## File-backed Block Volumes
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
    volumeHandle: /k8s-block-storage/csi-k8s-pvc-b55c132c-5f1a-11ea-b1fd-42010a800016
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
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: hs-storage-block
  selector:
    matchLabels:
      name: restored-block-pv
```