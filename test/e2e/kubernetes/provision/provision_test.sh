#!/usr/bin/env bash
set -euo pipefail

kubectl_bin="${KUBECTL:-kubectl}"
scenario="${SCENARIO:-all}"
kubectl_image="${KUBECTL_IMAGE:-busybox:1.36}"
backing_share="${BACKING_SHARE:-k8s-file-storage}"
nfs_backing_share="${NFS_BACKING_SHARE:-k8s-nfs-storage}"
block_backing_share="${BLOCK_BACKING_SHARE:-k8s-block-storage}"
objectives="${OBJECTIVES:-keep-online}"
export_options="${EXPORT_OPTIONS:-*,RW,false}"
fs_type="${FS_TYPE:-ext4}"
volume_size="${VOLUME_SIZE:-1Gi}"
timeout="${TIMEOUT:-5m}"
preflight_only="false"
cleanup_first="true"
render_only="false"

usage() {
  cat <<EOF
Usage: $0 [options]

Hammerspace CSI volume provisioning and pod attachment test for four scenarios:
  - file-backed
  - nfs
  - nfs-mount-backing
  - block
  - all

Each scenario creates a Namespace, StorageClass, PVC, and Pod. The Pod writes
and reads a marker through the provisioned volume, then exits successfully.

Options:
  --kubectl CMD              kubectl command to use. Example: "minikube kubectl --"
  --scenario NAME            Scenario to run: file-backed, nfs, nfs-mount-backing, block, or all. Default: ${scenario}
  --kubectl-image IMAGE      Image used by the check pod. Default: ${kubectl_image}
  --backing-share NAME       mountBackingShareName for file-backed volumes. Default: ${backing_share}
  --nfs-backing-share NAME   mountBackingShareName for nfs-mount-backing volumes. Default: ${nfs_backing_share}
  --block-backing-share NAME blockBackingShareName for block volumes. Default: ${block_backing_share}
  --objectives VALUE         Hammerspace objectives. Default: ${objectives}
  --export-options VALUE     exportOptions for NFS volumes. Default: ${export_options}
  --fs-type TYPE             Filesystem type for file-backed volumes. Default: ${fs_type}
  --volume-size SIZE         PVC size. Default: ${volume_size}
  --timeout DURATION         Wait timeout. Default: ${timeout}
  --preflight-only           Check the environment but do not create test resources.
  --render-only              Print the generated manifest and exit.
  --no-cleanup-first         Do not delete previous scenario Pod/PVC before applying.
  -h, --help                 Show this help.

Examples:
  $0 --kubectl "kubectl" --scenario all
  $0 --kubectl "minikube kubectl --" --scenario block --block-backing-share k8s-block-storage
  $0 --kubectl "minikube kubectl --" --scenario nfs-mount-backing --nfs-backing-share k8s-nfs-storage
  $0 --scenario file-backed --backing-share k8s-file-storage --objectives keep-online
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
  exit 1
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
    --nfs-backing-share)
      [ "$#" -ge 2 ] || fail "--nfs-backing-share requires a name"
      nfs_backing_share="$2"
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

scenario_values() {
  local scenario_name="$1"

  case "$scenario_name" in
    file-backed)
      namespace="hs-file-provision-e2e"
      storage_class="hs-file-provision-e2e"
      pod="file-provision-check"
      pvc="data"
      marker="file-provision-ok"
      access_mode="ReadWriteOnce"
      volume_mode=""
      pod_volume=""
      storage_parameters=$(cat <<EOF
  fsType: "${fs_type}"
  mountBackingShareName: ${backing_share}
  objectives: "${objectives}"
  deleteDelay: "0"
  volumeNameFormat: "csi-file-provision-%s"
  additionalMetadataTags: "storageClassName=hs-file-provision-e2e,fsType=${fs_type}"
  comment: "File-backed volume provisioning E2E volume"
EOF
)
      mount_options=$(cat <<'EOF'
mountOptions:
  - vers=4.2
  - hard
  - timeo=600
EOF
)
      ;;
    nfs)
      namespace="hs-nfs-provision-e2e"
      storage_class="hs-nfs-provision-e2e"
      pod="nfs-provision-check"
      pvc="data"
      marker="nfs-provision-ok"
      access_mode="ReadWriteMany"
      volume_mode=""
      pod_volume=""
      storage_parameters=$(cat <<EOF
  fsType: "nfs"
  objectives: "${objectives}"
  exportOptions: "${export_options}"
  deleteDelay: "0"
  volumeNameFormat: "csi-nfs-provision-%s"
  additionalMetadataTags: "storageClassName=hs-nfs-provision-e2e,fsType=nfs"
  comment: "NFS volume provisioning E2E volume"
EOF
)
      mount_options=$(cat <<'EOF'
mountOptions:
  - vers=4.2
  - hard
  - timeo=600
EOF
)
      ;;
    nfs-mount-backing)
      namespace="hs-nfs-mount-backing-provision-e2e"
      storage_class="hs-nfs-mount-backing-provision-e2e"
      pod="nfs-mount-backing-provision-check"
      pvc="data"
      marker="nfs-mount-backing-provision-ok"
      access_mode="ReadWriteMany"
      volume_mode=""
      pod_volume=""
      storage_parameters=$(cat <<EOF
  fsType: "nfs"
  mountBackingShareName: ${nfs_backing_share}
  objectives: "${objectives}"
  exportOptions: "${export_options}"
  deleteDelay: "0"
  volumeNameFormat: "csi-nfs-dir-provision-%s"
  additionalMetadataTags: "storageClassName=hs-nfs-mount-backing-provision-e2e,fsType=nfs"
  comment: "NFS mountBackingShareName volume provisioning E2E volume"
EOF
)
      mount_options=$(cat <<'EOF'
mountOptions:
  - vers=4.2
  - hard
  - timeo=600
EOF
)
      ;;
    block)
      namespace="hs-block-provision-e2e"
      storage_class="hs-block-provision-e2e"
      pod="block-provision-check"
      pvc="data"
      marker="block-provision-ok"
      access_mode="ReadWriteOnce"
      volume_mode="  volumeMode: Block"
      pod_volume="block"
      storage_parameters=$(cat <<EOF
  blockBackingShareName: ${block_backing_share}
  objectives: "${objectives}"
  deleteDelay: "0"
  volumeNameFormat: "csi-block-provision-%s"
  additionalMetadataTags: "storageClassName=hs-block-provision-e2e,fsType=block"
  comment: "Raw block volume provisioning E2E volume"
EOF
)
      mount_options=""
      ;;
    *)
      fail "unsupported scenario: ${scenario_name}"
      ;;
  esac
}

render_pod_container() {
  local scenario_name="$1"

  if [ "$scenario_name" = "block" ]; then
    cat <<EOF
    - name: check
      image: ${kubectl_image}
      command:
        - sh
        - -c
        - |
          set -eu
          marker="${marker}"
          printf "%s" "\${marker}" > /tmp/marker.txt
          dd if=/tmp/marker.txt of=/dev/hsblock bs=512 count=1 conv=notrunc
          sync
          actual="\$(dd if=/dev/hsblock bs=512 count=1 2>/dev/null | tr -d '\000')"
          echo "\${actual}"
          test "\${actual}" = "\${marker}"
          echo "PASS: raw block PVC provisioned, attached, and persisted marker"
      volumeDevices:
        - name: data
          devicePath: /dev/hsblock
EOF
    return
  fi

  cat <<EOF
    - name: check
      image: ${kubectl_image}
      command:
        - sh
        - -c
        - |
          set -eu
          marker="${marker}"
          echo "\${marker}" > /data/marker.txt
          sync
          actual="\$(cat /data/marker.txt)"
          echo "\${actual}"
          test "\${actual}" = "\${marker}"
          echo "PASS: ${scenario_name} PVC provisioned, attached, and persisted marker"
      volumeMounts:
        - name: data
          mountPath: /data
EOF
}

render_manifest() {
  local scenario_name="$1"
  scenario_values "$scenario_name"

  cat <<EOF
---
apiVersion: v1
kind: Namespace
metadata:
  name: ${namespace}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${storage_class}
provisioner: com.hammerspace.csi
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
${mount_options}
parameters:
${storage_parameters}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc}
  namespace: ${namespace}
spec:
  accessModes:
    - ${access_mode}
${volume_mode}
  storageClassName: ${storage_class}
  resources:
    requests:
      storage: ${volume_size}
---
apiVersion: v1
kind: Pod
metadata:
  name: ${pod}
  namespace: ${namespace}
spec:
  restartPolicy: Never
  containers:
$(render_pod_container "$scenario_name")
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${pvc}
EOF
}

preflight() {
  say "Preflight: Kubernetes API"
  run_kubectl version --short 2>/dev/null || run_kubectl version
  run_kubectl auth can-i create storageclasses.storage.k8s.io >/dev/null || \
    fail "current user cannot create StorageClasses"

  say "Preflight: Hammerspace CSI driver"
  run_kubectl get csidriver com.hammerspace.csi >/dev/null || \
    fail "CSIDriver com.hammerspace.csi is not registered"
  run_kubectl -n kube-system rollout status statefulset/csi-provisioner --timeout=120s
  run_kubectl -n kube-system rollout status daemonset/csi-node --timeout=120s
}

run_scenario() {
  local scenario_name="$1"
  scenario_values "$scenario_name"

  say "Scenario: ${scenario_name}"
  echo "namespace:      ${namespace}"
  echo "storage class:  ${storage_class}"
  echo "pvc:            ${pvc}"
  echo "pod:            ${pod}"
  echo "volume size:    ${volume_size}"

  if [ "${cleanup_first}" = "true" ]; then
    say "Cleaning up previous ${scenario_name} resources"
    run_kubectl -n "${namespace}" delete pod "${pod}" --ignore-not-found --wait=true --timeout=120s || true
    run_kubectl -n "${namespace}" delete pvc "${pvc}" --ignore-not-found --wait=true --timeout=120s || true
  fi

  say "Applying ${scenario_name} volume provisioning manifest"
  render_manifest "$scenario_name" | run_kubectl apply -f -

  say "Waiting for PVC to bind"
  run_kubectl -n "${namespace}" wait "pvc/${pvc}" --for=jsonpath='{.status.phase}'=Bound --timeout="${timeout}"

  say "Waiting for check pod to complete"
  if ! run_kubectl -n "${namespace}" wait "pod/${pod}" --for=jsonpath='{.status.phase}'=Succeeded --timeout="${timeout}"; then
    warn "${scenario_name} check pod did not complete; collecting state"
    run_kubectl -n "${namespace}" get pvc,pod -o wide || true
    run_kubectl -n "${namespace}" describe "pod/${pod}" || true
    run_kubectl -n "${namespace}" logs "pod/${pod}" --all-containers=true || true
    fail "${scenario_name} volume provisioning test failed"
  fi

  say "Check pod logs"
  run_kubectl -n "${namespace}" logs "pod/${pod}" --all-containers=true
}

case "${scenario}" in
  file-backed|nfs|nfs-mount-backing|block|all)
    ;;
  *)
    fail "unsupported scenario: ${scenario}"
    ;;
esac

if [ "${render_only}" = "true" ]; then
  if [ "${scenario}" = "all" ]; then
    for case_name in file-backed nfs nfs-mount-backing block; do
      render_manifest "${case_name}"
    done
  else
    render_manifest "${scenario}"
  fi
  exit 0
fi

say "Volume provisioning test target"
echo "scenario:       ${scenario}"
echo "kubectl:        ${kubectl_bin}"
echo "context:        $(run_kubectl config current-context 2>/dev/null || echo unknown)"
echo "pod image:      ${kubectl_image}"
echo "backing share:  ${backing_share}"
echo "nfs backing:    ${nfs_backing_share}"
echo "block backing:  ${block_backing_share}"
echo "objectives:     ${objectives}"
echo "fs type:        ${fs_type}"

preflight

if [ "${preflight_only}" = "true" ]; then
  say "Preflight complete"
  exit 0
fi

if [ "${scenario}" = "all" ]; then
  for case_name in file-backed nfs nfs-mount-backing block; do
    run_scenario "${case_name}"
  done
else
  run_scenario "${scenario}"
fi

say "Result"
echo "PASS: ${scenario} CSI volume provisioning test completed successfully."
