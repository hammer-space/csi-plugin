# File-backed provisioning performance

This document records the performance work on the **file-backed** CreateVolume
path (an ext4/xfs filesystem inside a file on a shared backing share) — the
bottlenecks found, the fixes, how each was measured, and the deployment knobs
that matter. It is the design reference for the `perf/file-backed-parallel-create`
changes and a runbook for anyone scaling file-backed provisioning.

The headline: file-backed provisioning was ~2–3 volumes/s no matter the hardware.
Removing the bottlenecks one layer at a time took a single backing share from
~2.2/s to the point where the CSI driver and storage are no longer the limit.

## The bottleneck ladder

Throughput was gated by a *stack* of independent limits. Each had to be removed
to expose the next; none of them was the storage backend's raw capacity.

### 1. Driver: the backing-share lock serialized every file create

`ensureFileBackedVolumeExists` held the per-backing-share lock
(`acquireVolumeLock(backingShareName)`) across the **entire** create — both
`ensureBackingShareExists` (create-the-share-if-missing, which genuinely needs
serializing) **and** `ensureDeviceFileExists` (the per-file `qemu-img` + `mkfs`,
which does not). So every file on a backing share was created one-at-a-time.

Symptom: with 100 provisioner workers, `hs_csi_locks_held` pinned at ~100 (99
workers blocked on the one lock), `Controller/CreateVolume` p50 pegged at the 30s
lock-acquire timeout, a storm of `codes.Aborted` retries, and ~2.2/s throughput —
while the controller host was idle (load 0.5 on 32 vCPU) and the Anvil served
~13 of its ~5000 calls/s.

**Fix (`e4c2005`):** narrow the lock to only `ensureBackingShareExists`, release
it, then do the per-file work unlocked. Because the per-file mount/unmount is
shared state, a **mount refcount** (`acquireBackingMount`/`releaseBackingMount`,
`mountRefs` map + `mountRefsMu`) keeps the backing share mounted while any create
is in flight and unmounts only when the last finishes — so `mkfs` parallelizes
across the worker threads without the unmount being yanked out mid-format.
Same-volume races are still covered by the *unique per-volume* lock in
`CreateVolume`.

Result: `held` stopped pinning at 100 and drained to 0; the lock left the
critical path.

### 2. Provisioner: client-side Kubernetes API throttle

With the lock gone, throughput barely moved — the external-provisioner sidecar
was **client-side throttled** on its own API calls:
`Waited … due to client-side throttling` filled its log. Its default
`--kube-api-qps`/`--kube-api-burst` (≈5/10) capped how fast it could drive
CreateVolume.

**Fix (deployment, not code):** raise the sidecar args —
`--kube-api-qps=200 --kube-api-burst=400` (and `--worker-threads=100`, the
default). Now up to 100 creates dispatch concurrently.

> Note: raising `--worker-threads` alone does **not** help while a single backing
> share serializes (see §1) or while the data path is saturated (see §3) — it
> just deepens the queue and pushes lock waits past the 30s timeout. Raise it only
> together with §1 and enough backing-store headroom.

### 3. Storage data path: mkfs write volume saturated the DSX

At 100 concurrent creates the ceiling became `FormatDevice` — `mkfs.ext4` p95
ballooned to **9.6s** even though the controller host was idle. `mkfs.ext4`
without flags **eagerly zeroes the inode table and journal** — ~37 MB of writes
per 1 GB volume — and for file-backed those writes go over NFS to the backing
share. Independent DSX-side `iostat` confirmed the NVMe pinned at ~442 MB/s /
100% util. Spreading across 8 backing shares did **not** help: it is one DSX.

**Fix (`e9be029`):** `mkfs.ext4 -E lazy_itable_init=1,lazy_journal_init=1` for
ext4/ext3 — defer inode-table/journal zeroing to lazy kernel background init.
Create-time writes drop from ~37 MB to ~KB; `mkfs.ext4` p95 falls to **0.01s**
and per-volume physical footprint from ~64 MB to **~660 KB**. `qemu-img create -f
raw` was already sparse, so the file itself was never the problem — only the
`mkfs` zeroing was.

Verified at scale: thousands of volumes created with DSX `iostat` staying low.

### 4. Control plane: kube-controller-manager binding throttle

With the driver and storage fast, PVs were created faster than Kubernetes could
**bind** them: thousands of PVs created, PVCs stuck Pending. This is
kube-controller-manager's PV controller running at its default
`--kube-api-qps=20`.

**Fix (control plane, not code):** raise kcm `--kube-api-qps=100
--kube-api-burst=100` (static-pod manifest). Binding went from ~1–2/s to ~15/s
and kept pace with creation.

## Filesystem comparison (ext4 vs xfs vs btrfs)

Measured on the same driver, backing store, and 100-way concurrency:

| fs | mkfs flags | mkfs p95 | phys / 1 GB volume |
|---|---|---|---|
| **ext4 (lazy)** | `-E lazy_itable_init=1,lazy_journal_init=1` | **0.01s** | **~660 KB** |
| ext4 (eager, old) | none | 9.6s | ~64 MB |
| xfs | `-m reflink=0 -K` | slower | ~65 MB |
| btrfs | — | — | not supported here |

- **ext4 + lazy-init is the winner** for file-backed — fastest mkfs and thinnest.
- **xfs** is heavier: `mkfs.xfs` lays down allocation-group structures (~65 MB)
  even with `-K` (which skips the discard pass — commit `c206377`, the xfs analog
  of ext4's lazy behaviour). Kept as a correct, supported option, not the default.
- **btrfs** is unavailable on RHEL/CentOS-family nodes: the CentOS Stream 10
  kernel ships no `btrfs` module (`modprobe btrfs` → *module not found*), so a
  formatted volume could not be mounted. Supporting it (`mkfs.btrfs -K`) is a
  one-line driver change gated on a btrfs-capable node OS (Ubuntu / Amazon Linux /
  Fedora) or ELRepo `kmod-btrfs`.

## Deployment tuning summary

| Knob | Where | Default | Set to | Why |
|---|---|---|---|---|
| `--kube-api-qps` / `--burst` | `csi-provisioner` sidecar | ~5/10 | 200/400 | dispatch creates concurrently (§2) |
| `--worker-threads` | `csi-provisioner` sidecar | 100 | 100 | concurrency cap; only useful with §1+§3 |
| `--kube-api-qps` / `--burst` | kube-controller-manager | 20/30 | 100/100 | bind PVCs as fast as PVs are made (§4) |

## Results

Single backing share, file-backed 1 GB volumes:

- **mkfs.ext4:** 9.6s → **0.01s** (~1000×)
- **space/volume:** ~64 MB → **~660 KB** (~100×)
- **DSX write load:** ~442 MB/s @ 100% util → **low/idle**
- **lock behaviour:** held pinned-at-100-blocked → parallel, drains to 0
- CreateVolume throughput improved and, critically, the ceiling moved **off** the
  CSI driver and storage entirely (onto Kubernetes control-plane rates, §2/§4).

## Open item

With mkfs at 0.01s, the DSX idle, and binding unthrottled, the driver's
CreateVolume rate still peaks ~15/s and degrades over a long run. That remaining
ceiling has not been root-caused; likely candidates are the mount-refcount churn
(§1) or `GetFile` slowing as a backing-share directory grows to thousands of
files. Profiling per-op latency vs. directory size is the next step.
