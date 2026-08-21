# Hammerspace CSI Volume Plugin

This plugin uses Hammerspace backend as distributed data storage for containers.

Supports CSI Spec v1.9.0 for `CSI_MAJOR_VERSION=1` and legacy CSI Spec v0.3.0 compatibility for `CSI_MAJOR_VERSION=0`.
 
Implements the Identity, Node, and Controller interfaces as single Golang binary.

## Compatibility

CSI Mode | CSI Spec Version | `CSI_MAJOR_VERSION` | Compatibility | Notes
-------- | ---------------- | ------------------- | ------------- | -----
CSI v1   | v1.9.0           | `1`                 | Kubernetes 1.29, 1.34–1.36 | Default mode for current deployments. There is no blanket minimum — deploy the per-minor manifest matching your cluster's version. **1.34–1.36** are the validated set and **1.29** is bundled for older clusters; manifests for minors older than 1.29 (1.13 through 1.28) are archived, unsupported, under `deploy/kubernetes/archive/`.
CSI v0.3 | v0.3.0           | `0`                 | Kubernetes 1.10-1.12 | Legacy compatibility mode. Supports filesystem (Mount) volumes only. See `deploy/kubernetes/archive/kubernetes-1.10-1.12/README.md`.
 
#### Supported Capabilities
Controller service
* CREATE_DELETE_VOLUME
* LIST_VOLUMES
* GET_CAPACITY
* CREATE_DELETE_SNAPSHOT
* LIST_SNAPSHOTS
* EXPAND_VOLUME (online and offline)

Node service
* STAGE_UNSTAGE_VOLUME
* GET_VOLUME_STATS
* EXPAND_VOLUME

Plugin
* CONTROLLER_SERVICE
* VOLUME_ACCESSIBILITY_CONSTRAINTS (topology — see [Topology support](#topology-support))

#### Unsupported Capabilities
The driver does not attach volumes (`ControllerPublishVolume` is not implemented);
volumes are staged and published on the node.

* CLONE_VOLUME — cloning a PVC directly. Restoring from a snapshot is supported.
* PUBLISH_UNPUBLISH_VOLUME, PUBLISH_READONLY, LIST_VOLUMES_PUBLISHED_NODES
* GET_VOLUME, VOLUME_CONDITION (controller and node)
* SINGLE_NODE_MULTI_WRITER (controller and node)
* MODIFY_VOLUME (VolumeAttributesClass)
* VOLUME_MOUNT_GROUP (node)
* GROUP_CONTROLLER_SERVICE (volume group snapshots)

## Volume Types

The driver can provision three kinds of volume. Which one you get is decided by
the `fsType` StorageClass parameter and the PVC's `volumeMode` — you do not
choose it directly.

**Share-backed volume — a shared folder over NFS.** Each volume is its own
Hammerspace share, mounted over NFS. Many pods on many nodes can read and write
it at the same time, and it can be grown while in use. This is the default
(`fsType: nfs`) and the right answer for most workloads.

**File-backed volume — a private disk for one pod.** The volume is a single
large file on a backing share, formatted with ext4 or xfs and mounted as a
loop device. Because it is a real local filesystem, it behaves exactly like a
normal disk — useful for software that depends on POSIX semantics that NFS does
not provide, such as file locking or `O_DIRECT`. Only one pod may use it at a
time.

**Block volume — a raw device.** The same idea as a file-backed volume, except
the driver hands the container an unformatted block device and the application
decides what to put on it. Used by databases and other software that manages its
own on-disk format.

| | Share-backed | File-backed | Block |
| --- | --- | --- | --- |
| How to request | `fsType: nfs` (default) | `fsType: ext4` or `xfs` | `volumeMode: Block` in the PVC |
| Appears in the container as | A directory | A directory | A raw device |
| Shared between pods | **Yes** (`ReadWriteMany`) | No — one pod | No — one pod |
| Backing share required | No | Yes (`mountBackingShareName`) | Yes (`blockBackingShareName`) |
| Grow while in use | **Yes** | Needs a pod restart | Needs a pod restart |
| Minimum size | — | xfs 300 MiB, ext4 20 MiB | — |
| Snapshot type | Hammerspace share snapshot | File snapshot (source frozen briefly) | File snapshot |
| At high volume counts | One Hammerspace share per volume, so the Anvil management API is the limit | **Scales to thousands of volumes** | **Scales to thousands of volumes** |

#### Which should I use?

* **Start with share-backed.** It is the default, needs no backing share, is the
  only type several pods can share, and grows without disruption.
* **Use file-backed** when an application misbehaves on NFS — typically file
  locking, `O_DIRECT`, or a database that refuses to run on a network filesystem
  — and only one pod needs the data.
* **Use block** when the application wants a raw device and manages its own
  format.
* **Prefer file-backed when provisioning very large numbers of volumes.** Each
  share-backed volume is a separate Hammerspace share, and every share create is
  an Anvil management task, so the management API becomes the limiting factor as
  volume counts grow. File-backed volumes are files inside a single backing
  share, so the per-volume work is a local `mkfs` that parallelizes across nodes
  — environments provisioning thousands of PVCs should use file-backed. See
  [`docs/file-backed-performance.md`](docs/file-backed-performance.md).

`ext3` is not supported; use `ext4` or `xfs`.

## Plugin Dependencies

Ensure that NFS client support is installed on the Kubernetes hosts.

Debian, Ubuntu, and derivatives
```bash
$ apt install nfs-common
```

Red Hat Enterprise Linux and compatible distributions (RHEL, Rocky Linux,
AlmaLinux, CentOS Stream, Fedora)
```bash
$ dnf install nfs-utils    # or: yum install nfs-utils
```

SUSE Linux Enterprise and openSUSE
```bash
$ zypper install nfs-client
```

The plugin container(s) must run as privileged containers

## Installation
Kubernetes specific deployment instructions are located at [here](https://github.com/hammer-space/csi-plugin/blob/master/deploy/kubernetes/README.md)

### Configuration
Configuration parameters for the driver (passed as environment variables to plugin container):

``*`` Required

Variable                       |     Default           | Description
----------------               |     ------------      | -----
*``CSI_ENDPOINT``              |                       | Location on host for gRPC socket (Ex: /tmp/csi.sock)
*``CSI_NODE_NAME``             |                       | Identifier for the host the plugin is running on
*``HS_ENDPOINT``               |                       | Hammerspace API gateway
*``HS_USERNAME``               |                       | Hammerspace username for the driver. Use a dedicated least-privilege account rather than a full administrator — see [`deploy/kubernetes/SECRETS.md`](deploy/kubernetes/SECRETS.md)
*``HS_PASSWORD``               |                       | Hammerspace password
``HS_TLS_VERIFY``              |     ``false``         | Whether to validate the Hammerspace API gateway certificates
``CSI_MAJOR_VERSION``          |     ``"1"``           | CSI interface compatibility mode. Use ``"1"`` for Kubernetes 1.13+ deployments and ``"0"`` only for legacy Kubernetes 1.10-1.12 environments.
``LOG_LEVEL``                  |     ``info``          | Log verbosity: ``panic``, ``fatal``, ``error``, ``warn``, ``info``, ``debug``, or ``trace``. ``debug`` logs every Anvil REST call, so use it for troubleshooting rather than steady-state operation. An unrecognized value falls back to ``info``.

## Usage
Supported volume parameters for CreateVolume requests (maps to Kubernetes storage class params):

Name                      |     Default            | Description
----------------          |     ------------       | -----
``exportOptions``         |                        | Export options applied to shares created by plugin. Format is  ';' seperated list of subnet,access,rootSquash. Ex ``*,RW,false; 172.168.0.0/20,RO,true``
``deleteDelay``           |     ``-1``             | The value of the delete delay parameter passed to Hammerspace when the share is deleted. '-1' implies Hammerspace cluster defaults.
``volumeNameFormat``      |     ``%s``             | The name format to use when creating shares or files on the backend. Must contain a single '%s' that will be replaced with unique volume id information. Ex: ``csi-volume-%s-us-east``
``objectives``            |     ``""``             | Comma separated list of objectives to set on created shares and files in addition to default objectives.
``blockBackingShareName`` |                        | The share in which to store Block Volume files. If it does not exist, the plugin will create it. Alternatively, a preexisting share can be used. Must be specified if provisioning Block Volumes.
``mountBackingShareName`` |                        | The share in which to store File-backed Mount Volume files. If it does not exist, the plugin will create it. Alternatively, a preexisting share can be used. Must be specified if provisioning Filesystem Volumes other than 'nfs'.
``fsType``                |     ``nfs``            | The file system type to place on created mount volumes. If a value other than "nfs", then a file-backed volume is created instead of an NFS share.
``additionalMetadataTags``|                        | Comma separated list of tags to set on files and shares created by the plugin. Format is ',' separated list of key=value pairs. Ex ``storageClassName=hs-storage,fsType=nfs``

Use the Kubernetes StorageClass ``mountOptions`` field for mount flags applied to CSI node mounts. Example:
```yaml
mountOptions:
  - vers=4.2
  - hard
  - timeo=600
```

### Topology support
Currently, only the ``topology.csi.hammerspace.com/is-data-portal`` key is supported. Values are 'true' and 'false'

## Development
### Requirements
* Docker
* Golang 1.12+
* nfs-utils

### Building
##### Build a new docker image from local source:
```sudo make build```

##### Build a new release:
Update VERSION file, then

```bash
make build-release
```

##### Publish a new release
```bash
docker push hammerspaceinc/csi-plugin:$(cat VERSION)
```


### Testing
#### Manual tests
Manual tests can be facilitated by using the Dev Image. Local files can be exposed to the container to facilitate iterative development and testing.

Example Usage:

Building the image - 
```bash
make build-dev
```
Create ENV file for plugin and csi-sanity configuration.
```bash
echo "
CSI_ENDPOINT=/tmp/csi.sock
HS_ENDPOINT=https://anvil.example.com
HS_USERNAME=admin
HS_PASSWORD=admin
HS_TLS_VERIFY=false
CSI_NODE_NAME=test
SANITY_PARAMS_FILE=/tmp/csi_sanity_params.yaml
 " >  ~/csi-env
 ```
 
 Create params file for csi-sanity (defines the parameters passed to CreateVolume)
 ```bash
 echo "
 blockBackingShareName: test-csi-block
 deleteDelay: 0
 objectives: "test-objective"
 " > ~/csi_sanity_params.yaml
 ```
 
Running the image - 
```bash
docker run --privileged=true \
--cap-add ALL \
--cap-add CAP_SYS_ADMIN \
-v /tmp/:/tmp/:shared \
-v /dev/:/dev/ \
--env-file ~/csi-env \
-it \
-v ~/csi_sanity_params.yaml:/tmp/csi_sanity_params.yaml \
-v ~/csi-plugin:/csi-plugin/:shared \
--name=csi-dev \
hammerspaceinc/csi-plugin-dev
```

Running CSI plugin in dev image
```bash
make compile # Recompile
./bin/hs-csi-plugin
```

Using csc to call the plugin - 
```bash
# open additional shell into dev container
docker exec -it csi-dev /bin/sh

# use csc tool
## Call GetPluginInfo 
CSI_DEBUG=true CSI_ENDPOINT=/tmp/csi.sock csc identity plugin-info

## Make a 1GB file-backed mount volume
CSI_DEBUG=true CSI_ENDPOINT=/tmp/csi.sock csc controller create --cap 5,mount,ext4 --req-bytes 1073741824 --params mountBackingShareName=file-backed test-filesystem

## Delete volume
CSI_DEBUG=true CSI_ENDPOINT=/tmp/csi.sock csc controller delete  /file-backed/test-filesystem

## Explore additional commands
csc -h
```


#### Running unit tests
``make unittest``

#### Running Sanity tests
These tests are functional and will create and delete volumes on the backend.

Must have connections from the host to the HS_ENDPOINT. This can be run from within the Dev image.
Uses the [CSI sanity package](https://github.com/kubernetes-csi/csi-test/tree/master/cmd/csi-sanity)

Make parameters
```bash
echo "
fsType: nfs
blockBackingShareName: test-csi-block
deleteDelay: 0
objectives: "test-objective"
" > ~/csi_sanity_params.yaml
```

Run sanity tests

```bash
export CSI_ENDPOINT=/tmp/csi.sock
export HS_ENDPOINT="https://anvil.example.com"
export HS_USERNAME=admin
export HS_PASSWORD=admin
export HS_TLS_VERIFY=false
export CSI_NODE_NAME=test
export SANITY_PARAMS_FILE=~/csi_sanity_params.yaml
make sanity
```
