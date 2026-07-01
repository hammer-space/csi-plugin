#!/usr/bin/env bash
set -euo pipefail

kubectl_bin="${KUBECTL:-kubectl}"
namespace=""
storage_class=""
snapshot_class=""
service_account=""
job=""
kubectl_image="${KUBECTL_IMAGE:-docker.io/bitnami/kubectl:latest}"
backing_share="${BACKING_SHARE:-k8s-file-storage}"
block_backing_share="${BLOCK_BACKING_SHARE:-k8s-block-storage}"
objectives="${OBJECTIVES:-keep-online}"
export_options="${EXPORT_OPTIONS:-*,RW,false}"
fs_type="${FS_TYPE:-ext4}"
volume_size="${VOLUME_SIZE:-1Gi}"
timeout="${TIMEOUT:-10m}"
preflight_only="false"
cleanup_first="true"
render_only="false"
scenario="${SCENARIO:-file-backed}"

# Snapshot CRD/controller install commands, if this script fails at the
# "Preflight: snapshot API" check.
#
# Use a version compatible with the target Kubernetes cluster. v8.5.0 supports
# Kubernetes 1.25+ and installs the GA snapshot.storage.k8s.io/v1 API used by
# this demo.
SNAPSHOTTER_VERSION=v8.5.0
# kubectl -- apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml"
# kubectl -- apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml"
# kubectl -- apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml"
# kubectl -- apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml"
# kubectl -- apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml"
# For minikube, replace "kubectl" with "minikube kubectl --".

usage() {
  cat <<EOF
Usage: $0 [options]

Self-contained Hammerspace CSI snapshot/restore demo for supported scenarios:
  - file-backed
  - nfs-share
  - nfs-mount-backing
  - block
  - all (runs every scenario sequentially)

The script creates:
  - Namespace
  - StorageClass
  - VolumeSnapshotClass
  - RBAC for the demo Job
  - A Job that creates a source PVC, writes a marker, snapshots it, restores a
    new PVC from the snapshot, and verifies the marker on the restored PVC.

Options:
  --kubectl CMD            kubectl command to use. Example: "minikube kubectl --"
  --scenario NAME          Scenario to run: file-backed, nfs-share, nfs-mount-backing, block, or all. Default: ${scenario}
  --namespace NAME         Namespace for demo resources. Default depends on the scenario
  --storage-class NAME     StorageClass name. Default depends on the scenario
  --snapshot-class NAME    VolumeSnapshotClass name. Default depends on the scenario
  --job NAME               Demo Job name. Default depends on the scenario
  --kubectl-image IMAGE    kubectl image used by the in-cluster Job.
                           Default: ${kubectl_image}
  --backing-share NAME     Hammerspace mountBackingShareName for filesystem-backed scenarios. Default: ${backing_share}
  --block-backing-share NAME  Hammerspace block backing share for block volumes. Default: ${block_backing_share}
  --objectives VALUE       Hammerspace objectives. Default: ${objectives}
  --export-options VALUE   exportOptions for NFS scenarios. Default: ${export_options}
  --fs-type TYPE           Filesystem type for file-backed volume. Default: ${fs_type}
  --volume-size SIZE       PVC size. Default: ${volume_size}
  --timeout DURATION       Wait timeout for the demo Job. Default: ${timeout}
  --preflight-only         Check the environment but do not run the demo.
  --render-only            Print the generated manifest and exit.
  --no-cleanup-first       Do not delete the existing demo Job before applying.
  -h, --help               Show this help.

Examples:
  $0 --kubectl "kubectl" --scenario file-backed --backing-share k8s-file-storage --objectives keep-online
  $0 --kubectl "kubectl" --scenario nfs-mount-backing --backing-share k8s-nfs-storage --objectives keep-online
  $0 --kubectl "kubectl" --scenario all

Tier 0 prerequisites:
  - Hammerspace CSI driver is installed and healthy.
  - Kubernetes snapshot CRDs and snapshot controller are installed.
  - Worker nodes can mount Hammerspace NFS v4.2 and have the filesystem tooling
    needed for the selected StorageClass.
  - The backing share/objective values are valid in the target Hammerspace env.

Note:
  - nfs-share restore clones the snapshot into the original source share and
    points the restored PVC at that cloned directory.
EOF
}

run_kubectl() {
  # Allows commands such as: --kubectl "minikube kubectl --"
  # shellcheck disable=SC2086
  ${kubectl_bin} "$@"
}

say() {
  printf '\n==> %s\n' "$*"
}

warn() {
  printf 'WARN: %s\n' "$*" >&2
}

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  printf 'See the snapshot CRD install command comments near the top of this script if the failure is about snapshot.storage.k8s.io.\n' >&2
  exit 1
}

set_defaults_for_scenario() {
  local scenario_name="$1"
  case "$scenario_name" in
    file-backed)
      namespace="${namespace:-hs-file-snapshot-e2e}"
      storage_class="${storage_class:-hs-file-snapshot-e2e}"
      snapshot_class="${snapshot_class:-hs-file-snapshot-e2e}"
      service_account="${service_account:-hs-file-snapshot-restore-e2e}"
      job="${job:-hs-file-snapshot-restore-e2e}"
      ;;
    nfs-share)
      namespace="${namespace:-hs-nfs-share-snapshot-e2e}"
      storage_class="${storage_class:-hs-nfs-share-snapshot-e2e}"
      snapshot_class="${snapshot_class:-hs-nfs-share-snapshot-e2e}"
      service_account="${service_account:-hs-nfs-share-snapshot-restore-e2e}"
      job="${job:-hs-nfs-share-snapshot-restore-e2e}"
      ;;
    nfs-mount-backing)
      namespace="${namespace:-hs-nfs-mount-backing-snapshot-e2e}"
      storage_class="${storage_class:-hs-nfs-mount-backing-snapshot-e2e}"
      snapshot_class="${snapshot_class:-hs-nfs-mount-backing-snapshot-e2e}"
      service_account="${service_account:-hs-nfs-mount-backing-snapshot-restore-e2e}"
      job="${job:-hs-nfs-mount-backing-snapshot-restore-e2e}"
      ;;
    block)
      namespace="${namespace:-hs-block-snapshot-e2e}"
      storage_class="${storage_class:-hs-block-snapshot-e2e}"
      snapshot_class="${snapshot_class:-hs-block-snapshot-e2e}"
      service_account="${service_account:-hs-block-snapshot-restore-e2e}"
      job="${job:-hs-block-snapshot-restore-e2e}"
      ;;
    all)
      ;;
    *)
      fail "unsupported scenario: ${scenario_name}"
      ;;
  esac
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --kubectl)
      [ "$#" -ge 2 ] || fail "--kubectl requires a command"
      kubectl_bin="$2"
      shift 2
      ;;
    --scenario)
      [ "$#" -ge 2 ] || fail "--scenario requires a name"
      scenario="$2"
      shift 2
      ;;
    --namespace)
      [ "$#" -ge 2 ] || fail "--namespace requires a name"
      namespace="$2"
      shift 2
      ;;
    --storage-class)
      [ "$#" -ge 2 ] || fail "--storage-class requires a name"
      storage_class="$2"
      shift 2
      ;;
    --snapshot-class)
      [ "$#" -ge 2 ] || fail "--snapshot-class requires a name"
      snapshot_class="$2"
      shift 2
      ;;
    --job)
      [ "$#" -ge 2 ] || fail "--job requires a name"
      job="$2"
      shift 2
      ;;
    --kubectl-image)
      [ "$#" -ge 2 ] || fail "--kubectl-image requires an image"
      kubectl_image="$2"
      shift 2
      ;;
    --backing-share)
      [ "$#" -ge 2 ] || fail "--backing-share requires a name"
      backing_share="$2"
      shift 2
      ;;
    --block-backing-share)
      [ "$#" -ge 2 ] || fail "--block-backing-share requires a name"
      block_backing_share="$2"
      shift 2
      ;;
    --objectives)
      [ "$#" -ge 2 ] || fail "--objectives requires a value"
      objectives="$2"
      shift 2
      ;;
    --export-options)
      [ "$#" -ge 2 ] || fail "--export-options requires a value"
      export_options="$2"
      shift 2
      ;;
    --fs-type)
      [ "$#" -ge 2 ] || fail "--fs-type requires a value"
      fs_type="$2"
      shift 2
      ;;
    --volume-size)
      [ "$#" -ge 2 ] || fail "--volume-size requires a value"
      volume_size="$2"
      shift 2
      ;;
    --timeout)
      [ "$#" -ge 2 ] || fail "--timeout requires a duration"
      timeout="$2"
      shift 2
      ;;
    --preflight-only)
      preflight_only="true"
      shift
      ;;
    --render-only)
      render_only="true"
      shift
      ;;
    --no-cleanup-first)
      cleanup_first="false"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

render_manifest() {
  local scenario_name="$1"
  local scenario_namespace="$2"
  local scenario_storage_class="$3"
  local scenario_snapshot_class="$4"
  local scenario_service_account="$5"
  local scenario_job="$6"
  local storage_class_comment=""
  local storage_class_parameters=""
  local access_modes="ReadWriteOnce"
  local volume_mode_block=""
  local writer_pod_manifest=""
  local reader_pod_manifest=""
  local writer_wait_command="wait_for_pod_succeeded writer 300
kubectl -n \"\${ns}\" logs pod/writer"
  local writer_after_snapshot_command=""
  local marker_prefix="file-backed"
  local scenario_label="file-backed"
  local scenario_description="file-backed"
  local pvc_name="src"
  local restore_pvc_name="restore"

  case "$scenario_name" in
    file-backed)
      storage_class_comment="File-backed snapshot restore demo volume"
      storage_class_parameters=$(cat <<EOF
  fsType: "${fs_type}"
  mountBackingShareName: ${backing_share}
  objectives: "${objectives}"
  deleteDelay: "0"
  volumeNameFormat: "csi-file-%s"
  additionalMetadataTags: "storageClassName=${scenario_storage_class},fsType=${fs_type}"
  comment: "File-backed snapshot restore demo volume"
EOF
)
      access_modes="ReadWriteOnce"
      marker_prefix="file-backed"
      scenario_label="file-backed"
      scenario_description="file-backed"
      writer_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: writer
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: writer
      image: busybox:1.36
      env:
        - name: MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          cat > /data/demo-payload.txt <<PAYLOAD
          Hammerspace CSI snapshot restore demo
          marker=${MARKER}
          file=demo-payload.txt
          validation=full-content-and-sha256
          PAYLOAD
          sed -i 's/^          //' /data/demo-payload.txt
          cp /data/demo-payload.txt /data/expected-payload.txt
          sha256sum /data/demo-payload.txt | awk '{print $1}' > /data/demo-payload.sha256
          sync
          cat /data/demo-payload.txt
          echo "sha256=$(cat /data/demo-payload.sha256)"
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: src
YAML
)
      reader_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: reader
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: reader
      image: busybox:1.36
      env:
        - name: EXPECTED_MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          ls -la /data
          cat > /tmp/expected-payload.txt <<PAYLOAD
          Hammerspace CSI snapshot restore demo
          marker=${EXPECTED_MARKER}
          file=demo-payload.txt
          validation=full-content-and-sha256
          PAYLOAD
          sed -i 's/^          //' /tmp/expected-payload.txt
          cmp -s /tmp/expected-payload.txt /data/demo-payload.txt
          expected_sha="$(sha256sum /tmp/expected-payload.txt | awk '{print $1}')"
          actual_sha="$(sha256sum /data/demo-payload.txt | awk '{print $1}')"
          saved_sha="$(cat /data/demo-payload.sha256)"
          echo "expected_sha=${expected_sha}"
          echo "actual_sha=${actual_sha}"
          echo "saved_sha=${saved_sha}"
          test "${actual_sha}" = "${expected_sha}"
          test "${saved_sha}" = "${expected_sha}"
          grep -qx "marker=${EXPECTED_MARKER}" /data/demo-payload.txt
          cat /data/demo-payload.txt
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: restore
YAML
)
      ;;
    nfs-share)
      storage_class_comment="NFS share-backed snapshot restore demo volume"
      storage_class_parameters=$(cat <<EOF
  fsType: "nfs"
  objectives: "${objectives}"
  exportOptions: "${export_options}"
  deleteDelay: "0"
  volumeNameFormat: "csi-nfs-share-%s"
  additionalMetadataTags: "storageClassName=${scenario_storage_class},fsType=nfs"
  comment: "NFS share-backed snapshot restore demo volume"
EOF
)
      access_modes="ReadWriteMany"
      marker_prefix="nfs-share"
      scenario_label="nfs-share"
      scenario_description="NFS share-backed"
      writer_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: writer
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: writer
      image: busybox:1.36
      env:
        - name: MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          echo "${MARKER}" > /data/marker.txt
          sync
          cat /data/marker.txt
          sleep 600
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: src
YAML
)
      reader_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: reader
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: reader
      image: busybox:1.36
      env:
        - name: EXPECTED_MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          ls -la /data
          actual="$(cat /data/marker.txt)"
          echo "$actual"
          test "$actual" = "${EXPECTED_MARKER}"
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: restore
YAML
)
      writer_wait_command="kubectl -n \"\${ns}\" wait pod/writer --for=condition=Ready --timeout=300s
kubectl -n \"\${ns}\" logs pod/writer --tail=20"
      writer_after_snapshot_command="kubectl -n \"\${ns}\" delete pod writer --ignore-not-found --wait=true --timeout=120s || true"
      ;;
    nfs-mount-backing)
      storage_class_comment="NFS mountBackingShareName snapshot restore demo volume"
      storage_class_parameters=$(cat <<EOF
  fsType: "nfs"
  mountBackingShareName: ${backing_share}
  objectives: "${objectives}"
  exportOptions: "${export_options}"
  deleteDelay: "0"
  volumeNameFormat: "csi-nfs-dir-%s"
  additionalMetadataTags: "storageClassName=${scenario_storage_class},fsType=nfs"
  comment: "NFS mountBackingShareName snapshot restore demo volume"
EOF
)
      access_modes="ReadWriteMany"
      marker_prefix="nfs-mount-backing"
      scenario_label="nfs-mount-backing"
      scenario_description="NFS mountBackingShareName"
      writer_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: writer
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: writer
      image: busybox:1.36
      env:
        - name: MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          echo "${MARKER}" > /data/marker.txt
          sync
          cat /data/marker.txt
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: src
YAML
)
      reader_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: reader
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: reader
      image: busybox:1.36
      env:
        - name: EXPECTED_MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          ls -la /data
          actual="$(cat /data/marker.txt)"
          echo "$actual"
          test "$actual" = "${EXPECTED_MARKER}"
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: restore
YAML
)
      ;;
    block)
      storage_class_comment="Raw block snapshot restore demo volume"
      storage_class_parameters=$(cat <<EOF
  blockBackingShareName: ${block_backing_share}
  objectives: "${objectives}"
  deleteDelay: "0"
  volumeNameFormat: "csi-block-%s"
  additionalMetadataTags: "storageClassName=${scenario_storage_class},fsType=block"
  comment: "Raw block snapshot restore demo volume"
EOF
)
      access_modes="ReadWriteOnce"
      volume_mode_block="  volumeMode: Block"
      marker_prefix="block"
      scenario_label="block"
      scenario_description="raw block"
      writer_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: writer
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: writer
      image: busybox:1.36
      env:
        - name: MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          printf "%s" "${MARKER}" > /tmp/marker.txt
          dd if=/tmp/marker.txt of=/dev/hsblock bs=512 count=1 conv=notrunc
          sync
          dd if=/dev/hsblock bs=512 count=1 2>/dev/null | tr -d '\000'
      volumeDevices:
        - name: data
          devicePath: /dev/hsblock
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: src
YAML
)
      reader_pod_manifest=$(cat <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: reader
  namespace: __NS__
spec:
  restartPolicy: Never
  containers:
    - name: reader
      image: busybox:1.36
      env:
        - name: EXPECTED_MARKER
          value: "__MARKER__"
      command:
        - sh
        - -c
        - |
          set -eu
          actual="$(dd if=/dev/hsblock bs=512 count=1 2>/dev/null | tr -d '\000')"
          echo "$actual"
          test "$actual" = "${EXPECTED_MARKER}"
      volumeDevices:
        - name: data
          devicePath: /dev/hsblock
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: restore
YAML
)
      ;;
    *)
      fail "unsupported scenario: ${scenario_name}"
      ;;
  esac

  local job_script
  job_script=$(cat <<SCRIPT
set -eu

ns="\$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)"
marker="${marker_prefix}-restore-ok-\$(date -u +%Y-%m-%dT%H%M%SZ)"

wait_for_pod_succeeded() {
  pod_name="\$1"
  timeout_seconds="\${2:-300}"
  end_time=\$((\$(date +%s) + timeout_seconds))
  while [ "\$(date +%s)" -lt "\${end_time}" ]; do
    phase="\$(kubectl -n "\${ns}" get pod "\${pod_name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "\${phase}" in
      Succeeded)
        return 0
        ;;
      Failed)
        echo "Pod \${pod_name} failed"
        kubectl -n "\${ns}" logs "pod/\${pod_name}" --all-containers=true || true
        kubectl -n "\${ns}" describe "pod/\${pod_name}" || true
        return 1
        ;;
    esac
    sleep 2
  done
  echo "Timed out waiting for pod \${pod_name} to succeed"
  kubectl -n "\${ns}" get pod "\${pod_name}" -o wide || true
  kubectl -n "\${ns}" logs "pod/\${pod_name}" --all-containers=true || true
  return 1
}

echo "Cleaning up any previous test resources in namespace \${ns}"
kubectl -n "\${ns}" delete pod reader writer --ignore-not-found --wait=true --timeout=120s || true
kubectl -n "\${ns}" delete pvc restore --ignore-not-found --wait=true --timeout=120s || true
kubectl -n "\${ns}" delete volumesnapshot src-snap --ignore-not-found --wait=true --timeout=120s || true
kubectl -n "\${ns}" delete pvc src --ignore-not-found --wait=true --timeout=120s || true

echo "Creating source ${scenario_description} PVC"
cat <<EOF | kubectl create -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: src
  namespace: \${ns}
spec:
  accessModes:
    - ${access_modes}
${volume_mode_block}
  storageClassName: ${scenario_storage_class}
  resources:
    requests:
      storage: ${volume_size}
EOF

kubectl -n "\${ns}" wait pvc/src --for=jsonpath='{.status.phase}'=Bound --timeout=300s

echo "Writing marker to source PVC: \${marker}"
cat <<'EOF' | sed \
  -e "s|__NS__|\${ns}|g" \
  -e "s|__MARKER__|\${marker}|g" | kubectl create -f -
${writer_pod_manifest}
EOF

${writer_wait_command}

echo "Creating snapshot from source PVC"
cat <<EOF | kubectl create -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: src-snap
  namespace: \${ns}
spec:
  volumeSnapshotClassName: ${scenario_snapshot_class}
  source:
    persistentVolumeClaimName: src
EOF

kubectl -n "\${ns}" wait volumesnapshot/src-snap --for=jsonpath='{.status.readyToUse}'=true --timeout=300s
kubectl -n "\${ns}" get volumesnapshot src-snap -o wide
${writer_after_snapshot_command}

echo "Creating restore PVC from snapshot"
cat <<EOF | kubectl create -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restore
  namespace: \${ns}
spec:
  accessModes:
    - ${access_modes}
${volume_mode_block}
  storageClassName: ${scenario_storage_class}
  dataSource:
    name: src-snap
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  resources:
    requests:
      storage: ${volume_size}
EOF

kubectl -n "\${ns}" wait pvc/restore --for=jsonpath='{.status.phase}'=Bound --timeout=300s

echo "Reading restored PVC and validating marker"
cat <<'EOF' | sed \
  -e "s|__NS__|\${ns}|g" \
  -e "s|__MARKER__|\${marker}|g" | kubectl create -f -
${reader_pod_manifest}
EOF

wait_for_pod_succeeded reader 300
kubectl -n "\${ns}" logs pod/reader

echo "PASS: ${scenario_description} snapshot and restore preserved marker \${marker}"
SCRIPT
)

  local job_script_indented
  job_script_indented=$(printf '%s\n' "$job_script" | sed 's/^/              /')

  cat <<YAML
---
apiVersion: v1
kind: Namespace
metadata:
  name: ${scenario_namespace}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${scenario_storage_class}
provisioner: com.hammerspace.csi
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
mountOptions:
  - vers=4.2
  - hard
  - timeo=600
parameters:
${storage_class_parameters}
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: ${scenario_snapshot_class}
driver: com.hammerspace.csi
deletionPolicy: Delete
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${scenario_service_account}
  namespace: ${scenario_namespace}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${scenario_service_account}
  namespace: ${scenario_namespace}
rules:
  - apiGroups: [""]
    resources: ["persistentvolumeclaims", "pods"]
    verbs: ["create", "delete", "get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: ["volumesnapshots"]
    verbs: ["create", "delete", "get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${scenario_service_account}
  namespace: ${scenario_namespace}
subjects:
  - kind: ServiceAccount
    name: ${scenario_service_account}
    namespace: ${scenario_namespace}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${scenario_service_account}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: ${scenario_job}
  namespace: ${scenario_namespace}
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: ${scenario_service_account}
      restartPolicy: Never
      containers:
        - name: e2e
          image: ${kubectl_image}
          imagePullPolicy: IfNotPresent
          command:
            - /bin/sh
            - -c
            - |
${job_script_indented}
YAML
}

run_scenario_once() {
  local scenario_name="$1"
  set_defaults_for_scenario "$scenario_name"
  render_manifest "$scenario_name" "$namespace" "$storage_class" "$snapshot_class" "$service_account" "$job"
}

if [ "$scenario" = "all" ]; then
  for case_name in file-backed nfs-share nfs-mount-backing block; do
    namespace=""
    storage_class=""
    snapshot_class=""
    service_account=""
    job=""
    set_defaults_for_scenario "$case_name"
    if [ "$render_only" = "true" ]; then
      render_manifest "$case_name" "$namespace" "$storage_class" "$snapshot_class" "$service_account" "$job"
      continue
    fi
    say "Preparing ${case_name} scenario"
    if [ "$preflight_only" = "true" ]; then
      continue
    fi
    if [ "$cleanup_first" = "true" ]; then
      say "Cleaning up previous ${case_name} demo Job"
      run_kubectl -n "$namespace" delete job "$job" --ignore-not-found --wait=true --timeout=120s || true
    fi
    say "Applying embedded ${case_name} snapshot/restore demo"
    render_manifest "$case_name" "$namespace" "$storage_class" "$snapshot_class" "$service_account" "$job" | run_kubectl apply -f -
    say "Waiting for demo Job to complete"
    if ! run_kubectl -n "$namespace" wait --for=condition=Complete "job/${job}" --timeout="${timeout}"; then
      warn "demo Job did not complete successfully for ${case_name}; collecting current state"
      run_kubectl -n "$namespace" get pvc,pod,volumesnapshot -o wide || true
      run_kubectl -n "$namespace" describe "job/${job}" || true
      run_kubectl -n "$namespace" logs "job/${job}" --all-containers=true || true
      fail "${case_name} snapshot/restore demo failed"
    fi
    say "Demo logs"
    run_kubectl -n "$namespace" logs "job/${job}" --all-containers=true || true
    say "Demo evidence"
    run_kubectl -n "$namespace" get pvc src restore -o wide || true
    run_kubectl -n "$namespace" get volumesnapshot src-snap -o wide || true
  done
  if [ "$render_only" = "true" ]; then
    exit 0
  fi
  if [ "$preflight_only" = "true" ]; then
    say "Preflight complete"
    echo "Environment is ready for all snapshot/restore demos."
  else
    say "Result"
    echo "PASS: all CSI snapshot/restore demos completed successfully."
  fi
  exit 0
fi

set_defaults_for_scenario "$scenario"

if [ "${render_only}" = "true" ]; then
  render_manifest "$scenario" "$namespace" "$storage_class" "$snapshot_class" "$service_account" "$job"
  exit 0
fi

say "Demo target"
echo "scenario:      ${scenario}"
echo "kubectl:       ${kubectl_bin}"
echo "context:       $(run_kubectl config current-context 2>/dev/null || echo unknown)"
echo "namespace:     ${namespace}"
echo "storage class: ${storage_class}"
echo "snapshot class:${snapshot_class}"
echo "job:           ${job}"
echo "kubectl image: ${kubectl_image}"
echo "backing share: ${backing_share}"
echo "objectives:    ${objectives}"
echo "fs type:       ${fs_type}"
echo "volume size:   ${volume_size}"

say "Preflight: Kubernetes API"
run_kubectl version --short 2>/dev/null || run_kubectl version
if ! run_kubectl get namespace "${namespace}" >/dev/null 2>&1; then
  run_kubectl auth can-i create namespaces >/dev/null || \
    fail "current user cannot create namespace ${namespace}"
fi
run_kubectl auth can-i create storageclasses.storage.k8s.io >/dev/null || \
  fail "current user cannot create StorageClasses"
run_kubectl auth can-i create volumesnapshotclasses.snapshot.storage.k8s.io >/dev/null || \
  fail "current user cannot create VolumeSnapshotClasses"
run_kubectl auth can-i create persistentvolumeclaims -n "${namespace}" >/dev/null || \
  fail "current user cannot create PVCs in namespace ${namespace}"
run_kubectl auth can-i create volumesnapshots.snapshot.storage.k8s.io -n "${namespace}" >/dev/null || \
  fail "current user cannot create VolumeSnapshots in namespace ${namespace}"

say "Preflight: Hammerspace CSI driver"
run_kubectl get csidriver com.hammerspace.csi >/dev/null || \
  fail "CSIDriver com.hammerspace.csi is not registered"
run_kubectl -n kube-system rollout status statefulset/csi-provisioner --timeout=120s
run_kubectl -n kube-system rollout status daemonset/csi-node --timeout=120s

say "Preflight: snapshot API"
for crd in \
  volumesnapshotclasses.snapshot.storage.k8s.io \
  volumesnapshotcontents.snapshot.storage.k8s.io \
  volumesnapshots.snapshot.storage.k8s.io
do
  run_kubectl get crd "${crd}" >/dev/null || fail "missing snapshot CRD: ${crd}"
done

if ! run_kubectl api-resources --api-group=snapshot.storage.k8s.io | grep -q '^volumesnapshots'; then
  fail "snapshot.storage.k8s.io API resources are not discoverable"
fi

if ! run_kubectl get pods -A -o name | grep -E 'snapshot-controller|csi-snapshotter' >/dev/null; then
  warn "could not find a pod name containing snapshot-controller or csi-snapshotter"
  warn "continuing because some environments use different pod names"
fi

if [ "${preflight_only}" = "true" ]; then
  say "Preflight complete"
  echo "Environment is ready for the ${scenario} snapshot/restore demo."
  exit 0
fi

if [ "${cleanup_first}" = "true" ]; then
  say "Cleaning up previous demo Job"
  run_kubectl -n "${namespace}" delete job "${job}" --ignore-not-found --wait=true --timeout=120s || true
fi

say "Applying embedded ${scenario} snapshot/restore demo"
render_manifest "$scenario" "$namespace" "$storage_class" "$snapshot_class" "$service_account" "$job" | run_kubectl apply -f -

say "Waiting for demo Job to complete"
if ! run_kubectl -n "${namespace}" wait --for=condition=Complete "job/${job}" --timeout="${timeout}"; then
  warn "demo Job did not complete successfully; collecting current state"
  run_kubectl -n "${namespace}" get pvc,pod,volumesnapshot -o wide || true
  run_kubectl -n "${namespace}" describe "job/${job}" || true
  run_kubectl -n "${namespace}" logs "job/${job}" --all-containers=true || true
  fail "${scenario} snapshot/restore demo failed"
fi

say "Demo logs"
run_kubectl -n "${namespace}" logs "job/${job}" --all-containers=true

say "Demo evidence"
run_kubectl -n "${namespace}" get pvc src restore -o wide
run_kubectl -n "${namespace}" get volumesnapshot src-snap -o wide

say "Result"
echo "PASS: ${scenario} CSI snapshot/restore demo completed successfully."
