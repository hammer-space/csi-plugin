# Developer Guide: Building, Packaging, and Publishing Helm Charts

This guide helps you build a new Helm chart or update an existing chart for configuration changes. See `README.md` for user installation instructions.

---

## Prerequisites
- Helm installed (`helm version`)
- Git access to this repository
- (Optional) GitHub Pages enabled for publishing the Helm repo index

---

## Create a New Helm Chart
```sh
helm create <chart-name>
```
Then customize:
- Chart.yaml - name, version (chart version), appVersion (driver version)
- values.yaml - defaults
- templates/ - manifests

Lint & test:
```sh
helm lint <chart-name>
helm install --dry-run --debug <release-name> <chart-name>
```

---

## Update an Existing Chart
```sh
git pull
# Edit values/templates/Chart.yaml as needed
helm lint <chart-dir>
helm upgrade --dry-run --debug <release-name> <chart-dir>
```

Versioning rules (semantic):
- Bump Chart.yaml:version each release (e.g., 1.2.8 -> 1.2.9)
- Update appVersion when the CSI plugin version changes

---

## Package a New Version

Example layout:
```
deploy/helm/repo/
├─ v1.2.7/               				# chart root
├─ index.yaml    						# Helm repo index (published)
└─ v1.2.7/hammerspace-csi-1.2.7.tgz     # packaged chart(s)
```

1) Package the chart
```sh
cd deploy/helm/repo/v1.2.7
helm package ./hammerspace-helm-chart
# Produces hammerspace-csi-<version>.tgz
```

2) Update the repo index
```sh
cd ..
# Replace <GITHUB_PAGES_URL> with the base URL of your Helm repo hosting
helm repo index . --url <GITHUB_PAGES_URL>
```

3) Commit and push the artifacts
```sh
git add *.tgz index.yaml
git commit -m "chore(helm): release csi-driver <version>"
git push
```

### If hosted on GitHub Pages
- Publish index.yaml and all *.tgz to your Pages branch (commonly gh-pages) or /docs folder on main
- Ensure Pages serves the exact directory containing index.yaml
- Optional: add artifacthub-repo.yml next to index.yaml for Artifact Hub

Minimal artifacthub-repo.yml:
```yaml
repositoryID: "hammerspace-csi-helm-repo"
owners:
  - name: "Your Name"
    email: "you@example.com"
```

---

## Local Smoke Test from the Built Repo
After you publish the new package + index.yaml:
```sh
helm repo remove hscsi || true
helm repo add hscsi <GITHUB_PAGES_URL>
helm repo update
helm search repo hscsi
helm install test-hscsi hscsi/csi-driver --dry-run --debug
```

---

## Optional: Makefile helpers
Create a Makefile in deploy/helm/repo:

```make
CHART_DIR ?= csi-driver
REPO_URL  ?= <GITHUB_PAGES_URL>

package:
\tcd $(CHART_DIR) && helm package .

index:
\thelm repo index . --url $(REPO_URL)

release: package index
\tgit add *.tgz index.yaml
\tgit commit -m "chore(helm): release $$(date +%Y.%m.%d-%H%M)"
\tgit push
```

Usage:
```sh
make package
make index
make release
```

---

## Release Checklist
- [ ] Chart.yaml version bumped
- [ ] appVersion set to the intended CSI plugin image tag
- [ ] helm lint passes
- [ ] helm install --dry-run --debug passes
- [ ] New .tgz generated and committed
- [ ] index.yaml updated with correct --url
- [ ] GitHub Pages serves the folder containing index.yaml
