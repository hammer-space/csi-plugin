# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).


## [Unreleased]

## [1.2.8]
### Added
- Added OpenTelemetry-based tracing to all API calls using standard W3C `traceparent` propagation.
- Injected trace ID into all HTTP headers for communication with Hammerspace REST API.
- Configured `otel.TracerProvider` with `hammerspace-csi` instrumentation scope for better observability.

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
