### Here are a few example YAML files to illustrate this:

1. Secret Definitions:
   
First, you need to define the secrets in your Kubernetes cluster. Let's create two secrets, hs-secret-1 and hs-secret-2, in the default namespace.

- secret-1.yaml
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: hs-secret-1
  namespace: default
type: Opaque
stringData:
  username: "admin"
  password: "password"
  csiEndpoint: "10.10.10.10"
  csiTlsVerify: "false"
```
- secret-2.yaml
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: hs-secret-2
  namespace: default
type: Opaque
stringData:
  username: "user2"
  password: "password2"
  csiEndpoint: "10.10.10.11"
  csiTlsVerify: "false"
```

2. Storage Class Definitions:

Now, let's define two storage classes, each referencing a different secret.

- hs-sc-1.yaml
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: hs-sc-1
provisioner: csi.hammerspace.com
parameters:
  csi.storage.k8s.io/provisioner-secret-name: hs-secret-1
  csi.storage.k8s.io/provisioner-secret-namespace: default
  csi.storage.k8s.io/node-stage-secret-name: hs-secret-1
  csi.storage.k8s.io/node-stage-secret-namespace: default
  csi.storage.k8s.io/node-publish-secret-name: hs-secret-1
  csi.storage.k8s.io/node-publish-secret-namespace: default
  # Other parameters specific to your storage provisioner
  fsType: "nfs"
  volumeNameFormat: "pvc-%s"
reclaimPolicy: Delete
```
- hs-sc-2.yaml
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: hs-sc-2
provisioner: csi.hammerspace.com
parameters:
  csi.storage.k8s.io/provisioner-secret-name: hs-secret-2
  csi.storage.k8s.io/provisioner-secret-namespace: default
  csi.storage.k8s.io/node-stage-secret-name: hs-secret-2
  csi.storage.k8s.io/node-stage-secret-namespace: default
  csi.storage.k8s.io/node-publish-secret-name: hs-secret-2
  csi.storage.k8s.io/node-publish-secret-namespace: default
  # Other parameters specific to your storage provisioner
  fsType: "xfs"
  volumeNameFormat: "test-%s"
reclaimPolicy: Delete
```

3. PersistentVolumeClaim (PVC) Definitions:

Finally, let's create PersistentVolumeClaims (PVCs) that use these storage classes.

- hs-pvc-1.yaml
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: hs-pvc-1
spec:
  storageClassName: hs-sc-1
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
```
- hs-pvc-2.yaml

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: hs-pvc-2
spec:
  storageClassName: hs-sc-2
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 2Gi
```

### Explanation:

- Secrets: The Secret resources store the credentials used by the CSI driver to talk to the Hammerspace management/API endpoint (REST). These credentials are **not** used as NFS mount credentials; the data path mount is performed by the node via NFS, while the driver uses the API credentials to discover/prepare exports, create/delete shares, apply policies/objectives, etc.
  - Required keys: `csiEndpoint`, `username`, `password`
  - Optional keys: `csiTlsVerify` (`"true"`/`"false"`)

- Storage Classes: The StorageClass resources define the type of storage to be provisioned. The parameters section includes:
csi.storage.k8s.io/provisioner-secret-name: Specifies the name of the secret containing the credentials.
csi.storage.k8s.io/provisioner-secret-namespace: Specifies the namespace where the secret is located.
  csi.storage.k8s.io/node-stage-secret-name / namespace: Secrets used by NodeStageVolume (root share mount).
  csi.storage.k8s.io/node-publish-secret-name / namespace: Secrets used by NodePublishVolume (bind mounts and file/block mounts).
Other storage-specific parameters (e.g., fsType, volumeNameFormat).

#### Delete Behavior

DeleteVolume uses the provisioner secrets when provided. The driver does **not** cache secrets from CreateVolume for later use.

If the delete request does not include secrets (for example, if the StorageClass is not configured with `csi.storage.k8s.io/provisioner-secret-name` / `csi.storage.k8s.io/provisioner-secret-namespace`), the driver falls back to the controller's configured credentials (for example, the Deployment's `com.hammerspace.csi.credentials` Secret via env vars).

This is usually fine in single-endpoint clusters, but in multi-tenant or multi-endpoint setups it can cause share-backed deletes to fail if the controller's default credentials do not have access to the target Hammerspace system/share. In those setups, always supply provisioner secrets in the StorageClass so DeleteVolume is authenticated to the correct Hammerspace system.

- PersistentVolumeClaims: The PersistentVolumeClaim resources request storage from the provisioner. The storageClassName field specifies which storage class to use, which in turn determines which secret will be used for provisioning.

#### Testing the Configuration

Apply the Secrets:
```bash
kubectl apply -f hs-secret-1.yaml
kubectl apply -f hs-secret-2.yaml
```
Apply the Storage Classes:
```bash
kubectl apply -f hs-sc-1.yaml
kubectl apply -f hs-sc-2.yaml
```
Apply the PVCs:
```bash
kubectl apply -f hs-pvc-1.yaml
kubectl apply -f hs-pvc-2.yaml
```
After applying these YAML files, the CSI driver should use hs-secret-1 to provision the volume for hs-pvc-secret-1 and hs-secret-2 to provision the volume for hs-pvc-secret-2. You can verify this by inspecting the logs of the CSI driver to see which credentials were used for each provisioning operation.
