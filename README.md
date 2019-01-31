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

The plugin container(s) must run as priviledge containers

## Installation
Kubernetes specific deployment instructions are located at [here](./deploy/kubernetes/README.md)

### Configuration
Configuration parameters for the driver (Passed as environment variables to plugin container):

``*`` Required

Variable                   |     Default           | Description
----------------           |     ------------      | -----
*``CSI_ENDPOINT``          |                       | Location on host for gRPC socket (Ex: /tmp/csi.sock)
*``CSI_NODE_NAME``         |                       | Identifier for the host the plugin is running on
``CSI_USE_ANVIL_FOR_DATA`` |     ``true``          | Whether to try mount shares as connections to the Anvil server over pNFS. If false, data-portals are used.
*``HS_ENDPOINT``           |                       | Hammerspace API gateway
*``HS_USERNAME``           |                       | Hammerspace username
*``HS_PASSWORD``           |                       | Hammerspace password
``HS_TLS_VERIFY``          |     ``true``          | Whether to validate the Hammerspace API gateway certificates

## Usage
Supported volume parameters for CreateVolume requests (maps to Kubernetes storage class params):

Name                     |     Default            | Description
----------------         |     ------------       | -----
``exportOptions``        |                        | Export options applied to shares created by plugin. Format is  ';' seperated list of <subnet>,access,rootSquash. Ex ``*,RW,false; 172.168.0.0/20,RO,true``
``deleteDelay``          |     ``-1``             | The value of the delete delay parameter passed to hammerspace when the share is deleted. '-1' implies Hammerspace cluster defaults
``volumeNameFormat``     |     ``%s``             | The name format to use when creating shares or files on the backend. Must contain a single '%s' that will be replaced with unique volume id information. Ex: ``csi-volume-%s-us-east``
``objectives``           |     ``""``             | Comma separated list of objectives to set on created shares in addition to default objectives.
``blockBackingShareName``|                        | The share in which to store Block Volume files. If it does not exist, the plugin will create it. Alternatively, a preexisting share can be used. Must be specified if provisioning a Block Volume.


## Development
### Requirements
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
``$ make build``

##### Build a new release:
Update VERSION file, then

```bash
make build-release
```

##### Publish a new release
```bash
docker tag hs-csi-plugin:$(cat VERSION)  <registry>/hs-csi-plugin:$(cat VERSION)
docker push <registry>/hs-csi-plugin:$(cat VERSION)
```


### Testing
#### Manual tests
Manual tests can be facilitated by using the CSC tool
```bash
go get github.com/rexray/gocsi
cd src/github.com/rexray/gocsi/csc
go install .
export CSI_ENDPOINT=/tmp/csi.sock
csc -h
```

#### Running unit tests
``$ make unittest``

#### Running Sanity tests
These tests are functional and will create and delete volumes on the backend.

Must have connections from the host to the HS_ENDPOINT.
Uses the [CSI sanity package](
)

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
sudo make sanity
```