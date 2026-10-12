# Hammerspace CSI Operator

Initial OpenShift implementation for the existing `com.hammerspace.csi` driver.
Supports deployment configuration for NFS, raw block PVCs, and ext4/xfs
file-backed volumes. It is not yet a certified or cluster-tested release.

## What is implemented

- Cluster-scoped `HammerspaceCSIDriver/cluster` API and schema validation.
- Leader election and reconciliation every 15 seconds in `hammerspace-csi`.
- Controller StatefulSet, node DaemonSet, service accounts, RBAC, configuration,
  CSIDriver, an operand-specific SCC, and optional StorageClasses.
- Explicit release image configuration, drift repair, credential rotation using
  Secret UID/resourceVersion, and rollout-aware status conditions.
- Refusal to adopt existing resources owned by another installation. The existing
  manual driver must be migrated separately; applying the example CR over it
  reports OwnershipConflict.
- Ordered cleanup of owned workloads before permissions; no deletion of PVCs,
  PVs, backend data, credentials Secrets, or the operand namespace. Removing the
  CR still stops storage service: migrate/drain workloads before removal.
- Local OLM bundle and file-based catalog generation from the same deployment
  and RBAC inputs. No registry push or certification submission is automatic.

The first release fixes the namespace to `hammerspace-csi`. StorageClass
definitions are immutable after CR creation. Generated classes use
WaitForFirstConsumer, Retain and expansion enabled. Classes are not defaults.
Capacity publication is enabled in CSIDriver and the provisioner. It uses the
driver's GetCapacity implementation; use the matching driver build so old
capacity-reporting behavior does not leave claims unschedulable.

## Storage modes

| CR mode | StorageClass parameters | PVC mode | Data path |
| --- | --- | --- | --- |
| NFS | fsType=nfs | Filesystem | Independent NFS share |
| Block | blockBackingShareName | Block | Backing file on NFS exposed through a host loop device |
| ext4 | fsType=ext4, mountBackingShareName | Filesystem | ext4 on a file-backed loop device |
| xfs | fsType=xfs, mountBackingShareName | Filesystem | xfs on a file-backed loop device |

Raw block does not imply multi-node concurrent-write support. Start with
ReadWriteOnce and validate the intended access modes. Kubernetes selects raw
block through the PVC's `volumeMode: Block`, not a StorageClass fsType flag.

Linux nodes need NFS client support and `/dev/loop-control`/loop-device support;
the node operand retains privileged host `/dev` access and bidirectional kubelet
mount propagation. Both controller and node driver containers require privileged
mount operations. Only their service accounts are listed in the dedicated SCC;
the Operator itself runs without privilege or added capabilities. Nodes using
the observed pNFS flexfiles backend also need nfsv3 loaded. No MachineConfig or
kernel-module changes are applied by this Operator. Verify node prerequisites
before block testing, including enough free loop devices for the workload.

The Operator's Kubernetes RBAC includes the explicit permissions delegated to
the driver roles, because the API server checks these when it creates those
roles. It does not receive an unrestricted RBAC `escalate` grant. Its code limits
management and deletion to the declared Operator-owned operands.
`config/rbac.yaml` is maintained by hand, so `generate-operands` checks that its
ClusterRole holds every permission granted by the operand ClusterRoles in
`plugin.yaml`. The check runs on every `make generate`, `make build`,
`make test` and image build, and fails with the missing grants listed.

**Security note.** Because the Operator creates the driver's ClusterRoles
without `escalate`, its own service account must hold every permission those
roles grant. The broadest standing grants are `create` on `clusterroles` and
`clusterrolebindings` (Kubernetes cannot scope `create` by name), cluster-wide
`create` on `pods/exec` (used by the driver to freeze filesystems during
snapshots), and full control of PersistentVolumes. The grants are bounded: no
`bind` or `escalate`, Secrets are `get`-only in `hammerspace-csi`, and the
custom resource is limited to the `cluster` name. Even so, anyone who
compromises the Operator pod gains control of cluster storage and can exec into
any pod. Treat the Operator's namespace and service account as
cluster-admin-equivalent when deciding who may access them.

**Host ports.** Both operands use `hostNetwork`, so their ports open on each
node's IP: controller metrics `9090` and liveness `29791`, node metrics `9091`
and liveness `29793`. The liveness ports avoid the upstream `livenessprobe`
default `9808`, which other CSI drivers also bind. If something else on a node
uses one of these ports, the driver pod stays Pending on the port conflict. To
change a liveness port, edit the sidecar's `--health-port` argument and the
matching `healthz` `containerPort`/`hostPort`. To change a metrics port, edit
`OTEL_METRICS_PROMETHEUS_LISTEN` and the matching `metrics` port. Make the edits
in `plugin.yaml`, then run `make generate`. The dedicated SCC sets
`allowHostPorts: true` and `allowHostNetwork: true`. `/healthz` reports only
whether the driver process is answering. It does not contact the Anvil.

The driver image must include losetup, mkfs.ext4 and mkfs.xfs as appropriate.
Block and filesystem-backed expansion, snapshots, restoration, restart
persistence and loop-device cleanup require separate functional validation.

## Build and test

From this directory, with Go 1.25 or newer and Python 3 with PyYAML:

```sh
make test
make build
make image IMAGE=hammerspace-csi-operator:dev
```

The separate module pins client-go v0.33.6; it does not import or modify the
driver's dependency graph. The development image uses UBI micro at runtime;
base-image digest pins, license/release metadata, scans and container
certification are still release work.

The single source for CSI workloads and RBAC is
[`deploy/kubernetes/kubernetes-1.36/plugin.yaml`](../deploy/kubernetes/kubernetes-1.36/plugin.yaml).
Edit that manifest, then run `make -C operator generate` from the repository
root. Commit the resulting `internal/operator/operands.json` together with the
manifest; the JSON is generated build input, not a second configuration to
maintain. The same command derives `config/development-images.json` and the
manager's `RELATED_IMAGE_*` defaults from that manifest, so image updates cannot
silently differ between manual, Operator and development bundle installs. `make build` regenerates it, while `make test` rejects stale output.
The image build regenerates it inside Docker from the same source manifest:
run `make image` here, or `docker build -f operator/Dockerfile .` from the root.

Operator-specific defaults live in `internal/manifests/generate.go`: isolated
RBAC names, removal of unnecessary Secret/CRD permissions, Linux scheduling,
resource requests, pull policy, registrar cleanup and persistent Hammerspace
host directories. Namespace, release images, credentials and CR settings are
still supplied by the renderer. The generator preserves the existing Operator
defaults; changes to the shared manifest propagate automatically. Older
versioned Kubernetes manifests remain separate compatibility snapshots.

Tests use a fake Kubernetes client. They cover storage rendering, invalid
configuration, ownership conflicts, repeated reconciliation, drift, image
updates, Secret rotation, rollout readiness and cleanup. The fake client does
not verify server-side apply semantics, admission or garbage collection. The
CRD passed an OpenShift server-side dry run; a clean cluster installation is
still required before considering this implementation validated.

## Development installation

Use a separate test cluster or a planned migration of the manual driver. Make
the built Operator image available to the cluster and change its image in
`config/manager.yaml`. First build the matching driver with `make build-release`
from the repository root and make that tagged image available to the test
cluster. The default driver tag follows the current manual manifest and must
include this branch's capacity and snapshot fixes; using an older driver with
capacity publication enabled can prevent scheduling. To select a different
registry/tag, edit the canonical manifest and run `make generate`. Its seven
operand images propagate to the manager and development bundle inventory.
These development defaults are not certification claims. For release bundles,
use a separate digest-pinned image inventory via `--images`.

```sh
oc create namespace hammerspace-csi
oc apply -f config/crd.yaml
oc apply -f config/rbac.yaml
oc apply -f config/manager.yaml
```

Provision an existing Secret named `com.hammerspace.csi.credentials` in
`hammerspace-csi` with nonempty `username`, `password`, and `endpoint` keys using
your normal secret-management process. The Operator only reads Secrets in its
namespace; it does not copy or create them. TLS verification defaults to true.
Configure trusted backend certificates, or explicitly set `tlsVerify: false`
for a development backend that requires it.

Review the backing share names in `examples/driver.yaml` before creation:

```sh
oc apply -f examples/driver.yaml
oc get hscsi cluster -o yaml
oc -n hammerspace-csi get pods
```

For a raw block smoke check, apply the two examples in an application test
namespace. The pod writes a marker to the first sector of its new test PVC;
never point it at an existing volume containing data. WaitForFirstConsumer
means the PVC can remain Pending until the pod is created.

```sh
oc -n YOUR_TEST_NAMESPACE apply -f examples/block-pvc.yaml -f examples/block-pod.yaml
oc -n YOUR_TEST_NAMESPACE wait --for=jsonpath='{.status.phase}'=Succeeded pod/hammerspace-block-example --timeout=5m
oc -n YOUR_TEST_NAMESPACE logs hammerspace-block-example
```

The example Pod's device permissions must be validated under the application's
actual SCC. Do not grant the application's account the driver's privileged SCC
as a shortcut. Retain-policy test PVs/backend files require deliberate cleanup.

## Generate an OLM bundle

With Python 3 and PyYAML, supply the image references you built:

```sh
python3 hack/build_bundle.py \
  --operator-image YOUR_REGISTRY/hammerspace-csi-operator:0.1.1 \
  --bundle-image YOUR_REGISTRY/hammerspace-csi-operator-bundle:0.1.1
```

This creates ignored `bundle/` containing the CSV, CRD, annotations, bundle
Dockerfile and `catalog.json`. It supports OwnNamespace installation in
`hammerspace-csi` only. The namespace must permit the privileged operands. The
CSV's `operatorframework.io/suggested-namespace-template` annotation creates it
with the `pod-security.kubernetes.io/enforce: privileged` label from
`config/manager.yaml` during a console install. For CLI installs, create and
label it as shown in `config/manager.yaml` first.
The CSV lists all eight images, including the Operator, under relatedImages.
Use `--images` with a release image JSON file, `--release`, and an explicit
`--openshift-versions` range to require digest pins. Every sidecar must be listed
in `spec.relatedImages` and included in certification review. Digest-pinned
upstream sidecars are allowed as generator inputs; Red Hat payload sidecars can
be selected instead when appropriate for the target release. See
[`config/certified-images.example.json`](config/certified-images.example.json)
and [`../docs/redhat-certification.md`](../docs/redhat-certification.md).
That switch validates inputs; it does not certify them. The generated catalog starts an alpha channel and has
no upgrade edge until an upgrade path is tested.

Run Operator SDK bundle validation, `opm validate`, container certification
preflight, OLM installation and upgrade tests before publishing. These tools and
full OLM validation are not replaced by the generator's local tests.

## Remaining certification work

Perform clean-cluster installation, server-side apply/drift and SCC checks,
upgrade/uninstall/reinstall tests and workload data-retention tests. Add separate
NFS, raw-block, ext4 and xfs certification capability manifests based on measured
support. Do not turn the existing NFS-only manifest into a block test profile.
Run raw-block write/read, pod/node restart, expansion, snapshot/restore and
reclaim tests; validate multi-node behavior and Virtualization separately.

Red Hat requires certification/publication of the Operator and referenced
containers before CSI certification, and driver installation through the
Operator for the CSI run:
[Red Hat CSI workflow](https://docs.redhat.com/en/documentation/red_hat_software_certification/2026/html/red_hat_software_certification_workflow_guide/con_csi-certification_openshift-sw-cert-workflow-working-with-container-network-interface).
