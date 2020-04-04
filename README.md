# Hammerspace CSI Volume Plugin

This plugin uses Hammerspace backend as distributed data storage for containers.

Supports [CSI Spec 1.1.0](https://github.com/container-storage-interface/spec/blob/master/spec.md) 
 
Implements the Identity, Node, and Controller interfaces as single Golang binary.
 
#### Supported Capabilities
* CREATE_DELETE_VOLUME
* GET_CAPACITY
* CREATE_DELETE_SNAPSHOT
* STAGE_UNSTAGE_VOLUME
* GET_VOLUME_STATS

#### Unsupported Capabilities
* LIST_VOLUMES
* LIST_SNAPSHOTS
* EXPAND_VOLUME
* CLONE_VOLUME

## Volume Types
File-backed Block Volume (raw device)
- Storage is exposed to the container as a raw device
- Exists as a special device file on a Hammerspace share (backing share)

File-backed Mounted volume (filesystem)
- Storage is exposed to the container as a directory
- Exists as a special device file on a Hammerspace share (backing share) which contains a filesystem

Share-backed Mounted volume (shared filesystem)
- Storage is exposed to the container as a directory
- Exists as a Hammerspace share
- Mounted via NFS

## Plugin Dependencies

Ensure that nfs-utils is installed on the Kubernetes hosts

Ubuntu
```bash
$ apt install nfs-common
```

CentOS
```bash
$ yum install nfs-utils
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
*``HS_USERNAME``               |                       | Hammerspace username (admin role credentials)
*``HS_PASSWORD``               |                       | Hammerspace password
``HS_TLS_VERIFY``              |     ``false``         | Whether to validate the Hammerspace API gateway certificates
``HS_DATA_PORTAL_MOUNT_PREFIX``|                       | Override the prefix for data portal mounts. Ex ``/mnt/data-portal``
``CSI_MAJOR_VERSION``          |     ``"1"``           | The major version of the CSI interface used to communicate with the plugin. Valid values are "1" and "0"

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
