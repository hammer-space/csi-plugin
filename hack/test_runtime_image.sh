#!/usr/bin/env bash
# Smoke-test the tools used by the CSI driver without a cluster or storage server.
# All filesystem images live inside an unprivileged, disposable container. Actual
# NFS mounts, loop devices, filesystem freezing, and online XFS growth need the
# privileged node environment and are not exercised here.
set -euo pipefail

if [[ $# -gt 1 ]]; then
  echo "Usage: $0 [IMAGE]" >&2
  exit 2
fi

IMAGE="${1:-hammerspaceinc/csi-plugin:latest}"
echo "Testing runtime tools in ${IMAGE}"

docker run --rm -i --network none --cap-drop ALL \
  --security-opt no-new-privileges --entrypoint sh "$IMAGE" -se <<'CONTAINER'
export LC_ALL=C
runtime_test_dir=$(mktemp -d /tmp/hs-csi-runtime-test.XXXXXX)
trap 'rm -rf "$runtime_test_dir"' EXIT

# command -v catches missing packages; invoking the commands also catches missing
# shared libraries and broken Python dependencies in the final runtime image.
for tool in mount.nfs qemu-img mkfs.ext4 mkfs.xfs resize2fs xfs_growfs \
  losetup blkid mount umount fsfreeze showmount rpcinfo hs e2fsck dumpe2fs; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Missing runtime tool: $tool" >&2
    exit 1
  fi
done

mount.nfs -V
qemu-img --version
mkfs.ext4 -V
mkfs.xfs -V
xfs_growfs -V
losetup --version
blkid --version
mount --version
umount --version
fsfreeze --version
showmount --version
hs --help >/dev/null

# rpcinfo has no version/help option that exits successfully. Its usage output
# confirms the executable loads without contacting an RPC server.
rpcinfo_status=0
rpcinfo -h >"$runtime_test_dir/rpcinfo-help" 2>&1 || rpcinfo_status=$?
if [ "$rpcinfo_status" -ne 1 ] || ! grep -qi 'usage.*rpcinfo' "$runtime_test_dir/rpcinfo-help"; then
  cat "$runtime_test_dir/rpcinfo-help" >&2
  echo "rpcinfo did not produce the expected usage output" >&2
  exit 1
fi

ext4_image="$runtime_test_dir/ext4.raw"
xfs_image="$runtime_test_dir/xfs.raw"

echo "Creating, formatting, and growing an ext4 volume"
qemu-img create -fraw "$ext4_image" 67108864
test "$(stat -c %s "$ext4_image")" -eq 67108864
mkfs.ext4 -E lazy_itable_init=1,lazy_journal_init=1 "$ext4_image"
test "$(blkid -p -s TYPE -o value "$ext4_image")" = ext4
original_blocks=$(dumpe2fs -h "$ext4_image" 2>/dev/null | sed -n 's/^Block count: *//p')

qemu-img resize -fraw "$ext4_image" 134217728
test "$(stat -c %s "$ext4_image")" -eq 134217728
resize2fs "$ext4_image"
grown_blocks=$(dumpe2fs -h "$ext4_image" 2>/dev/null | sed -n 's/^Block count: *//p')
test "$grown_blocks" -gt "$original_blocks"
e2fsck -f -n "$ext4_image"
test "$(blkid -p -s TYPE -o value "$ext4_image")" = ext4

echo "Creating and formatting an XFS volume"
qemu-img create -fraw "$xfs_image" 536870912
mkfs.xfs -m reflink=0 -K "$xfs_image"
test "$(blkid -p -s TYPE -o value "$xfs_image")" = xfs

echo "Runtime smoke test passed (mount/loop/freeze/online-growth operations require cluster testing)."
CONTAINER
