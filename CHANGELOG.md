# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]
### Added
- Topology support

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