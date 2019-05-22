# Hammerspace CSI Volume Plugin

This plugin uses Hammerspace storage backend as distributed data storage for containers.

Supports [CSI Spec 1.0.0](https://github.com/container-storage-interface/spec/blob/master/spec.md) 
 
Implements the Identity, Node, and Controller interfaces as single Golang binary.
 
#### Supported Capabilities:
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
Block Volume
- Storage is exposed to the container as a block device
- Exists as a special device file on a Hammerspace share (backing share)

Mounted (shared filesystem) volume
- Storage is exposed to the container as a directory
- Exists as a Hammerspace share

## Plugin Dependencies

Ensure that nfs-utils is installed on the hosts

Ubuntu
```bash
$ apt install nfs-common
```

Centos
```bash
$ yum install nfs-utils
```

The plugin container(s) must run as privileged containers

## Installation
Kubernetes specific deployment instructions are located at [here](https://github.com/hammer-space/csi-plugin/blob/master/deploy/kubernetes/README.md)

### Configuration
Configuration parameters for the driver (Passed as environment variables to plugin container):

``*`` Required

Variable                       |     Default           | Description
----------------               |     ------------      | -----
*``CSI_ENDPOINT``              |                       | Location on host for gRPC socket (Ex: /tmp/csi.sock)
*``CSI_NODE_NAME``             |                       | Identifier for the host the plugin is running on
``CSI_USE_ANVIL_FOR_DATA``     |     ``true``          | Whether to try mount shares as connections to the Anvil server over pNFS. If false, data-portals are used.
*``HS_ENDPOINT``               |                       | Hammerspace API gateway
*``HS_USERNAME``               |                       | Hammerspace username
*``HS_PASSWORD``               |                       | Hammerspace password
``HS_TLS_VERIFY``              |     ``false``         | Whether to validate the Hammerspace API gateway certificates
``HS_DATA_PORTAL_MOUNT_PREFIX``|                       | Override the prefix for data-portal mounts. Ex "/hs"
``CSI_MAJOR_VERSION``          |     ``"1"``           | The major version of the CSI interface used to communicate with the plugin. Valid values are "1" and "0"

## Usage
Supported volume parameters for CreateVolume requests (maps to Kubernetes storage class params):

Name                     |     Default            | Description
----------------         |     ------------       | -----
``exportOptions``        |                        | Export options applied to shares created by plugin. Format is  ';' seperated list of subnet,access,rootSquash. Ex ``*,RW,false; 172.168.0.0/20,RO,true``
``deleteDelay``          |     ``-1``             | The value of the delete delay parameter passed to hammerspace when the share is deleted. '-1' implies Hammerspace cluster defaults
``volumeNameFormat``     |     ``%s``             | The name format to use when creating shares or files on the backend. Must contain a single '%s' that will be replaced with unique volume id information. Ex: ``csi-volume-%s-us-east``
``objectives``           |     ``""``             | Comma separated list of objectives to set on created shares in addition to default objectives.
``blockBackingShareName``|                        | The share in which to store Block Volume files. If it does not exist, the plugin will create it. Alternatively, a preexisting share can be used. Must be specified if provisioning Block Volumes.


## Development
### Requirements
* Docker
* Golang 1.10+
* nfs-utils
* vgo - `go get -u golang.org/x/vgo`

**NOTE** Workaround for [go issue #24773](https://github.com/golang/go/issues/24773)
```bash
sudo mkdir /usr/lib/go-1.10/api
sudo touch /usr/lib/go-1.10/api/go1.10.txt
```

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
Manual tests can be facilitated by using the Dev Image. Local files can be shared with the container to facilitate testing.

Example Usage:

Running the image - 
```bash
make build-dev
echo "
CSI_ENDPOINT=/tmp/csi.sock
HS_ENDPOINT=https://anvil.example.com
HS_USERNAME=admin
HS_PASSWORD=admin
HS_TLS_VERIFY=false
CSI_NODE_NAME=test
CSI_USE_ANVIL_FOR_DATA=true
SANITY_PARAMS_FILE=/tmp/csi_sanity_params.yaml
 " >  ~/csi-env
echo "
blockBackingShareName: test-csi-block
deleteDelay: 0
objectives: "test-objective"
" > ~/csi_sanity_params.yaml
docker run --privileged=true \
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
CSI_DEBUG=true csc node get-info
csc -h
```


#### Running unit tests
``make unittest``

#### Running Sanity tests
These tests are functional and will create and delete volumes on the backend.

Must have connections from the host to the HS_ENDPOINT. This can be run from within the Dev image.
Uses the [CSI sanity package](https://github.com/kubernetes-csi/csi-test/tree/master/cmd/csi-sanity)

Make a parameters
```bash
echo "
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
export CSI_USE_ANVIL_FOR_DATA=true
export CSI_NODE_NAME=test
export SANITY_PARAMS_FILE=~/csi_sanity_params.yaml
make sanity
```