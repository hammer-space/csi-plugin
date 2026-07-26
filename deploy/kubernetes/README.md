# Kubernetes Installation/Configuration

This directory contains example manifests for deploying the plugin to Kubernetes.

To deploy all necessary components, customize these files and apply them:
Apply all from within this directory:
```bash
kubectl apply -f *.yaml
```

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
## Kubernetes  Cluster Prerequisites
Kubernetes documentation for CSI support can be found [here](https://kubernetes-csi.github.io/)

* There is no single blanket minimum Kubernetes version — the required version is
  specific to the manifest you deploy. Per-minor manifests live under
  `deploy/kubernetes/kubernetes-<major>.<minor>/plugin.yaml`, and each targets that
  Kubernetes minor, so apply the one matching your `kubectl`/cluster version.
  Bundled: **1.29** and **1.34 / 1.35 / 1.36**. **1.34–1.36 are the currently supported +
  validated set** — the driver in this release was tested end-to-end on live k8s
  **1.34** and **1.35** clusters (and 1.36). Those three manifests are the 1.29
  manifest plus the observability wiring (a host-networked metrics port and OTel
  env vars); see [`docs/observability.md`](../../docs/observability.md) and
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
* Voluem expansion support requires v1.14+ ``ExpandCSIVolumes`` and ``ExpandInUsePersistentVolumes``
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
  namespace: kube-system
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