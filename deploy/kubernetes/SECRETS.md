# Managing the Hammerspace CSI credentials

The CSI driver authenticates to your Hammerspace Anvil with an administrative
user. Those credentials live in a Kubernetes `Secret` named
`com.hammerspace.csi.credentials` (namespace `kube-system`) with three keys —
`username`, `password`, and `endpoint` — which the controller and node pods read
via `secretKeyRef` (see the `plugin.yaml` for your Kubernetes version).

Because this is an **administrative** credential, how you store it matters. This
guide covers, in increasing order of hardening:

1. [What *not* to do](#what-not-to-do)
2. [Least-privilege Anvil account](#least-privilege-anvil-account)
3. [Baseline — create the Secret imperatively](#baseline--create-the-secret-imperatively)
4. [Least-privilege RBAC](#least-privilege-rbac)
5. [GitOps-safe: Sealed Secrets](#gitops-safe-sealed-secrets)
6. [External secret managers: ESO and Secrets Store CSI](#external-secret-managers-eso-and-secrets-store-csi)

> **Key fact:** a Kubernetes `Secret` is **base64-encoded, not encrypted.**
> `YWRtaW4=` is just `admin`. Base64 stops shoulder-surfing, nothing more.

---

## What *not* to do

- **Do not commit a filled-in `Secret` manifest to version control.** The
  base64 in a committed `Secret` is trivially reversible, and git keeps it in
  history forever even after you "delete" it. `example_secret.yaml` in this
  directory is a **template** full of `<PLACEHOLDER>` values for exactly this
  reason — fill it in locally and apply it, but do not check the filled copy in.
- **Do not paste the password into a `ConfigMap`, a container `command`, or a
  plain environment `value:`.** Those are not treated as sensitive by Kubernetes.
- **Do not use a full Anvil administrator for the driver.** The credential in the
  Secret only needs to manage shares and snapshots — give it a scoped role, not
  `admin`. See below.

---

## Least-privilege Anvil account

Give the driver a dedicated Anvil service user bound to a custom role that grants
**only** what the driver calls: full control of shares and snapshots, read-only on
everything else. This bounds the blast radius if the Secret leaks.

The role needs these permissions (Hammerspace roles are per-object-type
`create/read/update/delete` ACLs):

| Object type      | Permissions | Used for                                             |
| ---------------- | ----------- | ---------------------------------------------------- |
| `ANY`            | R           | read fallback — capacity, tasks, volumes, portals, file lookups |
| `SHARE`          | C R U D     | provision / resize / delete volumes, set objectives  |
| `SHARE_SNAPSHOT` | C R U D     | share-level snapshots                                |
| `FILE_SNAPSHOT`  | C R U D     | file-level snapshots and restores                    |

### Create the role

REST (`POST /mgmt/v1.2/rest/roles`):

```bash
ANVIL=https://<ANVIL>:8443/mgmt/v1.2/rest
# Authenticate as an admin; save the session cookie
curl -sk -c cookies.txt --data-urlencode 'username=<ADMIN>' \
  --data-urlencode 'password=<ADMIN_PASSWORD>' "$ANVIL/login"

curl -sk -b cookies.txt -X POST "$ANVIL/roles" -H 'Content-Type: application/json' -d '{
  "name": "csi-provisioner",
  "comment": "Least-privilege role for the Kubernetes CSI driver",
  "idleTimeoutSeconds": 3600,
  "acls": [
    { "objectType": "ANY",            "mask": { "read": true } },
    { "objectType": "SHARE",          "mask": { "create": true, "read": true, "update": true, "delete": true } },
    { "objectType": "SHARE_SNAPSHOT", "mask": { "create": true, "read": true, "update": true, "delete": true } },
    { "objectType": "FILE_SNAPSHOT",  "mask": { "create": true, "read": true, "update": true, "delete": true } }
  ]
}'
```

CLI (`hs` / clish) — the ACL grammar is `ObjectType:+<crud>`:

```
role-create --name=csi-provisioner \
  --acl ANY:+r \
  --acl SHARE:+crud \
  --acl SHARE_SNAPSHOT:+crud \
  --acl FILE_SNAPSHOT:+crud \
  --idle-timeout=1h
```

### Create the service user and assign the role

REST (`POST /mgmt/v1.2/rest/users`) — reference the role by name or uuid:

```bash
curl -sk -b cookies.txt -X POST "$ANVIL/users" -H 'Content-Type: application/json' -d '{
  "username": "csi",
  "password": "<PLACEHOLDER>",
  "enabled": true,
  "managementRole": { "name": "csi-provisioner" }
}'
```

CLI:

```
user-create --name=csi --password=<PLACEHOLDER> --mgmt-role-name=csi-provisioner
```

Use **this** service user's username/password in the Secret below — not an admin's.

---

## Baseline — create the Secret imperatively

The simplest safe path: create the Secret directly with `kubectl`, so the plaintext never lands in a
file you might commit.

```bash
kubectl create secret generic com.hammerspace.csi.credentials \
  --namespace kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-literal=password='<PLACEHOLDER>' \
  --from-literal=endpoint='https://<PLACEHOLDER>'
```

To read the value from a file instead of your shell history:

```bash
kubectl create secret generic com.hammerspace.csi.credentials \
  --namespace kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-file=password=./anvil-password.txt \
  --from-literal=endpoint='https://<PLACEHOLDER>'
```

**Rotating the credential.** The pods read the values as environment variables at
start, so they do not pick up a changed Secret automatically. To rotate:

```bash
kubectl delete secret com.hammerspace.csi.credentials -n kube-system
kubectl create secret generic com.hammerspace.csi.credentials -n kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-literal=password='<NEW_PLACEHOLDER>' \
  --from-literal=endpoint='https://<PLACEHOLDER>'
kubectl -n kube-system rollout restart statefulset/csi-provisioner
kubectl -n kube-system rollout restart daemonset/csi-node
```

(Adjust the workload names to match your `plugin.yaml`.)

---

## Least-privilege RBAC

Two facts drive the RBAC recommendation:

1. The driver's own credentials are injected into the pods by the **kubelet** via
   `secretKeyRef`. This needs **no ServiceAccount RBAC at all** — the kubelet
   reads the Secret on the pod's behalf.
2. The bundled `StorageClass` examples do **not** use per-volume provisioner
   secrets (`csi.storage.k8s.io/provisioner-secret-name`), so the
   `external-provisioner` sidecar does not need to read Secrets either.

The bundled `plugin.yaml` therefore scopes the `secrets` grant on both the
`csi-provisioner` and `csi-node` ClusterRoles down to a single named Secret with
`get` only (rather than cluster-wide `get, list` over *all* Secrets):

```yaml
- apiGroups: [""]
  resources: ["secrets"]
  resourceNames: ["com.hammerspace.csi.credentials"]
  verbs: ["get"]
```

This bounds the blast radius: a compromised driver ServiceAccount can no longer
enumerate or read arbitrary Secrets across the cluster.

If you configure **per-volume secrets** on a StorageClass, add the specific
Secret name(s) to `resourceNames` (and a matching
`csi.storage.k8s.io/provisioner-secret-namespace`). See the CSI
[StorageClass Secrets](https://kubernetes-csi.github.io/docs/secrets-and-credentials-storage-class.html)
docs.

---

## GitOps-safe: Sealed Secrets

If you drive your cluster from git and want the *encrypted* credential to live in
the repo safely, [Sealed Secrets](https://github.com/bitnami/sealed-secrets)
is the lightest-weight option. You encrypt a normal Secret with the cluster's
public key using the `kubeseal` CLI; only the in-cluster controller can decrypt
it, so the resulting `SealedSecret` is safe to commit.

Generate one from the imperative command above:

```bash
kubectl create secret generic com.hammerspace.csi.credentials \
  --namespace kube-system \
  --from-literal=username='<PLACEHOLDER>' \
  --from-literal=password='<PLACEHOLDER>' \
  --from-literal=endpoint='https://<PLACEHOLDER>' \
  --dry-run=client -o yaml \
  | kubeseal --format yaml > sealed-credentials.yaml
```

`sealed-credentials.yaml` looks like the following (the `encryptedData` values are
ciphertext — the real example will have long base64 blobs). **This** file is the
one you commit; the controller unseals it into the `Secret` the driver reads:

```yaml
apiVersion: bitnami.com/v1alpha1
kind: SealedSecret
metadata:
  name: com.hammerspace.csi.credentials
  namespace: kube-system
spec:
  encryptedData:
    username: AgB<PLACEHOLDER-CIPHERTEXT>...
    password: AgC<PLACEHOLDER-CIPHERTEXT>...
    endpoint: AgD<PLACEHOLDER-CIPHERTEXT>...
  template:
    metadata:
      name: com.hammerspace.csi.credentials
      namespace: kube-system
    type: Opaque
```

Trade-off: `kubeseal` is a manual step for every credential change, so it keeps a
human in the loop (fine for small teams; friction for fully-automated GitOps).

---

## External secret managers: ESO and Secrets Store CSI

For larger environments the credential usually already lives in a dedicated
secret manager (HashiCorp Vault, AWS Secrets Manager, Azure Key Vault, GCP Secret
Manager). Two CNCF projects bridge those into the cluster; both keep the
plaintext out of git entirely.

- **[External Secrets Operator (ESO)](https://external-secrets.io/)** — you
  commit a `SecretStore` (which backend, how to auth) and an `ExternalSecret`
  (which key maps to which Secret field). ESO reconciles a *real* Kubernetes
  `Secret` named `com.hammerspace.csi.credentials`, which the driver then reads
  with no changes. Best when you want a native Secret synced from an external
  source and rotated automatically.

- **[Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)** —
  mounts secrets from the external store into pods as files, and can optionally
  sync them to a Kubernetes `Secret`. Note the Hammerspace driver currently reads
  its credentials from environment variables (`secretKeyRef`), so to use Secrets
  Store CSI here, enable its **`secretObjects` sync** so it produces the
  `com.hammerspace.csi.credentials` Secret the driver expects.

Rough guidance:

| Approach            | Plaintext in git? | Auto-rotation | Best for            |
| ------------------- | ----------------- | ------------- | ------------------- |
| Imperative Secret   | No                | Manual        | Getting started     |
| Sealed Secrets      | No (encrypted)    | Manual reseal | Small GitOps teams  |
| External Secrets Op | No                | Yes           | Vault-backed shops  |
| Secrets Store CSI   | No                | Yes           | Mount-from-vault    |

---

## References

- [Kubernetes — Secrets](https://kubernetes.io/docs/concepts/configuration/secret/)
- [Kubernetes CSI — StorageClass Secrets](https://kubernetes-csi.github.io/docs/secrets-and-credentials-storage-class.html)
- [Sealed Secrets](https://github.com/bitnami/sealed-secrets)
- [External Secrets Operator](https://external-secrets.io/)
- [Secrets Store CSI Driver — Best Practices](https://secrets-store-csi-driver.sigs.k8s.io/topics/best-practices)
