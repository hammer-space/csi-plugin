# Hammerspace CSI Helm Charts

This repository provides Helm charts to deploy the **Hammerspace CSI driver** in a Kubernetes cluster.  
The chart installs both the **Controller** and **Node** plugins, and supports configuration of timeouts, retry intervals.

---

## 🚀 Quickstart: Deploy the Chart

### 1) Add the Helm repository
> Replace `<GITHUB_PAGES_URL>` with your GitHub Pages URL where `index.yaml` is hosted (e.g., `https://hammer-space/csi-plugin.github.io/deploy/helm/`).
```bash
helm repo add hscsi https://hammer-space/csi-plugin.github.io/deploy/helm/
helm repo update
helm install my-hammerspace-csi hscsi/hammerspace-csi --version 1.2.8
```

### 2) Create your `values.yaml`
You can override default settings by creating a custom `values.yaml`. Example:

```yaml
# values.yaml (example)

controller:
  replicaCount: 2
  resources:
    requests:
      cpu: "200m"
      memory: "512Mi"
    limits:
      cpu: "1"
      memory: "1Gi"

node:
  # Tolerate control-plane nodes if needed
  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: "Exists"
      effect: "NoSchedule"

```

### 3) Install the chart
```bash
helm install hscsi hscsi/hammerspace-csi \
  --namespace kube-system \
  --create-namespace \
  -f values.yaml
```

Alternatively, override values inline:
```bash
helm install hscsi hscsi/hammerspace-csi \
  --namespace kube-system \
  --create-namespace \
  --set controller.replicaCount=2
```

### 4) Verify the deployment
```bash
kubectl get pods -n kube-system -l app.kubernetes.io/name=hscsi
kubectl get csidrivers | grep hammerspace || true
```

You should see both **csi-provisioner-0** and **csi-node-<uuid>** plugin pods running.
---

## 🔧 Upgrade
```bash
helm upgrade hscsi -n kube-system -f values.yaml
```

## 🗑 Uninstall
```bash
helm uninstall hscsi -n kube-system
```

---

## ⚙️ Configuration Reference (common)
| Key | Type | Description |
| --- | --- | --- |
| `controller.replicaCount` | int | Number of controller replicas |
| `controller.resources` | map | Requests/limits for controller pods |
| `node.tolerations` | list | Tolerations for node daemonset |

> For all options, see `values.yaml` in the chart.

---

## 🧰 Troubleshooting
- Pods stuck in ImagePullBackOff: Verify image registry access and tag in the chart values.
- "csi-driver" not found: Run `helm repo update` and check that your `<GITHUB_PAGES_URL>/index.yaml` is accessible.
- No nodes provisioned: Check node plugin DaemonSet tolerations/affinity and that nodes can reach the NFS endpoints.

---

## 📚 References
- Helm: https://helm.sh/docs/
- Kubernetes CSI: https://kubernetes-csi.github.io/docs/
