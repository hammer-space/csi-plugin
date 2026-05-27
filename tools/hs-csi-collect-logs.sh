#!/usr/bin/env bash
#
# hs-csi-collect-logs.sh
#
# Collects logs and state for troubleshooting the Hammerspace CSI driver in a
# Kubernetes cluster. Produces a single tarball that can be attached to a
# support case.
#
# Two collection scopes:
#   * Cluster scope (default): runs `kubectl` against the cluster to gather CSI
#     objects, driver pod logs (controller + node), events, and descriptions.
#     Run from any machine with kubectl access to the cluster.
#   * Host scope (--host):      gathers node-local state (mounts, NFS, loop
#     devices, dmesg, Hammerspace mount dirs). Run *on a worker node*, ideally
#     with sudo, when a specific node is misbehaving.
#
# Secrets are never collected: the script does not dump Kubernetes Secrets and
# masks any HS_PASSWORD value it encounters.
#
# Usage:
#   ./hs-csi-collect-logs.sh [options]
#
# Options:
#   -n, --namespace NS      Namespace the driver is deployed in (default: kube-system)
#   -d, --driver NAME       CSIDriver name (default: com.hammerspace.csi)
#   -o, --output DIR        Output directory (default: ./hs-csi-logs-<timestamp>)
#       --since DUR         How far back to pull container logs (default: 24h)
#       --controller-label  Label selector for the controller pods (default: app=csi-provisioner)
#       --node-label        Label selector for the node pods (default: app=csi-node)
#       --kubectl CMD       kubectl-compatible command (default: kubectl, or KUBECTL env var)
#       --host              Also collect node-local (host) state from THIS machine
#       --no-tar            Leave the output directory in place; do not create a tarball
#   -h, --help              Show this help
#
# Examples:
#   KUBECTL=k8 ./hs-csi-collect-logs.sh
#   ./hs-csi-collect-logs.sh --kubectl k8
#
set -u

# ---- defaults ---------------------------------------------------------------
NAMESPACE="kube-system"
DRIVER_NAME="com.hammerspace.csi"
CONTROLLER_LABEL="app=csi-provisioner"
NODE_LABEL="app=csi-node"
SINCE="24h"
OUTDIR=""
DO_HOST=0
DO_TAR=1
KUBECTL="${KUBECTL:-kubectl}"

# On-host paths used by the driver (see pkg/common/config.go)
HS_ROOT_MOUNT="/var/lib/hammerspace/rootmount"
HS_VOLUME_MARKERS="/var/lib/hammerspace/volumes"
HS_STAGING_DIR="/tmp"

usage() { sed -n '2,/^set -u/{/^set -u/d;s/^# \{0,1\}//;p}' "$0"; }

# ---- arg parsing ------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--namespace)        NAMESPACE="$2"; shift 2;;
    -d|--driver)           DRIVER_NAME="$2"; shift 2;;
    -o|--output)           OUTDIR="$2"; shift 2;;
    --since)               SINCE="$2"; shift 2;;
    --controller-label)    CONTROLLER_LABEL="$2"; shift 2;;
    --node-label)          NODE_LABEL="$2"; shift 2;;
    --kubectl)             KUBECTL="$2"; shift 2;;
    --host)                DO_HOST=1; shift;;
    --no-tar)              DO_TAR=0; shift;;
    -h|--help)             usage; exit 0;;
    *) echo "Unknown option: $1" >&2; usage; exit 2;;
  esac
done

TS="$(date +%Y%m%d-%H%M%S)"
[ -n "$OUTDIR" ] || OUTDIR="./hs-csi-logs-${TS}"
mkdir -p "$OUTDIR" || { echo "Cannot create output dir $OUTDIR" >&2; exit 1; }

# ---- helpers ----------------------------------------------------------------
# Mask the literal secret value if it ever shows up in collected text.
redact() { sed -E 's/(HS_PASSWORD[^A-Za-z0-9]+)[^[:space:]"]+/\1***REDACTED***/g'; }

log()  { echo "[$(date +%H:%M:%S)] $*"; }
note() { echo "  - $*" >> "$OUTDIR/_collection.log"; }

# run a command, capture stdout+stderr into a file, never abort the script
run() {
  # run <relative/output/path> <cmd> [args...]
  local out="$OUTDIR/$1"; shift
  mkdir -p "$(dirname "$out")"
  {
    echo "### \$ $*"
    echo "### $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "---"
  } > "$out"
  "$@" 2>&1 | redact >> "$out"
  note "$1"
}

have() { command -v "$1" >/dev/null 2>&1; }

load_shell_commands() {
  shopt -s expand_aliases

  set +u
  for rc_file in "$HOME/.bashrc" "$HOME/.bash_aliases" "$HOME/.profile"; do
    if [ -r "$rc_file" ]; then
      # Aliases such as k8 are normally unavailable to non-interactive scripts.
      # Source common shell config files quietly so they can be resolved here.
      # shellcheck source=/dev/null
      . "$rc_file" >/dev/null 2>&1 || true
    fi
  done
  set -u
}

get_alias_command() {
  local alias_line

  alias_line="$(alias "$1" 2>/dev/null || true)"
  [ -n "$alias_line" ] || return 1

  alias_line="${alias_line#*=}"
  alias_line="${alias_line#\'}"
  alias_line="${alias_line%\'}"

  printf '%s' "$alias_line"
}

resolve_kubectl_command() {
  local alias_command

  load_shell_commands

  if [ "$KUBECTL" != "${KUBECTL%[[:space:]]*}" ]; then
    KUBECTL_SHELL_COMMAND="$KUBECTL"
    return 0
  fi

  if alias_command="$(get_alias_command "$KUBECTL")"; then
    KUBECTL_SHELL_COMMAND="$alias_command"
    return 0
  fi

  if command -v "$KUBECTL" >/dev/null 2>&1; then
    KUBECTL_EXECUTABLE="$KUBECTL"
    return 0
  fi

  if bash -ic "type -t $(printf '%q' "$KUBECTL") >/dev/null" >/dev/null 2>&1; then
    KUBECTL_INTERACTIVE_COMMAND="$KUBECTL"
    return 0
  fi

  return 1
}

# kubectl wrapper that supports kubectl-compatible binaries, aliases, functions,
# and command strings such as "minikube kubectl --".
k() {
  local quoted_args

  if [ -n "${KUBECTL_SHELL_COMMAND:-}" ]; then
    printf -v quoted_args ' %q' "$@"
    eval "$KUBECTL_SHELL_COMMAND$quoted_args"
    return
  fi

  if [ -n "${KUBECTL_INTERACTIVE_COMMAND:-}" ]; then
    printf -v quoted_args ' %q' "$@"
    bash -ic "$KUBECTL_INTERACTIVE_COMMAND$quoted_args"
    return
  fi

  "$KUBECTL_EXECUTABLE" "$@"
}

# =============================================================================
# CLUSTER SCOPE
# =============================================================================
collect_cluster() {
  if ! resolve_kubectl_command; then
    log "$KUBECTL not found; skipping cluster-scope collection."
    echo "$KUBECTL not found" > "$OUTDIR/CLUSTER-SCOPE-SKIPPED.txt"
    echo "Pass a kubectl-compatible command with --kubectl, for example: --kubectl k8" \
      >> "$OUTDIR/CLUSTER-SCOPE-SKIPPED.txt"
    return
  fi
  if ! k version --request-timeout=10s >/dev/null 2>&1; then
    log "WARNING: cannot reach the cluster API; cluster data may be incomplete."
  fi

  log "Collecting cluster & version info..."
  run cluster/kubectl-version.txt           k version -o yaml
  run cluster/nodes-wide.txt                k get nodes -o wide
  run cluster/nodes-describe.txt            k describe nodes
  run cluster/csidriver.txt                 k get csidriver "$DRIVER_NAME" -o yaml
  run cluster/csidrivers-all.txt            k get csidrivers -o wide
  run cluster/csinodes.txt                  k get csinodes -o yaml

  log "Collecting CSI / storage objects (cluster-wide)..."
  run storage/storageclasses.txt           k get storageclass -o yaml
  run storage/pv.txt                        k get pv -o yaml
  run storage/pv-wide.txt                   k get pv -o wide
  run storage/pvc-all-namespaces.txt        k get pvc -A -o wide
  run storage/volumeattachments.txt         k get volumeattachment -o yaml
  run storage/volumeattachments-wide.txt    k get volumeattachment -o wide

  # Snapshot CRDs may not be installed; failures are captured per-file.
  run storage/volumesnapshotclasses.txt     k get volumesnapshotclass -o yaml
  run storage/volumesnapshots.txt           k get volumesnapshots -A -o yaml
  run storage/volumesnapshotcontents.txt    k get volumesnapshotcontents -o yaml

  log "Collecting driver workloads in namespace '$NAMESPACE'..."
  run driver/statefulset.txt                k -n "$NAMESPACE" get statefulset -o yaml
  run driver/daemonset.txt                  k -n "$NAMESPACE" get daemonset -o yaml
  run driver/pods-wide.txt                  k -n "$NAMESPACE" get pods -o wide
  run driver/events.txt                     k -n "$NAMESPACE" get events --sort-by=.lastTimestamp

  # Cluster-wide events filtered for volume/mount/attach trouble are very useful.
  run driver/events-all-ns.txt              k get events -A --sort-by=.lastTimestamp

  collect_pod_set "controller" "$CONTROLLER_LABEL"
  collect_pod_set "node"       "$NODE_LABEL"
}

# Describe each pod in a label set, and pull logs from every container
# (current + previous/crashed).
collect_pod_set() {
  local role="$1" selector="$2"
  log "Collecting $role pods (selector: $selector)..."
  local pods
  pods="$(k -n "$NAMESPACE" get pods -l "$selector" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)"
  if [ -z "$pods" ]; then
    echo "No pods found for selector '$selector' in namespace '$NAMESPACE'" \
      > "$OUTDIR/driver/$role/NO-PODS-FOUND.txt"
    note "driver/$role: no pods found"
    return
  fi
  for pod in $pods; do
    run "driver/$role/$pod/describe.txt" k -n "$NAMESPACE" describe pod "$pod"
    # iterate init + regular containers
    local containers
    containers="$(k -n "$NAMESPACE" get pod "$pod" \
      -o jsonpath='{range .spec.initContainers[*]}{.name}{"\n"}{end}{range .spec.containers[*]}{.name}{"\n"}{end}' 2>/dev/null)"
    for c in $containers; do
      [ -n "$c" ] || continue
      run "driver/$role/$pod/logs-$c.txt"       k -n "$NAMESPACE" logs "$pod" -c "$c" --since="$SINCE"
      # previous logs only exist if the container restarted; file will note the error otherwise
      run "driver/$role/$pod/logs-$c.prev.txt"  k -n "$NAMESPACE" logs "$pod" -c "$c" --previous --since="$SINCE"
    done
  done
}

# =============================================================================
# HOST SCOPE  (run on a worker node; sudo recommended)
# =============================================================================
collect_host() {
  log "Collecting host-local state on $(hostname)..."
  local h="host/$(hostname)"
  run "$h/uname.txt"            uname -a
  run "$h/os-release.txt"       cat /etc/os-release

  # Mounts — the heart of CSI troubleshooting
  run "$h/mount.txt"            mount
  run "$h/proc-mounts.txt"      cat /proc/mounts
  run "$h/nfs-mounts.txt"       sh -c "mount -t nfs,nfs4 2>/dev/null"
  run "$h/findmnt.txt"          sh -c "command -v findmnt >/dev/null && findmnt -A || echo 'findmnt not available'"
  run "$h/df.txt"               df -hT
  run "$h/nfsstat.txt"          sh -c "command -v nfsstat >/dev/null && nfsstat -m || echo 'nfsstat not available'"

  # Loop devices — file-backed volumes attach as /dev/loopN
  run "$h/losetup.txt"          sh -c "command -v losetup >/dev/null && losetup -a || echo 'losetup not available'"
  run "$h/lsblk.txt"            sh -c "command -v lsblk >/dev/null && lsblk -o NAME,MAJ:MIN,RM,SIZE,RO,TYPE,MOUNTPOINT || echo 'lsblk not available'"

  # Hammerspace driver state on the host
  run "$h/hs-rootmount-ls.txt"      sh -c "ls -la '$HS_ROOT_MOUNT' 2>&1"
  run "$h/hs-volume-markers-ls.txt" sh -c "ls -la '$HS_VOLUME_MARKERS' 2>&1"
  run "$h/hs-mounts-grep.txt"       sh -c "grep -E 'hammerspace|/tmp/' /proc/mounts 2>&1"
  run "$h/kubelet-csi-mounts.txt"   sh -c "grep -E 'kubernetes.io~csi|/var/lib/kubelet/pods' /proc/mounts 2>&1"

  # Kernel / NFS client messages
  run "$h/dmesg.txt"            sh -c "dmesg -T 2>/dev/null | tail -n 2000 || dmesg | tail -n 2000"
  run "$h/dmesg-nfs.txt"        sh -c "( dmesg -T 2>/dev/null || dmesg ) | grep -iE 'nfs|rpc|mount|loop' | tail -n 500"

  # kubelet logs (best-effort across common locations)
  run "$h/kubelet-journal.txt"  sh -c "command -v journalctl >/dev/null && journalctl -u kubelet --no-pager --since '24 hours ago' || echo 'journalctl/kubelet unit not available'"

  log "Host collection complete."
}

# =============================================================================
# MAIN
# =============================================================================
{
  echo "Hammerspace CSI log collection"
  echo "  date:          $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "  namespace:     $NAMESPACE"
  echo "  driver:        $DRIVER_NAME"
  echo "  controller:    $CONTROLLER_LABEL"
  echo "  node:          $NODE_LABEL"
  echo "  kubectl:       $KUBECTL"
  echo "  log since:     $SINCE"
  echo "  host scope:    $([ "$DO_HOST" -eq 1 ] && echo yes || echo no)"
  echo "  collected by:  $(whoami)@$(hostname)"
} | tee "$OUTDIR/_manifest.txt"
: > "$OUTDIR/_collection.log"

collect_cluster
[ "$DO_HOST" -eq 1 ] && collect_host

# ---- package ----------------------------------------------------------------
if [ "$DO_TAR" -eq 1 ]; then
  TARBALL="${OUTDIR%/}.tar.gz"
  if tar -czf "$TARBALL" -C "$(dirname "$OUTDIR")" "$(basename "$OUTDIR")" 2>/dev/null; then
    rm -rf "$OUTDIR"
    log "Done. Bundle: $TARBALL"
    echo
    echo "Attach this file to your support case:"
    echo "  $TARBALL"
  else
    log "tar failed; leaving raw directory: $OUTDIR"
  fi
else
  log "Done. Raw output left in: $OUTDIR"
fi
