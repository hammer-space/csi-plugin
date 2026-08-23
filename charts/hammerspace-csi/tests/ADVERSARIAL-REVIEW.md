# Adversarial review — hammerspace-csi Helm chart (Task 12)

Genuine defect hunt across the whole chart, not a rubber-stamp. Findings are
listed most-severe first, each with `file:line`, severity, a concrete failure
scenario, and a disposition (`fixed` / `accepted-risk-by-design` / `deferred`).
All Critical/High findings are resolved. The chart's default render stays
byte-identical to the shipped manifest (`tests/golden/expected-1.36.yaml`); every
fix below is guarded so it renders nothing at default values.

Verification anchors: `make chart-test` (helm lint, 21 helm-unittest cases,
golden gate, kubeconform 1.29/1.34/1.35/1.36, trivy regression gate, helm-docs
drift) is green after the fixes. Trivy default-vs-hardened comparison proves the
hardening toggles reduce the attack surface (see §3).

---

## Findings

### F1 — Dead `csi-provisioner-volumeattachment-status-binding` (RBAC hygiene) — HIGH — FIXED
- **Where:** `templates/rbac-controller.yaml` (removed; was the 3rd doc);
  `tests/golden/expected-1.36.yaml` (removed to match).
- **Scenario:** The shipped manifest carried a `ClusterRoleBinding` whose
  `roleRef` named `ClusterRole/csi-provisioner-volumeattachment-status` — a role
  defined **nowhere** in the manifest or chart. A RoleBinding to a non-existent
  role grants nothing, so the binding was a functional no-op that misleads
  auditors into thinking a dedicated role exists. Its intended
  `volumeattachments` + `volumeattachments/status` permissions are **already**
  granted by the `csi-provisioner` ClusterRole (`rbac-controller.yaml` rule
  `storage.k8s.io: volumeattachments, volumeattachments/status → get,list,watch,update,patch`),
  which the `csi-provisioner` ServiceAccount is bound to via the working
  `csi-provisioner` binding. Verified in the rendered controller ClusterRole
  before removal.
- **Fix:** Removed the dead binding from the template and from
  `expected-1.36.yaml`; documented as the one intentional divergence in that
  file's header ("chart drops the broken no-op ...; permissions already granted
  by the csi-provisioner ClusterRole; behavior unchanged"). Behavior unchanged —
  golden gate stays green.

### F2 — `serviceAccount.controllerName`/`nodeName` overrides silently break RBAC — HIGH — FIXED
- **Where:** `templates/controller-statefulset.yaml:19` (`serviceAccountName`),
  `templates/node-daemonset.yaml:17` (`serviceAccount`).
- **Scenario:** The ServiceAccount objects (`serviceaccounts.yaml`) and the
  ClusterRoleBinding subjects (`rbac-*.yaml`) honored
  `serviceAccount.controllerName`/`nodeName`, but the StatefulSet/DaemonSet
  **hardcoded** `csi-provisioner` / `csi-node`. Setting
  `serviceAccount.controllerName=foo` therefore created SA `foo` and bound `foo`,
  while the controller pod still ran as `csi-provisioner` — a SA that no longer
  exists (with `create=true`) or has no RBAC binding. Result: pods run with no /
  the wrong RBAC → provisioning, attach, snapshot all fail. An advertised,
  schema-typed value produced a silently broken deployment.
- **Fix:** Threaded `{{ .Values.serviceAccount.controllerName }}` /
  `{{ .Values.serviceAccount.nodeName }}` into the workloads. Defaults
  (`csi-provisioner`/`csi-node`) render identically → golden-safe; overrides now
  work end-to-end (verified).

### F3 — `image.pullSecrets` never applied to the node DaemonSet — HIGH — FIXED
- **Where:** `templates/node-daemonset.yaml` (was missing; `image.pullSecrets`
  was only consumed by `controller-statefulset.yaml:155`).
- **Scenario:** `image.pullSecrets` is documented as "imagePullSecrets for the
  driver image" (not controller-only). The node DaemonSet pulls the **same**
  `hammerspaceinc/csi-plugin` image. With a private registry, an operator sets
  `image.pullSecrets`, the controller pulls fine, but **every node pod**
  `ImagePullBackOff`s → the driver cannot mount volumes on any node, i.e. it is
  cluster-wide non-functional. The controller-only wiring hides this until a
  private registry is used.
- **Fix:** Added the `{{- with .Values.image.pullSecrets }}` block to the node
  DaemonSet pod spec. Empty default → renders nothing → golden-safe.

### F4 — `metrics.otlpEndpoint` is a dead knob (OTLP export has no endpoint) — MEDIUM — FIXED
- **Where:** `values.yaml:126`, consumed by no template before the fix; driver
  reads `OTEL_EXPORTER_OTLP_ENDPOINT` (`main.go:97,135`).
- **Scenario:** `metrics.tracesExporter: otlp` sets `OTEL_TRACES_EXPORTER=otlp`,
  but `metrics.otlpEndpoint` was never emitted as `OTEL_EXPORTER_OTLP_ENDPOINT`,
  so the OTLP exporter had no collector address — traces silently go nowhere. The
  value looked wired but did nothing.
- **Fix:** Emit `OTEL_EXPORTER_OTLP_ENDPOINT` on both driver containers when
  `metrics.otlpEndpoint` is set (env name confirmed in `main.go`). Empty default
  → golden-safe.

### F5 — `hammerspace.dataPortalMountPrefix` is a dead knob — MEDIUM — FIXED
- **Where:** `values.yaml:37`, consumed by no template before the fix; driver
  reads `HS_DATA_PORTAL_MOUNT_PREFIX` (`main.go:192`
  → `common.DataPortalMountPrefix`).
- **Scenario:** Documented as "prefix prepended to Data Portal mount paths," but
  the env var the driver reads was never set, so the value had zero effect.
- **Fix:** Emit `HS_DATA_PORTAL_MOUNT_PREFIX` on both driver containers when set.
  Empty default → golden-safe.

### F6 — `priorityClassName` not applied to the node DaemonSet — MEDIUM — FIXED
- **Where:** `templates/node-daemonset.yaml` (was missing; consumed only by
  `controller-statefulset.yaml:152`). Docs (`values.yaml:15`, README) say
  "applied to the controller **and node** pods."
- **Scenario:** An operator sets a high-priority class expecting the critical
  node CSI pods (which perform every mount) to resist eviction under node
  pressure; only the controller got it. Node pods stayed default-priority and
  could be evicted first, breaking mounts — the opposite of intent, and a
  documented-contract violation.
- **Fix:** Added the `{{- with .Values.priorityClassName }}` block to the node
  DaemonSet. Empty default → golden-safe. Makes README's "controller and node"
  claim true.

### F7 — `existingSecret: ""` renders an invalid `secretKeyRef.name: ""` — MEDIUM — FIXED
- **Where:** `values.schema.json` (`hammerspace.existingSecret`);
  `_helpers.tpl:27` (`secretName`) uses `existingSecret` verbatim when
  `secret.create=false`.
- **Scenario:** With `secret.create=false` and `existingSecret=""`, every
  `secretKeyRef.name` renders empty → the API server rejects the pod env, pods
  stuck in `CreateContainerConfigError`, with no clear pointer to the cause.
  (Note the asymmetry: when `secret.create=true`, the helper `default`s an empty
  `existingSecret` to `com.hammerspace.csi.credentials`; when `false` it does
  not.)
- **Fix:** Added `"minLength": 1` to `hammerspace.existingSecret` in the schema.
  Empty now fails fast at `helm template`/`install` with a clear message
  (`'/hammerspace/existingSecret': minLength: got 0, want 1`). Verified the
  non-empty default still validates.

### F8 — `commonAnnotations` is a dead knob — MEDIUM — DEFERRED
- **Where:** `values.yaml:13`, `values.schema.json`; consumed by **no** template
  (the sibling `commonLabels` IS wired via the `labels` helper).
- **Scenario:** Documented "Extra annotations applied to every resource this
  chart renders," but setting it has zero effect.
- **Disposition — deferred (non-blocking):** The honest fix is a symmetric
  `hammerspace-csi.annotations` helper consumed by ~15 resource `metadata`
  blocks (each guarded so an empty map does not emit `annotations: {}` and
  drift the golden gate). That is a feature-completion change spanning every
  template — too invasive and error-prone to slip into an adversarial-review
  commit, and it warrants its own reviewed task. Default is `{}` (no behavioral
  impact) and no test/consumer depends on it. **Recommendation:** either wire the
  symmetric helper or remove the knob from `values.yaml`/schema/README.

### F9 — `hammerspace.logLevel` is a dead knob — MEDIUM — DEFERRED
- **Where:** `values.yaml:35`; consumed by no template.
- **Scenario:** Documented "Driver log level (e.g. DEBUG, INFO)," but no
  `LOG_LEVEL`/log-level env var is set on the containers.
- **Disposition — deferred (non-blocking):** The driver currently reads **no**
  log-level env var (`grep os.Getenv` over `pkg/`+`main.go` shows none), so there
  is nothing correct to wire it to yet. Per the project's own
  `csi-log-level-validation-pending` note, the LOG_LEVEL change is explicitly
  gated on comparing INFO vs DEBUG on a real cluster before shipping. Wiring it
  now would be speculative. Default is empty → no behavioral impact.
  **Recommendation:** wire `logLevel → LOG_LEVEL` in the same change that adds
  driver-side support, after the pending cluster validation.

### F10 — Doc wording: freezer `pods/exec` is a controller grant, not "node ServiceAccount" — LOW — DEFERRED
- **Where:** `values.yaml:113` (freezer comment), README rows generated from it
  (README `freezer.enabled`, "Common tweaks").
- **Scenario:** The docs say `freezer.enabled` grants **the node ServiceAccount**
  `pods/exec`. In fact the `pods/exec` rule lives on the **`csi-provisioner`
  (controller) ClusterRole** (`rbac-controller.yaml`), exercised by the
  controller during `CreateSnapshot` (`controller.go:1465` → `freezer.go`, which
  execs into the on-node `csi-node` pod via the k8s `pods/exec` API using the
  controller SA's credentials — RBAC is about the caller, not the exec target).
  The grant location is correct; only the doc wording is wrong, so it could
  misdirect a least-privilege audit.
- **Disposition — deferred (cosmetic):** Fixing the wording means editing the
  `values.yaml` comment + `README.md.gotmpl` and regenerating the README, which
  couples doc-generation churn into the RBAC-fix commit for no functional gain.
  **Recommendation:** in a docs pass, change "node ServiceAccount" →
  "controller ServiceAccount (`csi-provisioner` ClusterRole)".

### F11 — `node.kubeletDir` with a trailing slash yields `//` paths — LOW — ACCEPTED
- **Where:** `templates/node-daemonset.yaml` (`{{ .Values.node.kubeletDir }}/...`
  and `mountPath: {{ .Values.node.kubeletDir }}/`).
- **Scenario:** Default `/var/lib/kubelet` (no trailing slash) is correct and
  matches golden. If a user sets `node.kubeletDir=/var/lib/kubelet/`, paths
  become `/var/lib/kubelet//plugins_registry` etc. Double slashes are
  path-harmless on Linux and the mounts still resolve, but it is untidy.
- **Disposition — accepted:** No functional impact; a `pattern` constraint
  forbidding a trailing slash risks over-constraining edge cases (`/`). The
  values comment already implies a directory path. Left as-is; low value to
  change.

---

## Review dimensions — results

### 1. Templating correctness — PASS
- Rendered `ci/default`, `ci/hardened`, `ci/block`, plus a bare
  `--set hammerspace.endpoint=...` minimal render (no `minimal-values.yaml`
  exists). All render without error.
- **Numeric/bool coercion:** `HS_TLS_VERIFY` (`| quote` → `"false"`/`"true"`),
  ConfigMap timeouts (`| quote`), and `OTEL_METRICS_PROMETHEUS_LISTEN`
  (`printf ":%v" ... | quote` → `":9090"`) are all quoted → no YAML coercion.
  `csidriver.yaml` intentionally emits **unquoted** booleans
  (`podInfoOnMount`/`storageCapacity`/`attachRequired`) and integers
  (`replicaCount`, metrics ports) where the K8s schema wants scalars — correct.
- **nil-deref:** `csiDriver.attachRequired` nil is guarded
  (`{{- if ne ... nil }}` in the template, `{{ if eq ... nil }}` in NOTES).
- **nindent/toYaml:** `commonLabels` multi-key map renders at the correct
  indentation under `.metadata.labels` (verified with two extra labels); every
  `toYaml . | nindent N` block (`resources`, `nodeSelector`, `tolerations`,
  `affinity`, `imagePullSecrets`, storageclass `parameters`/`mountOptions`)
  checked for indentation.
- **range scoping:** `storageclass.yaml` uses `$sc`/`$` correctly (the labels
  helper is called with `$`, not the loop var). Fixed the `allowVolumeExpansion`
  `with`→`hasKey` footgun (below).

### 2. RBAC scope — PASS (one dead binding removed, see F1)
- **No over-grant:** `secrets` is granted only `verbs: ["get"]` with
  `resourceNames: ["com.hammerspace.csi.credentials"]` on both the controller and
  node ClusterRoles — no cluster-wide `list`/`watch` on secrets. No `"*"` verb or
  resource anywhere in any render (default/hardened/block).
- **No under-grant:** provisioner has `persistentvolumes` (full CRUD),
  `persistentvolumeclaims`(+`/status`), `storageclasses`; snapshotter has all
  three `snapshot.storage.k8s.io` resources
  (`volumesnapshotclasses`/`volumesnapshotcontents`/`volumesnapshots`); resizer
  has `persistentvolumeclaims` + `persistentvolumeclaims/status` with
  `update`/`patch`. Node role has PV/PVC/nodes/events as needed.
- **`freezer.enabled=false` drops ONLY `pods/exec`:** a full RBAC-rule diff of
  the default vs hardened render shows the *only* rule difference is the removal
  of `pods/exec → create`; everything else is identical.

### 3. Security-misconfig regression (trivy) — PASS (toggles measurably help)
`trivy config --severity HIGH,CRITICAL` on the DEFAULT vs HARDENED render:

| trivy check | default | hardened | driver of the reduction |
|---|---|---|---|
| KSV-0005 (SYS_ADMIN cap) | 2 | 2 | intrinsic |
| KSV-0009 (host network) | 2 | 2 | intrinsic |
| KSV-0014 (root FS not read-only) | 8 | 7 | `attacher.enabled=false` drops a container |
| KSV-0017 (privileged) | 3 | 3 | intrinsic |
| KSV-0024 (host ports) | 8 | 7 | `attacher.enabled=false` drops a container |
| KSV-0041 (ClusterRole manages secrets) | 2 | 2 | intrinsic (scoped `get`) |
| KSV-0053 (ClusterRole can exec into pods) | **1** | **0** | `freezer.enabled=false` drops `pods/exec` |
| KSV-0056 (manages networking/events RBAC) | 1 | 1 | intrinsic |
| KSV-0118 (default security context) | **7** | **0** | `securityContext.seccompProfile.enabled=true` |
| KSV-0121 (hostPath volumes) | 1 | 1 | intrinsic |
| **TOTAL HIGH/CRITICAL instances** | **35** | **25** | **−10 (28% fewer)** |

Hardened has **measurably fewer** HIGH/CRITICAL findings (35 → 25; 10 → 8 unique
IDs). The reduction is driven by real toggles — seccomp (`KSV-0118` 7→0),
freezer-off (`KSV-0053` 1→0), and attacher-off (`KSV-0014`/`KSV-0024` each −1) —
proving the hardening switches actually shrink the attack surface, not just
rename it. The `default` render's HIGH/CRITICAL id set is unchanged by the F1–F9
fixes (still the 10 baselined ids), so `tests/trivy-baseline.txt` needs no edit.

### 4. CSIDriver immutability upgrade traps — PASS
`README.md` "Immutability caveats" section + per-value warnings, and `NOTES.txt`
"IMMUTABILITY WARNING", both state that changing `csiDriver.attachRequired` /
`csiDriver.fsGroupPolicy` on an existing release requires
`kubectl delete csidriver com.hammerspace.csi` before `helm upgrade`. NOTES also
notes the StatefulSet/DaemonSet `matchLabels` immutability.

### 5. Secret leakage — PASS
Rendered with `--set hammerspace.secret.create=true
--set hammerspace.secret.username=SEKRET_USER
--set hammerspace.secret.password=SEKRET_PASS --set hammerspace.endpoint=...`.
`SEKRET_USER`/`SEKRET_PASS` appear **only** in the `Secret` object's `stringData`
— nowhere in any ConfigMap, container env (the workloads use `secretKeyRef`, not
literals), annotation, or NOTES. NOTES prints the endpoint URL (config summary,
non-secret) but never the username/password.

### 6. helm upgrade safety — PASS
StatefulSet/DaemonSet `spec.selector.matchLabels` are the stable literals
`app: csi-provisioner` / `app: csi-node`, derived from no value — nothing an
operator can set perturbs them, so no value change can produce an immutable-field
upgrade rejection there. Workload/Service **names** are also stable literals (not
value-driven). The one value-driven field that *would* have broken an install if
changed — the SA name — is fixed in F2 (it never touched an immutable selector,
but it did break the SA reference). The CSIDriver immutable fields are covered by
F4-dimension docs above.

### 7. Cross-K8s-version render — PASS
`make chart-test` runs `kubeconform -strict` for 1.29/1.34/1.35/1.36 across all
`ci/*` presets — green after fixes.

### 8. Drift from tested manifest — PASS
Golden gate green. After the F1 removal, the render's only remaining divergences
from the raw `plugin.yaml` are (a) the four documented cosmetic
`app.kubernetes.io/*` + `helm.sh/chart` labels (stripped by `normalize.sh`), and
(b) the removed dead binding (removed from `expected-1.36.yaml` too and documented
in its header). No functional drift.

---

## Deferred-minors triage (from the plan ledger)

| Item | Verdict | Note |
|---|---|---|
| `existingSecret: ""` → invalid `secretKeyRef.name` | **fix-now (done, F7)** | added `minLength: 1`; default still validates |
| `storageclass allowVolumeExpansion` `with` drops explicit `false` | **fix-now (done)** | switched to `hasKey $sc "allowVolumeExpansion"`; explicit `false` now emitted (verified); golden unaffected (no storageClasses in default) |
| metrics-gate helm-unittest coverage | **accepted** | test-coverage gap, not a chart defect; ServiceMonitor + metrics toggles are exercised via renders in `extras`/validate. Recommend adding a focused unittest later |
| `kubeletDir` trailing-slash → `//` | **accepted (F11)** | path-harmless; documented |
| `secretName` trim-symmetry | **addressed via F7** | empty `existingSecret` now rejected by schema, removing the asymmetric-empty path |
| normalize.sh `commonLabels` calibration | **accepted** | default `commonLabels: {}` → no golden drift; helper indentation verified |
| helm-docs `null` type on `csiDriver.attachRequired` | **accepted** | cosmetic ("string"/`nil` in the README table); README is generated, no functional impact |

---

## Fixes applied (all keep `make chart-test` green; default render byte-identical)

1. **F1** removed dead `csi-provisioner-volumeattachment-status-binding` from
   `templates/rbac-controller.yaml` **and** `tests/golden/expected-1.36.yaml`
   (header documents the divergence).
2. **F2** `serviceAccountName`/`serviceAccount` now use
   `serviceAccount.controllerName`/`nodeName`.
3. **F3** `image.pullSecrets` added to the node DaemonSet.
4. **F4** `OTEL_EXPORTER_OTLP_ENDPOINT` emitted from `metrics.otlpEndpoint`.
5. **F5** `HS_DATA_PORTAL_MOUNT_PREFIX` emitted from
   `hammerspace.dataPortalMountPrefix`.
6. **F6** `priorityClassName` added to the node DaemonSet.
7. **F7** `hammerspace.existingSecret` given `"minLength": 1` in the schema.
8. `storageclass.yaml` `allowVolumeExpansion` `with`→`hasKey`.

Deferred: **F8** (`commonAnnotations` dead knob), **F9** (`logLevel` dead knob,
gated on cluster validation), **F10** (freezer doc wording), **F11**
(`kubeletDir` trailing slash) — all non-blocking, defaults have no behavioral
impact, recommendations recorded above.
