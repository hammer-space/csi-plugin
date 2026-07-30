# Keyed locks: model, metrics & leak troubleshooting

This document describes the CSI driver's **in-process keyed locks** — why they
exist, how they are structured, the metrics that make them observable, and how to
tell a healthy busy driver from a lock leak. It is both the design reference for
the lock instrumentation (`common.LockProbe`) and the troubleshooting runbook for
the "provisioning is wedged" class of incidents.

If you are staring at a Grafana tile that says "100 locks held" and wondering
whether that is a problem, jump to [§6](#6-reading-the-tiles-normal-vs-leak).

---

## 1. Why the driver holds locks

CSI RPCs for the *same* object can arrive concurrently and overlap: the external
sidecars retry, and Kubernetes can re-drive a `CreateVolume`/`DeleteVolume` while
the previous attempt is still running. Two creates for the same volume, or a
create racing a delete, would corrupt state on the Anvil. The driver therefore
serialises work **per object key** with an in-memory lock, so that at most one
RPC mutates a given key at a time.

Locks are **keyed by name, not by RPC** — the key is a volume name, a backing
share name, or a snapshot name. This matters: it is why a single overloaded
*backing share* can serialise thousands of unrelated file-backed volumes
([§4](#4-why-held-pins-at-100)).

## 2. The lock primitive

All of this lives in `pkg/driver/driver.go`:

```go
type keyLock struct {
    sem *semaphore.Weighted // weight=1 → acts like a mutex
}
func (kl *keyLock) lock(ctx context.Context) error { return kl.sem.Acquire(ctx, 1) }
func (kl *keyLock) unlock()                         { kl.sem.Release(1) }
```

- **`volumeLocks` / `snapshotLocks`** — two `map[string]*keyLock`, guarded by
  `locksMu`. A key's `keyLock` is created lazily on first use and kept.
- **Weight-1 semaphore, not `sync.Mutex`.** The reason is the **context**: a
  `sync.Mutex` cannot be acquired with a timeout, but `semaphore.Weighted.Acquire`
  takes a `ctx` and returns when it is cancelled. That gives us the bounded wait
  below.
- **Non-reentrant.** A goroutine that holds key `K` must not try to acquire `K`
  again — it would self-deadlock until the timeout. Nested acquisitions must
  always be of *different* keys (see the create path in
  [§3](#3-which-rpcs-take-which-locks)).

Acquisition is wrapped with a **30-second deadline**:

```go
probe := common.StartLockProbe(ctx, "volume")
lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
if err := lk.lock(lctx); err != nil {
    probe.Failed()
    return nil, status.Errorf(codes.Aborted, "could not acquire volume lock for %s: %v", volID, err)
}
release := probe.Acquired()
return func() { lk.unlock(); release() }, nil
```

If the lock cannot be acquired within 30s the RPC fails with **`codes.Aborted`**,
which is retryable — the sidecar backs off and re-drives it. This is a pressure
valve, not an error: under heavy contention you will see a background rate of
Aborted creates that succeed on retry. A *sustained, growing* Aborted rate is the
signal that a holder is stuck ([§6](#6-reading-the-tiles-normal-vs-leak)).

## 3. Which RPCs take which locks

| RPC | Lock key(s) | Notes |
|---|---|---|
| `CreateVolume` (file-backed) | `volumeName`, then **`backingShareName`** | Two locks, nested. The volume key is unique per PVC; the backing-share key is **shared by every file-backed volume on that StorageClass**. |
| `CreateVolume` (share-backed) | `volumeName` (+ `backingShareName` when ensuring the parent share) | Each volume is its own share, so there is no shared serialisation point. |
| `DeleteVolume` | `volumeId` | |
| `CreateSnapshot` | `snapshotLocks[req.Name]` | Separate map; `lock_type="snapshot"`. |
| create-from-snapshot | `residingShareName` | |

The key line for troubleshooting: **file-backed `CreateVolume` acquires its unique
volume lock, then contends for the single shared backing-share lock.** All
file-backed provisioning on one StorageClass funnels through that one key.

## 4. Why "held" pins at ~100

The height of `hs_csi_locks_held` during a provisioning storm is **not** set by
how many PVCs you submit. It is set by how many `CreateVolume` RPCs run
*concurrently*, which is the **external-provisioner sidecar's `--worker-threads`
(default 100)**.

> Naming caveat: "csi-provisioner" is overloaded. The controller **pod**
> `csi-provisioner-0` bundles five containers — our driver
> (`hs-csi-plugin-controller`) plus the upstream sidecars `csi-provisioner`,
> `csi-attacher`, `csi-snapshotter`, `csi-resizer`. The `--worker-threads` flag
> is on the upstream **`csi-provisioner` container** (the external-provisioner),
> *not* on our driver. That sidecar is the throttle; our driver is what holds the
> locks and has no concurrency cap of its own. (Node-side RPCs come from the
> separate `csi-node` DaemonSet, container `hs-csi-plugin-node`.)

Walk through a burst of 4,000 file-backed PVCs against one StorageClass:

1. The provisioner dispatches **100** `CreateVolume` calls at a time (the other
   3,900 sit in its workqueue).
2. Each of those 100 immediately acquires its **unique volume lock** → **~100
   held**.
3. All 100 then queue on the **single shared backing-share lock**. Exactly **one**
   holds it (→ +1) and does its Anvil work; the other 99 block in `lock()`
   (they have *not* incremented `held` yet — the probe counts a lock as held only
   once `Acquire` returns).

So steady-state `held ≈ 100 volume locks + 1 backing-share lock ≈ 101`, and it
stays there for the whole burst because as one create finishes the provisioner
immediately dispatches the next queued PVC into the freed worker slot.

**"Did we not push hard enough?"** Submitting *more* volumes (10k, 100k) does not
raise this ceiling — it only makes the burst last longer. The concurrency is
clamped at 100 upstream of the driver. To actually push the lock manager harder
you change the *concurrency*, not the count:

- **Raise `--worker-threads`** on the `csi-provisioner` container (e.g. 300–500).
  That drives proportionally more concurrent volume locks and, for file-backed,
  a *deeper queue* on the one backing-share lock → longer `lock_wait`, and once
  waits cross 30s, a rising `acquire_failures_total`. This is the single most
  effective way to stress the lock/timeout path.
- **Spread across StorageClasses** to multiply *backing-share* keys — but note
  that for file-backed the real throughput ceiling is the Anvil's metadata-op
  rate (~3/s per backing share on a single Anvil), not the lock, so this raises
  lock *breadth* more than depth.

There is no cap inside the driver itself; the map grows to as many distinct keys
as are in flight.

## 5. Metrics — the `LockProbe` contract

`common.StartLockProbe(ctx, lockType)` returns a probe with a two-outcome
lifecycle, wired in `pkg/common/metrics.go`. Every acquisition ends in exactly
one of `Failed()` or `Acquired()`; the closure returned by `Acquired()` is
called on release.

| Metric | Type | Emitted when | Meaning |
|---|---|---|---|
| `hs_csi_locks_held` | up/down counter (gauge) | `+1` on acquire, `-1` on release | Locks currently held. **A leak shows as a stuck non-zero value.** |
| `hs_csi_lock_wait_seconds` | histogram | on acquire **and** on failure | Time blocked waiting for the lock. `_count` = every attempt that resolved. |
| `hs_csi_lock_hold_seconds` | histogram | on release | How long the lock was held. **`_count` = number of releases.** |
| `hs_csi_lock_acquire_failures_total` | counter | on the 30s timeout | Acquisitions that gave up → `codes.Aborted`. |

All carry `lock_type` (`volume` \| `snapshot`); the scrape adds `role`
(`controller` \| `node`) and `instance`. Controller serves `:9090`, node
DaemonSet `:9091`.

### The counter identity (why the leak detector works)

These four are not independent — they satisfy an exact conservation law, verified
live on a running driver:

```
wait_count  = hold_count + acquire_failures_total      (every attempt either releases or times out)
locks_held  = successful_acquires − releases           (what's still held)
```

The consequence that powers [§6](#6-reading-the-tiles-normal-vs-leak):
**`rate(hs_csi_lock_hold_seconds_count[…])` is the rate of lock *releases*** — i.e.
"are locks still turning over?" A healthy driver under load releases constantly;
a leaked lock is acquired and *never* released. That release rate, not the height
of `locks_held`, is what distinguishes load from a leak.

## 6. Reading the tiles: normal vs leak

`hs_csi_locks_held > 0` is **expected** during provisioning — every in-flight
`CreateVolume`/`DeleteVolume` holds a key. Height alone means nothing. The Grafana
"CSI keyed locks" row is built around the release-rate discriminator so you never
have to guess:

**`Lock leak verdict`** (green/red stat) — the authoritative signal:

```promql
count(
  (min_over_time((sum by (role,instance)(hs_csi_locks_held))[10m:30s]) > 0)
  and on(role,instance)
  (sum by (role,instance)(rate(hs_csi_lock_hold_seconds_count[10m])) == 0)
) or vector(0)
```

Red **only** when, per role+instance, locks never returned to zero for 10 minutes
**and** zero releases happened in that window (held-and-not-releasing). It stays
green through any amount of honest load, because under load the release rate is
never zero.

**`Stuck locks (10m idle floor)`** — `sum(min_over_time((sum by (role,instance)(hs_csi_locks_held))[10m:30s]))`.
The lowest held count over 10 min. `0` = locks fully drain between ops. `>0` =
they never fully drained — either sustained load or a leak; the verdict tile
disambiguates. Use it as an early-warning magnitude.

**`Locks held vs. release rate`** — dual-axis timeseries overlaying
`sum by (role)(hs_csi_locks_held)` against
`sum by (role)(rate(hs_csi_lock_hold_seconds_count[1m]))`. The teaching view:
healthy = both lines rise together; leak = held stays up while releases fall to 0.

**Signature of a real leak**, all at once:

- `hs_csi_locks_held` stuck at a non-zero value with **no** downward movement,
- `rate(hs_csi_lock_hold_seconds_count)` at/near **0** (no releases),
- `hs_csi_lock_acquire_failures_total` **climbing** (everyone else times out on
  the wedged key → a storm of `codes.Aborted`),
- new `CreateVolume`s for the affected StorageClass all failing Aborted.

## 7. Root cause seen in the field & remediation

The leak this instrumentation was built to catch: after **re-pointing the driver
to a new Anvil** (or terminating one), a stale NFS mount to the *old, dead* Anvil
is left behind at `<ShareStagingDir=/tmp><exportPath>`. It is host-propagated and
survives pod restarts. `EnsureBackingShareMounted`'s `IsShareMounted` then hangs
uninterruptibly `stat`-ing the dead mount **while holding the volume and
backing-share locks** → both leak → every file-backed `CreateVolume` on that
backing share wedges behind the one held key. The node side is symmetric
(`NodeUnstage`/`NodeUnpublish` umount hangs → node lock leak).

**Manual remediation** (until the fix below is deployed):

```sh
# On each controller/node pod: force-unmount every stale mount to the dead Anvil IP
for mp in $(grep nfs /proc/mounts | grep -v <CURRENT_ANVIL_IP> | awk '{print $2}'); do
  umount -f -l "$mp"
done
# then restart the CSI pods so the leaked in-memory locks are released
kubectl -n kube-system delete pod csi-provisioner-0
kubectl -n kube-system delete pod -l app=csi-node
```

**Code fix** (`fix/scale-reliability`): `IsShareMounted` is now timeout-bounded
(`SafeIsMountPoint`, returns false on timeout instead of hanging), `MountShare`
bounds the mount syscall, and `EnsureBackingShareMounted` `umount -f -l`'s a stale
mount before re-mounting. This removes the hang that caused the leak, so a stale
mount after an Anvil swap self-heals instead of wedging the lock.

## 8. Rationale (why it looks like this)

- **Gauge, not just a counter.** A held-lock leak is a *level*, not a rate. An
  up/down counter that returns to 0 between operations is the most direct
  possible encoding of "is anything stuck right now."
- **Release rate as the discriminator.** We deliberately did *not* alert on
  `locks_held > N` — that fires on healthy load and trains operators to ignore it.
  Gating on "held **and not releasing**" is what makes the verdict trustworthy
  enough to page on.
- **`lock_type` label only.** No per-volume key on any lock metric — cardinality
  stays bounded exactly as in the main observability doc.
- **30s Aborted over blocking forever.** A bounded wait turns a stuck key into a
  visible, retryable, *localised* failure instead of an invisible pile-up of
  goroutines.
