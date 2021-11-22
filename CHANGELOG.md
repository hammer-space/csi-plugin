# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).


## [Unreleased]
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
