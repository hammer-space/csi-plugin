# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).


## [1.3.0]
### Added
- `objectiveTarget` StorageClass parameter (`share` (default) | `file` | `both`) for file-backed volumes. With the default `share`, CreateVolume skips the per-file objective-set and the Anvil file-visibility poll that only exists to gate it — the backing share already carries the objectives — so provisioning returns as soon as the local `mkfs` completes and the `GET /files` poll storm under concurrency is eliminated. Use `file`/`both` to also apply per-file objectives.
- OpenTelemetry tracing and Prometheus metrics for the driver (`OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, etc.); `hs_csi_operation_*` and `hs_csi_anvil_requests_total` instruments across the controller/node RPCs, the file- and share-backed provisioning steps, and every Anvil REST call. See `docs/observability.md`.
- Minimum size gates for file-backed volumes (xfs < 300 MiB, ext4 < 20 MiB rejected) and an `fsfreeze` of the source volume before snapshot for crash-consistent file-backed snapshots.
- `deploy/kubernetes/kubernetes-1.3{4,5,6}/plugin.yaml` — manifests for the currently supported k8s minors, validated end-to-end on live 1.34 and 1.35 clusters (1.29 base + host-networked metrics port + OTel env vars).
- `deploy/monitoring/` — importable Grafana dashboard (`hs-csi-driver`), an example VictoriaMetrics/Prometheus scrape config, and a wiring README.
- Unit tests for `objectiveTarget` parsing, the file-backed size gates, the file/share volume-ID discriminator, the Anvil route-template normalization, `MeasureOp`, and the lock-timeout→`codes.Aborted` behavior.

### Changed
- Parallelized file-backed CreateVolume by narrowing the per-backing-share lock, plus `mkfs` tuning (ext4 lazy-init, `mkfs.xfs -K` over NFS). See `docs/file-backed-performance.md`.
- Decide file- vs share-backed structurally from the volume ID instead of a `GetShare` probe that 404s for file-backed sources.
- Task-completion polling uses a fixed 2s/30s-then-4s cadence instead of exponential backoff. See `docs/tunable-retry-parameters.md`.

### Removed
- Dropped `ext3` as a supported file-backed filesystem; `CreateVolume` now rejects `fsType=ext3` with `InvalidArgument` (use `ext4` or `xfs`).

### Fixed
- `releaseBackingMount` no longer holds `mountRefsMu` while calling `UnmountBackingShareIfUnused` (which re-acquires the same non-reentrant mutex via the `mountRefs` refcount check): the volume that dropped the last reference self-deadlocked, wedging all subsequent file-backed provisioning. The decrement now happens under the lock and the unmount runs after releasing it. Caught by live xfs validation (a single file-backed PVC is the exact refcount-1 trigger).
- `acquireVolumeLock`/`acquireSnapshotLock` return `codes.Aborted` on a lock-acquire timeout instead of calling `os.Exit(1)`, which under concurrent load crashed the whole controller.
- Only force-unmount a stale backing-share mount after repeated (not a single) mount-check timeouts, so a slow-but-healthy NFS stat under concurrency can't force-unmount a live shared mount out from under in-flight pods.
- `UnmountBackingShareIfUnused` now honors the `mountRefs` refcount, so a concurrent delete can't unmount a backing share out from under an in-flight `mkfs` (which has no loop device yet).
- Run snapshot `Unfreeze` on a context detached from the gRPC request cancellation, so a cancelled/expired `CreateSnapshot` can't leave the source pod's filesystem frozen.
- `AnvilRoute` collapses `share-snapshots` share/snapshot identifiers to `{id}`, preventing unbounded `hs_csi_anvil_requests_total` metric cardinality.
- Survive a stale/dead backing-share NFS mount (timeout-bounded mount + force-unmount before remount) instead of leaking the lock and wedging serialized provisioning. See `docs/node-unmount-recovery.md`.
- Route file-backed snapshot deletes to the file-snapshot API instead of always calling the share-snapshot delete.

## [1.2.9]
### Fixed
- Included share objectives in share create requests instead of applying them with follow-up objective-set calls after provisioning.
- Treated all known terminal task states consistently so failed, halted, cancelled, validation-failed, and resumed tasks stop polling and report failure.
- Avoided passing StorageClass NFS mount options to local filesystem mounts for file-backed volumes; those options are now only used for the backing NFS share mount.
- Prevented duplicate or conflicting NFS version mount flags by treating both `nfsvers=` and `vers=` as version options, removing them before fallback retries, and passing NFSv3 fallback options as separate mount arguments.
- Honored the StorageClass `fqdn` parameter in deployments without data-portals (e.g. Anvil-only/no-DSX). Previously the resolved FQDN/floating IP was discarded because the mount loops only ran per data-portal, so an empty `data-portals` list caused provisioning to fail with `could not mount to any data-portals`.

### Changed
- Removed the driver-specific `clientMountOptions` StorageClass parameter; CSI node mounts now rely on Kubernetes `mountOptions` / CSI `mountFlags`.
- Added `dnsPolicy: ClusterFirstWithHostNet` to the Kubernetes 1.29 plugin manifest for host-networked CSI pods.

### Documentation
- Clarified supported CSI compatibility modes (`CSI_MAJOR_VERSION=1` and `CSI_MAJOR_VERSION=0`).
- Documented Kubernetes compatibility ranges and the legacy CSI v0.3 deployment path.

## [1.2.8]
### Added
 - OpenTelemetry Tracing: Integrated OpenTelemetry-based tracing across all API calls using standard W3C traceparent propagation.
 - Injected trace ID into all HTTP headers for REST API communication with Hammerspace.
 - Configured otel.TracerProvider using the hammerspace-csi instrumentation scope for enhanced observability and trace correlation.
 - NFS Root Share Mounting: Introduced support for mounting a root share for NFS-backed volumes.
 - Volumes now use bind mounts from this root instead of individual share mounts per volume.
 - mountBackingShareName Parameter: Added support for mountBackingShareName in the NFS storage class.

 - When specified, the actual NFS volume directories will be created inside this parent share.
 - Improved Logging: Added more detailed log messages throughout the CSI plugin for better operational clarity and troubleshooting.

### Changed
 - Mount Behavior for Volume Types:
   - For NFS volumes, the plugin mounts a single root share once per node and uses bind mounts per volume.

   - For file-backed and block volumes, the CSI driver continues to mount the share defined in the storage class and then perform bind mounts as before.

### Fixed
- Ensured trace ID is not dropped during API retries or chained calls.

### Security
- Reviewed HTTP client configuration for trace propagation compatibility with secure endpoints.

## 1.2.7
### Fixed
- Resolved an issue in `NodeGetVolumeStats` where excessive backend `GetShare` API calls were triggered for NFS volumes, causing SM log flooding. The function now uses `syscall.Statfs` directly on the volume mount path to obtain usage metrics, reducing API load.
- Improved `CleanupLoopDevice` to retry loop device detachment up to 3 times with a 1-second interval, ensuring better reliability when devices are temporarily busy.

### Added
- Introduced support for configurable unmount retry behavior using environment variables (`UNMOUNT_RETRY_COUNT`, `UNMOUNT_RETRY_INTERVAL`), which can be injected via Kubernetes `ConfigMap`.
- Enhanced FIP (Floating IP) selection logic to support **strict round-robin ordering** for multi-portal NFS mounts:
  - The CSI driver now maintains a rotating index to evenly distribute data access across available portal IPs.
  - This reduces hotspotting and improves throughput in clusters with multiple floating IPs.
  - If an FQDN is configured and resolves to a reachable NFS endpoint, it is used directly; otherwise, the round-robin FIP selection is attempted in order.

### Changed
- **Production image**: Replaced CentOS 8 UBI base image with **Rocky Linux 9 UBI** for better long-term support and compatibility with modern Python 3 and security patches.
- **Development image**: Updated `Dockerfile_dev` to use `golang:1.24-alpine`, removed CentOS dependencies, and transitioned to musl-based Alpine packages. Python `hstk` tool is now installed in a virtual environment to comply with PEP 668.


## 1.2.6
### Fixed Bug
- Fixed error where floating IP's is not being used. 

## 1.2.5
### Added
- Fixed error to get volume capability due to change in type fromat. (Fix breaking changes only work with thor2 and above)
- Added List volumes support
- Tested working with go 1.21.3 and k8 v1.27.4

## 1.2.4
### Added
- Fixed error while creating share to track task status.
- Added check for nfs mount before attempting to delete nfs mount volume.
- Update docker dev file to use go lang v:1.21 and gocsi v:1.2.2 

## 1.2.3
### Added
- Updated the deprecated module
- Added url parse to URI string, it was crashing the CSI when share name have % in it
- Added condition to expand volume only when share state is Mounted state 

## 1.2.2
### Added
- Added share name length restriction to 80 characters.
## 1.2.1
### Added
- Removed unnecessary mount option

## 1.2.0
### Added
- Supports online resize of file-backed devices
- Switch to UBI image
- Version update to UBI 8.4

## 1.0.4
### Added
- Support for Portal Floating IPs

## 1.0.3
### Added
- Support for share descriptions

## 1.0.2
### Added
- Support for Hammerspace 4.4.0
- Tested with Kubernetes up until 1.18
- Support for resize for NFS (without remount) and file-backed (requires remount) volumes

## 1.0.1
### Added
- Support for Hammerspace 4.3.0
- Tested with Kubernetes 1.16, 1.17

## 1.0.0
### Added
- Topology key ``topology.csi.hammerspace.com/is-data-portal``
- Ability to set additional metadata tags on plugin created shares and files
- Kubernetes 1.14 deployment manifests

### Changed
- Golang version 1.10 -> 1.12

## 0.1.3
### Added
- Default size (1GB) for file-backed volumes
- Support for filesystems other than nfs for Mount Volumes

### Changed
- Wait for file-backed volumes to exist on metadata server before responded successful for create
- Return error if source snapshot does not exist

### Fixed
- Issue where block volume snapshots may not be properly deleted

## 0.1.2
### Added
- Ability to specify export path prefix to use when mounting to a data portal HS_DATA_PORTAL_MOUNT_PREFIX
- Command execution timeout of 5 minutes
- Support for CSI spec v0.3 (Kubernetes 1.10-1.12)

### Changed
- Combined Kubernetes Deployment yaml files
- HS_TLS_VERIFY defaults to false
- Require HTTPS communication with Hammerspace API

### Fixed
- Set objectives on file for block volumes

## 0.1.1
### Fixed
- Include required CA root certificates in docker image

## 0.1.0
### Added
- Support for CSI spec 1.0
  - Mounted Volumes
    - Create
    - Delete
    - Snapshot
    - Publish/Unpublish
  - Block Volumes
      - Create
      - Create from snapshot source
      - Delete
      - Snapshot
      - Publish/Unpublish
