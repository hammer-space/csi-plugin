# Kubernetes v1.10.x-v1.12.x Installation/Configurations

Plugin interfaces with old versions of k8s via CSI v0.3 and only has support for Mount (Filesystem) Volumes.

## Updating to Kubernetes 1.13+

After upgrading your Kubernetes cluster to 1.13+, simply update or replace the existing Kubernetes objects defined in this directory with those defined in the YAML files for Kubernetes 1.13+.

1. Delete existing plugin resources
    ```bash
    kubectl delete -f deploy/kubernetes/versions_1.10-1.12/plugin.yaml
    ```
1. Customize the settings
1. Create new plugin resources
    ```bash
    kubectl apply -f deploy/kubernetes/plugin.yaml
    ```

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
