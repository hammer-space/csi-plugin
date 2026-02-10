# Hammerspace CSI Driver Upgrade Guide — v1.2.8

**Audience:** Cluster operators managing large Kubernetes clusters (400+ nodes).

**Purpose:** Safe, repeatable process to test and roll out the Hammerspace CSI `v1.2.8` node plugin (bind-mount root-share change) across large clusters without mass manual restaging.

---

## 1. Executive summary

- v1.2.8 changes staging/publish behavior: the driver uses a **root share** mounted once per node and then **bind-mounts** per-volume subdirectories into pod targets.
- Old drivers (<= v1.2.7) mounted volumes directly (NFS direct mounts) and did not create the root share mount.
- After upgrading nodes, *existing volumes* on nodes previously staged with v1.2.7 may skip `NodeStageVolume` and only call `NodePublishVolume`, so the root-share may not exist on those nodes unless a lazy-stage is performed during publish.

**Goal of this guide:** provide a safe single-node test and a staged cluster rollout with validation and rollback steps. Includes diagrams, commands, and the `csi-node` DaemonSet snippet.

---

## 2. Diagrams (conceptual)

### Old flow (v1.2.7 and earlier)

```
[IP:/pvc-XYZ mounted directly] --> /var/lib/kubelet/pods/<pod>/volumes/.../mount (NFS direct)
```

### New flow (v1.2.8)

```
IP:/                 --> /var/lib/hammerspace/rootmount   (1 root NFS mount per node)
                                     |
                                     +--> bind mount /var/lib/hammerspace/rootmount/pvc-XYZ/  --> /var/lib/kubelet/pods/<pod>/volumes/.../mount
```

**Key migration point:** If a node had direct NFS mounts from old driver, the root share directory will not exist until either `NodeStageVolume` runs or a lazy-stage is performed by `NodePublishVolume`. For large clusters we recommend lazy-stage logic inside `NodePublishVolume` to automatically mount root share and convert old direct NFS mounts to bind mounts.

---

## 3. Phase 1 — Single-node testing (safe & reversible)

**Pick a node:**

- Choose a healthy, non-critical node that currently successfully mounts PVCs.
- Verify with:

```bash
kubectl get nodes -o wide
kubectl describe node <NODE_NAME>
```

**Steps:**

1. Cordon the node so new pods don't land on it during validation:

```bash
kubectl cordon <NODE_NAME>
```

2. Label the node so DaemonSet can be targeted for the test:

```bash
kubectl label node <NODE_NAME> csi-test-node=true
```

3. Patch the `csi-node` DaemonSet to include the nodeSelector and set the new image. Example patch (JSON patch):

```bash
kubectl patch daemonset csi-node -n kube-system \
  --type='json' \
  -p='[
    {"op":"add","path":"/spec/template/spec/nodeSelector","value":{"csi-test-node":"true"}},
    {"op":"replace","path":"/spec/template/spec/containers/2/image","value":"hammerspaceinc/csi-plugin:v1.2.8"}
  ]'
```

*NB:* the `containers/2` index in the patch assumes container order; if you want safer, edit the DaemonSet YAML and change the `hs-csi-plugin-node` image and add `nodeSelector` under the template.

4. Wait for the DaemonSet to schedule the updated pod onto the labeled node and verify the pod is running:

```bash
kubectl get pods -n kube-system -o wide -l app=csi-node
kubectl logs -n kube-system <csi-node-pod> -c hs-csi-plugin-node --tail=200
```

5. Run a test Pod that mounts an existing PVC (or create a temporary PVC + Pod):

```bash
kubectl run csi-test --image=nginx --restart=Never \
  --overrides='{"apiVersion":"v1","spec":{"volumes":[{"name":"test-vol","persistentVolumeClaim":{"claimName":"<PVC_NAME>"}}],"containers":[{"name":"nginx","image":"nginx","volumeMounts":[{"mountPath":"/data","name":"test-vol"}]}]}}'

kubectl exec -it csi-test -- mount | grep /data
```

6. Validate:

- Confirm the root share exists on the node (`ls /var/lib/hammerspace/rootmount`).
- Confirm the pod target path is a **bind mount** whose source path is under `/var/lib/hammerspace/rootmount/<pvc-id>`.

Example on node: `mount | grep hammerspace` or inside the pod: `mount | grep /data`.

7. Roll back test targeting (remove nodeSelector and unlabel):

```bash
kubectl patch daemonset csi-node -n kube-system --type='json' -p='[{"op":"remove","path":"/spec/template/spec/nodeSelector"}]'
kubectl label node <NODE_NAME> csi-test-node-
kubectl uncordon <NODE_NAME>
```

---

## 4. Phase 2 — Cluster-wide rollout (recommended: staged)

**Plan:** Rolling update in batches, e.g., `maxUnavailable: 10%` in DaemonSet rollingUpdate. With 400 nodes, this means \~40 nodes at a time.

**Pre-checks:**

- Ensure cluster-wide backups of manifests for quick rollback.
- Ensure kubelet/node health and that `csi-node` DaemonSet can tolerate brief restarts.

**Step-by-step:**

1. Backup current DaemonSet and controller manifests:

```bash
kubectl get daemonset csi-node -n kube-system -o yaml > csi-node-backup.yaml
kubectl get statefulset csi-provisioner -n kube-system -o yaml > csi-provisioner-backup.yaml
```

2. Edit the DaemonSet update strategy:

```yaml
spec:
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 10%
```

3. Apply new image for the node plugin (and controller if needed):

```bash
kubectl set image daemonset/csi-node -n kube-system hs-csi-plugin-node=hammerspaceinc/csi-plugin:v1.2.8
kubectl set image statefulset/csi-provisioner -n kube-system csi-provisioner
```

4. Monitor rollout and logs:

```bash
kubectl rollout status daemonset/csi-node -n kube-system
kubectl get pods -n kube-system -l app=csi-node -o wide
kubectl logs -n kube-system -l app=csi-node --tail=200
```

5. Spot-check mounts on upgraded nodes:

Choose a few nodes that report `Ready` with updated pods and run:

```bash
kubectl debug node/<NODE_NAME> -n kube-system --image=registry.k8s.io/pause
# or ssh to node
mount | grep hammerspace
# check pod mounts
kubectl get pods -o wide --field-selector spec.nodeName=<NODE_NAME>
kubectl exec -it <pod-on-node> -- mount | grep /var/lib/kubelet/pods
```

6. Validate PVCs after rollout:

```bash
kubectl get pvc --all-namespaces
kubectl describe pvc -n <ns> <pvc-name>
# confirm pods on upgraded nodes can access volumes and see bind mounts from /var/lib/hammerspace/rootmount
```

---

## 5. Validation & health checks (commands)

### List PVCs and their bound PVs

```bash
kubectl get pvc --all-namespaces -o wide
```

### Check mount points on a node (SSH or debug pod)

```bash
mount | grep hammerspace
findmnt --target /var/lib/hammerspace/rootmount
```

### Inspect a pod's volume mount inside the pod

```bash
kubectl exec -it <pod> -- mount | grep /data
```

### Count nodes with rootshare mounted

```bash
# run as a DaemonSet job or remote SSH script to check per-node
# example: use kubectl debug or ssh
```

---

## 6. Troubleshooting

- **Old mounts not replaced / bind mount failing**: ensure `NodePublishVolume` lazy-stage logic runs — check node logs for `[LazyStage]` entries.
- **ESTALE errors**: may appear on stale NFS mounts. Rebooting kubelet or remounting the underlying NFS may help. Prefer to unmount the *old direct mount at the pod target path* before bind-mounting; do NOT unmount the root share if other pods depend on it.
- **If many nodes show errors**: pause rollout, revert to backup DaemonSet manifest, and investigate logs.

---

## 7. Rollback

1. Revert the `csi-node` image to the backed-up image:

```bash
kubectl set image daemonset/csi-node -n kube-system hs-csi-plugin-node=<OLD_IMAGE>
```

2. Monitor rollout and confirm volumes become accessible again.

---

## 8. Appendix — csi-node DaemonSet snippet (example)

```yaml
# (snippet) Example csi-node DaemonSet for v1.2.8
kind: DaemonSet
apiVersion: apps/v1
metadata:
  name: csi-node
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app: csi-node
  template:
    metadata:
      labels:
        app: csi-node
    spec:
      serviceAccount: csi-node
      hostNetwork: true
      containers:
        - name: hs-csi-plugin-node
          image: hammerspaceinc/csi-plugin:v1.2.8
          envFrom:
            - configMapRef:
                name: csi-env-config
          env:
            - name: CSI_ENDPOINT
              value: /csi/csi.sock
          volumeMounts:
            - name: socket-dir
              mountPath: /csi
            - name: registration-dir
              mountPath: /registration
              mountPropagation: Bidirectional
            - name: mountpoint-dir
              mountPath: /var/lib/kubelet/
              mountPropagation: Bidirectional
            - name: rootshare-dir # new added with 1.2.8
              mountPath: /var/lib/hammerspace/
              mountPropagation: Bidirectional
            - name: staging-dir
              mountPath: /tmp
              mountPropagation: Bidirectional
      volumes:
        - name: socket-dir
          hostPath:
            path: /var/lib/kubelet/plugins_registry/com.hammerspace.csi
            type: DirectoryOrCreate
        - name: mountpoint-dir
          hostPath:
            path: /var/lib/kubelet/
        - name: rootshare-dir # new added with 1.2.8
          hostPath:
            path: /var/lib/hammerspace/
        - name: staging-dir
          hostPath:
            path: /tmp
```
