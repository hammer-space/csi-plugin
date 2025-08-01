# Hammerspace CSI Helm Charts

This repository provides Helm charts to deploy the Hammerspace CSI driver components in a Kubernetes cluster. It supports deploying both **Controller** and **Node** plugins, along with configurable options such as timeouts and retry intervals.

---
## 🚀 How to Deploy the Chart

1. **Add the Helm repository** (Published to GitHub Pages):
	```bash
	helm repo add hscsi https://github.com/hammer-space/csi-plugin/deploy/helm/repo
	helm repo update
	```

2. **Install the chart into your cluster:**
	```
	helm install hscsi hscsi/ --namespace kube-system --create-namespace
	```
---

## 📦 How to Package a New Version

#### Navigate to the chart directory:
```
cd deploy/helm/hammerspace-helm-chart
```
#### Create new package
```
helm package .
```

#### Move the .tgz to your repo directory (e.g., deploy/helm/repo/) and regenerate the index.yaml:
```
mv hammerspace-csi-<version>.tgz ../../repo/
cd ../../repo
helm repo index .
```

#### If hosted on GitHub Pages:

Ensure index.yaml and .tgz files are committed to the branch (typically gh-pages)

Add or update artifacthub-repo.yml next to index.yaml for Artifact Hub metadata

#### How to Create a New Helm Chart
If you need to add a new chart:
```
helm create new-chart
```
