# Kubernetes v1.13+ Installation/Configurations

This directory contains example manifests for deploying the plugin to Kubernetes.

Documentation on how to write these manifests can be found [here](https://github.com/kubernetes/community/blob/master/contributors/design-proposals/storage/container-storage-interface.md#recommended-mechanism-for-deploying-csi-drivers-on-kubernetes)

To deploy all necessary components, customize these files and apply them:
Apply all from within this directory:
```bash
kubectl apply -f *.yaml
```


## Plugin Updates

To deploy updates to the plugin, simply change the image tag ```hammerspaceinc/csi-plugin``` of the StatefulSet and DaemonSet to the new plugin image, make any other update to environment variables, and reapply the yaml files.

If you are using ```hammerspaceinc/csi-plugin:latest``` you must delete all the existing plugin pods so the new image is pulled and the pods are recreated automatically. Otherwise, changing the image tag will trigger an update to occur. Ex. ```hammerspaceinc/csi-plugin:v0.1.0``` -> ```hammerspaceinc/csi-plugin:v0.1.1```
## Kubernetes  Cluster Prerequisites
Kubernetes documentation for CSI support can be found [here](https://kubernetes-csi.github.io/)

* Kubernetes version 1.13 or higher
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
* VolumeSnapshot support requires the VolumeSnapshotDataSource feature flag
* Each host should have support for NFS v4.2 or v3 with the relevant network ports open between the host and storage

### NOTE on Google Kubernetes Engine
GKE does not allow the creation of ClusterRoles
that are more powerful than the given user. An insecure work around to this is
to give the user creating the role cluster-admin privileges.

```bash
kubectl create clusterrolebinding i-am-root --clusterrole=cluster-admin --user=<current user>
```

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
    - name: data-dev
      persistentVolumeClaim:
        claimName: mydevice
```

### Create a Snapshot
```yaml
apiVersion: snapshot.storage.k8s.io/v1alpha1
kind: VolumeSnapshot
metadata:
  name: data-snapshot
spec:
  snapshotClassName: hs-snapshots
  source:
    name: mydevice
    kind: PersistentVolumeClaim
```
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