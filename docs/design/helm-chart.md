# Design: Hammerspace CSI Helm Chart (Phase 1)

**Status:** Draft for review
**Date:** 2026-08-22
**Scope:** Package the Hammerspace CSI driver as an idiomatic Helm chart. The chart is Phase 1 of a two-phase packaging effort; a Kubernetes operator is Phase 2 and is explicitly out of scope here (see Non-goals and the Phase 2 seam).

---

## 1. Motivation

The driver ships today as a set of hand-maintained per-minor manifests (`deploy/kubernetes/kubernetes-1.29|1.34|1.35|1.36/plugin.yaml`) and is the only CSI offering among its peers (NetApp, VAST, Dell, Pure/Portworx, WEKA, IBM) with **neither a Helm chart nor an operator**. That gap blocks enterprise adoption, complicates upgrades, and forces users to edit raw YAML. A Helm chart closes ~90% of that gap on its own: one-command install/upgrade, a validated configuration surface, and templating that collapses the per-minor manifest sprawl into a single source of truth.

## 2. Goal

A single, idiomatic Helm chart (`charts/hammerspace-csi`) that installs the complete driver — controller, node plugin, RBAC, `CSIDriver` object, and optional StorageClass/VolumeSnapshotClass — across the supported Kubernetes range (1.29, 1.34–1.36), with a clean `values.yaml`, schema validation, and a **fully static (no live cluster) test suite** that includes a dedicated adversarial review.

## 3. Guiding principle: faithful port, improvements opt-in

**The chart's default-rendered output must be behavior-equivalent to today's tested `kubernetes-1.36/plugin.yaml`.** Every security/cleanup improvement identified during design and prior reviews ships as a **values-gated toggle that is OFF by default**, so operators enable them incrementally after their own testing. "Same as today; reduce/harden after further testing," encoded directly in the defaults.

This principle is enforced mechanically by the golden-manifest snapshot test (§9).

## 4. Scope

In scope (Phase 1):
- One Helm chart rendering controller Deployment, node DaemonSet, RBAC, ServiceAccounts, `CSIDriver` object.
- Optional, values-driven StorageClasses and a VolumeSnapshotClass.
- `values.yaml` + `values.schema.json` + generated `README.md`.
- Static, cluster-free test suite (lint, template, schema validation, golden-manifest diff, `trivy config` on rendered output).
- A dedicated adversarial review phase over the chart and its rendered output.
- A `make` target (optionally a GitHub Action) running the static gates.

## 5. Non-goals (YAGNI)

- **No operator** — Phase 2, triggered only by an OpenShift-certification goal or an enterprise single-CR/self-healing requirement. The chart is designed so a future helm-based operator can wrap it thinly (§12).
- **No publishing** — no OCI push, no gh-pages Helm repo, no `chart-releaser`. The chart is built and kept in-repo; where/whether to publish is a later decision.
- **No cluster-dependent testing** — no `kind`, no `ct install`, no live API server in any gate.
- **No bundled snapshot CRDs or external-snapshotter controller** — those are cluster singletons; the chart documents them as a prerequisite (upstream convention) and does not own them.
- **No OpenShift SCC / OLM bundle** — Phase 2 / OpenShift.
- **No multi-backend CR config** — the driver is a single endpoint configured via env; no abstraction is introduced.
- **No driver code changes** — dependency-CVE bumps (HS-42779) and the code-review findings are tracked separately. The chart addresses packaging and manifest-level configuration only.

## 6. Chart structure

```
charts/hammerspace-csi/
  Chart.yaml              # chart version (independent) + appVersion = driver version
  values.yaml             # documented defaults (behavior-equivalent to today)
  values.schema.json      # JSON Schema validation of values
  README.md               # generated from values (helm-docs)
  templates/
    _helpers.tpl          # fullname, common labels, selectorLabels, image ref
    csidriver.yaml        # CSIDriver object
    controller-deployment.yaml
    node-daemonset.yaml
    serviceaccount-controller.yaml
    serviceaccount-node.yaml
    rbac-controller.yaml  # ClusterRole + ClusterRoleBinding (provisioner/resizer/snapshotter/attacher/freezer)
    rbac-node.yaml        # ClusterRole + ClusterRoleBinding for node
    secret.yaml           # rendered ONLY when hammerspace.secret.create=true
    storageclass.yaml     # range over .Values.storageClasses (optional)
    volumesnapshotclass.yaml  # optional, gated
    servicemonitor.yaml   # optional (metrics.serviceMonitor.enabled)
    NOTES.txt             # post-install guidance + immutability warnings
  ci/
    default-values.yaml   # = shipped defaults (drives the golden-manifest test)
    block-values.yaml     # block/file-backed StorageClass example rendering
    hardened-values.yaml  # all opt-in toggles ON (renders the "future" shape for review)
    minimal-values.yaml   # smallest valid install
```

## 7. Values schema (configuration surface)

Grouped and schema-validated. Defaults below preserve today's behavior.

| Group | Keys (defaults) |
|---|---|
| `image` | `repository`, `tag` (default = `.Chart.AppVersion`), `pullPolicy`, `pullSecrets: []` |
| `hammerspace` | `endpoint`, `tlsVerify: false`, `existingSecret` **or** `secret.create: false` (+ `username`/`password` for dev), `dataPortalMountPrefix: ""`, `logLevel` |
| `controller` | `replicaCount: 1`, `resources: {}` (unset), `nodeSelector`/`tolerations`/`affinity`, sidecar image tags, `attacher.enabled: true` |
| `node` | `resources: {}`, `tolerations` (run everywhere), `nodeDriverRegistrar.tag`, `kubeletDir: /var/lib/kubelet` |
| `csiDriver` | `attachRequired: true`, `fsGroupPolicy` (defaults to today's effective value; overridable), `podInfoOnMount: true`, `storageCapacity: true`, `requiresRepublish: true` |
| `rbac` | `create: true` |
| `serviceAccount` | `create: true`, `name` |
| `freezer` | `enabled: true` (controls the cluster-wide `pods/exec` grant) |
| `metrics` | `enabled`, `port`, OTel exporter env, `serviceMonitor.enabled: false` |
| `storageClasses` | `[]` (list of SC specs; each with `name`, `default`, params, mountOptions, reclaimPolicy, allowVolumeExpansion) |
| `snapshotClass` | `enabled: false`, params |
| `securityContext` | `seccompProfile.enabled: false`, container hardening toggles (all off) |
| misc | `priorityClassName`, `commonLabels`, `commonAnnotations`, `namespaceOverride` |

`values.schema.json` constrains types, enums (`logLevel`, `fsGroupPolicy`, `pullPolicy`), and required-when-relations (e.g., `secret.create=true` requires `username`/`password`; `existingSecret` and `secret.create` are mutually exclusive).

## 8. Behavior-preserving defaults vs. opt-in toggles

| Setting | Today | Chart default | Opt-in toggle |
|---|---|---|---|
| Anvil TLS verify | off (`HS_TLS_VERIFY=false`) | `hammerspace.tlsVerify: false` | set `true` (+ provide CA) |
| csi-attacher | deployed | `controller.attacher.enabled: true` | set `false` |
| `attachRequired` | unset → true | `csiDriver.attachRequired: true` | set `false` |
| Consistency-freeze RBAC (`pods/exec`) | granted | `freezer.enabled: true` | set `false` |
| Container resource limits | none | unset | populate `*.resources` |
| seccompProfile | none | off | `securityContext.seccompProfile.enabled: true` |
| Secret RBAC | `resourceNames`-scoped `get` | same (carried forward) | n/a |

`CSIDriver` immutability note (surfaced in `NOTES.txt` and README): `attachRequired`, `fsGroupPolicy`, and other `CSIDriver.spec` fields are immutable. Changing them on an existing install requires delete+recreate of the `CSIDriver` object, not a plain `helm upgrade`. The chart documents this; the adversarial review verifies upgrade paths flag it.

## 9. Testing strategy — fully static, no live cluster

All gates run offline (no API server):

1. **`helm lint`** across every `ci/*-values.yaml`.
2. **`helm template`** render across every `ci/*-values.yaml` — must succeed with no errors.
3. **`values.schema.json` validation** — invalid value sets must be rejected at template time (negative tests included).
4. **`kubeconform`** — validate rendered YAML against the Kubernetes OpenAPI JSON schemas for each supported minor (1.29, 1.34, 1.35, 1.36). Pulls schemas locally; no cluster.
5. **Golden-manifest snapshot** — render with `ci/default-values.yaml` and diff against a checked-in expected file derived from today's `kubernetes-1.36/plugin.yaml`. **Any drift fails the test and must be explained.** This is the mechanical enforcement of §3.
6. **`trivy config`** (a.k.a. `trivy fs --scanners misconfig`) on the **rendered** default and hardened outputs — closes the loop with the earlier driver Trivy scan; the default render is expected to carry the same by-design CSI findings (privileged/hostNetwork), the hardened render should show measurably fewer.

### Adversarial review phase (explicit requirement)

A dedicated review pass mirroring the driver code-review, over the templates and the rendered manifests, hunting by dimension and reporting findings by severity with concrete fixes:

- **Templating correctness** — unquoted or `nil`-deref values, `| quote` / `| toYaml` / `nindent` misuse, indentation drift, `range` scoping bugs, helper-name collisions.
- **RBAC scope** — over-grant (broad `secrets`/`*` verbs) and under-grant (a sidecar missing a verb it needs); confirm the `resourceNames`-scoped secret grant is preserved and `freezer.enabled=false` actually drops `pods/exec`.
- **Security-misconfig regressions** — via `trivy config` on rendered output; no new HIGH/CRITICAL relative to today's manifest.
- **`CSIDriver` immutability upgrade traps** — simulate `helm upgrade` value changes to immutable fields; verify they are documented/guarded, not silently broken.
- **Secret leakage** — no secret value rendered into a ConfigMap, into `NOTES.txt`, or logged; `secret.create=false` path never emits credentials.
- **`helm upgrade` safety** — vN → vN+1 render diff review for orphaned resources, selector/label immutability (Deployment/DaemonSet `matchLabels` are immutable), and PVC-affecting changes.
- **Cross-K8s-version render** — the chart renders valid manifests on every supported minor; API-group differences (e.g., snapshot APIs) are handled via `.Capabilities.KubeVersion`, not per-minor forks.
- **Drift from the tested manifest** — the golden-manifest diff (gate 5) is the anchor; any intentional difference is enumerated with rationale.

Output: a severity-ranked findings list (Critical/High/Medium/Low) with file/line and fix, same format as the driver teardown. Findings are resolved before the chart is considered done.

## 10. CI

A `make` target (e.g., `make chart-test`) runs gates 1–6 and the static portions of the adversarial review checklist. Optionally wired into a GitHub Action that runs the same `make` target. **No publish/release step.**

## 11. Kubernetes version handling

One chart for 1.29 + 1.34–1.36. Sidecar image tags live in `values` with defaults validated across the range. Branch on `.Capabilities.KubeVersion` only where a real API difference exists. The per-minor `deploy/kubernetes/kubernetes-1.*` manifest dirs are deprecated in favor of the chart: keep `kubernetes-1.36` for one release as the reference/golden source, then archive the rest.

## 12. Phase 2 operator seam

No operator work now. To keep Phase 2 cheap, the chart avoids imperative install-time-only steps and keeps every resource fully templated from values, so a future **helm-based operator** (Operator SDK) can render this exact chart from a custom resource without a rewrite. This design choice is the only Phase-1 obligation toward Phase 2.

## 13. Migration

- New install path: `helm install`. Documented migration guide (raw manifest → chart) in the chart README.
- The `CSIDriver` object and driver name (`com.hammerspace.csi`) are cluster singletons; the README warns that the chart and the raw manifests must not be applied to the same cluster simultaneously (they would fight over the same `CSIDriver`).

## 14. Acceptance criteria

1. `helm install`/`helm upgrade` of the chart produces a working driver on a supported cluster (validated manually by a maintainer, outside CI).
2. **Default render is behavior-equivalent to `kubernetes-1.36/plugin.yaml`** — golden-manifest diff passes.
3. All static gates (lint, template, schema, kubeconform, golden diff, `trivy config`) pass in `make chart-test` with no live cluster.
4. Every improvement from §8 is reachable via a documented value and defaults off.
5. The adversarial review produces a findings report and all Critical/High findings are resolved.
6. `values.schema.json` rejects the documented negative test cases.
7. README documents install, upgrade, every value, the immutability caveats, and the manifest→chart migration.

## 15. Decisions log

- **TLS verify defaults to `false`** (preserve today) — per maintainer, harden after testing.
- **attacher stays enabled / `attachRequired: true`** (preserve today) — reduce after further testing.
- **No publishing** in Phase 1.
- **Testing must not require deploying a cluster** — all gates static.
- **Operator deferred to Phase 2**, triggered by OpenShift certification or enterprise lifecycle need.
- **Dropped the dead `csi-provisioner-volumeattachment-status-binding`** (Task 12 adversarial
  review) — **superseded by the v1.3.0 re-sync, see below.** The v1.2.x manifest carried a
  `ClusterRoleBinding` whose `roleRef` pointed at `ClusterRole/csi-provisioner-volumeattachment-status`,
  which was defined nowhere — so the binding granted nothing (a RoleBinding to a non-existent role is a
  no-op). The chart originally dropped it. v1.3.0 instead repurposed that binding into a functional
  grant, so the chart now renders the real resource rather than dropping a dead one. See
  `charts/hammerspace-csi/tests/ADVERSARIAL-REVIEW.md`.
- **Wired previously-dead value knobs** (Task 12). `serviceAccount.controllerName`/`nodeName` now
  thread into the StatefulSet/DaemonSet `serviceAccountName` (previously hardcoded — an override
  produced pods bound to a non-existent SA); `image.pullSecrets` and `priorityClassName` now also
  apply to the node DaemonSet (previously controller-only, contradicting their docs);
  `hammerspace.dataPortalMountPrefix` → `HS_DATA_PORTAL_MOUNT_PREFIX` and `metrics.otlpEndpoint` →
  `OTEL_EXPORTER_OTLP_ENDPOINT` env vars are now emitted (env names confirmed in `main.go`). All are
  guarded so the default render stays byte-identical to the golden manifest.
- **`hammerspace.existingSecret` given `minLength: 1`** (Task 12) so `secret.create=false` with an
  empty name is rejected at schema-validation time instead of rendering an invalid
  `secretKeyRef.name: ""`.
- **Deferred (not fixed) in Task 12:** `commonAnnotations` remains declared-but-unwired — it needs a
  symmetric annotations helper across ~15 resources (too invasive for a review commit) and defaults to
  empty, so it does not affect behavior today. (`hammerspace.logLevel` was also deferred here but has
  since been wired — see the v1.3.0 re-sync entry below.)
- **v1.3.0 re-sync** (rebased onto `master` after v1.3.0 landed). The branch was rebased onto the
  v1.3.0 `master`, whose `kubernetes-1.36/plugin.yaml` had changed, and the chart was reconciled to
  stay behavior-equivalent: (a) `LOG_LEVEL` env (default `info`) now rendered on both driver
  containers, wiring the previously-deferred `hammerspace.logLevel` knob (schema tightened to the
  documented level enum); (b) the new `csi-provisioner-snapshot-content-status` ClusterRole + binding
  (volumesnapshotcontents/status for the snapshotter sidecar), which v1.3.0 introduced by repurposing
  the old dead binding. `tests/golden/expected-1.36.yaml` was re-derived from the v1.3.0 manifest and
  is now a verbatim copy (no resource added or removed); helm-unittest coverage was added for both.
  The archiving of the 1.29/1.34/1.35 manifests (§11) is deferred to a separate follow-up so this PR
  stays chart-only and additive.

## 16. References

- Current manifests: `deploy/kubernetes/kubernetes-1.36/plugin.yaml` (golden source).
- Prior work: driver adversarial code review; Trivy scan (HS-42779, dependency CVEs); competitive comparison + CSI feature/roadmap analysis.
- Upstream chart conventions: `kubernetes-csi/csi-driver-nfs`, `csi-driver-smb`.
