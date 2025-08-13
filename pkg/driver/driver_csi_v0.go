/*
Copyright 2019 Hammerspace

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"context"
	"net"
	"sync"
	"time"

	csi_v0 "github.com/ameade/spec/lib/go/csi/v0"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/common"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type CSIDriver_v0Support struct {
	listener net.Listener
	server   *grpc.Server
	wg       sync.WaitGroup
	running  bool
	lock     sync.Mutex
	driver   *CSIDriver
}

func NewCSIDriver_v0Support(driver *CSIDriver) *CSIDriver_v0Support {
	return &CSIDriver_v0Support{
		driver: driver,
	}

}

func (c *CSIDriver_v0Support) goServe(started chan<- bool) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		started <- true
		err := c.server.Serve(c.listener)
		if err != nil {
			panic(err.Error())
		}
	}()
}

func (c *CSIDriver_v0Support) Address() string {
	return c.listener.Addr().String()
}
func (c *CSIDriver_v0Support) Start(l net.Listener) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	log.Infof("Starting gRPC server with CSI v0 support")

	// Set listener
	c.listener = l

	// Create a new grpc server
	c.server = grpc.NewServer(
		grpc.UnaryInterceptor(c.callInterceptor),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time: 5 * time.Minute,
		}),
	)

	csi_v0.RegisterControllerServer(c.server, c)
	csi_v0.RegisterIdentityServer(c.server, c)
	csi_v0.RegisterNodeServer(c.server, c)
	reflection.Register(c.server)

	// Start listening for requests
	waitForServer := make(chan bool)
	c.goServe(waitForServer)
	<-waitForServer
	c.running = true
	return nil
}

func (c *CSIDriver_v0Support) Stop() {
	c.lock.Lock()
	defer c.lock.Unlock()

	if !c.running {
		return
	}

	c.server.Stop()
	c.wg.Wait()
}

func (c *CSIDriver_v0Support) Close() {
	c.server.Stop()
}

func (c *CSIDriver_v0Support) IsRunning() bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.running
}

func (c *CSIDriver_v0Support) callInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (interface{}, error) {
	rsp, err := handler(ctx, req)
	logGRPC(info.FullMethod, req, rsp, err)
	return rsp, err
}

func (d *CSIDriver_v0Support) CreateVolume(
	ctx context.Context,
	req *csi_v0.CreateVolumeRequest) (
	*csi_v0.CreateVolumeResponse, error) {

	// Change capabilities from v0 -> v1
	caps := []*csi.VolumeCapability{}
	for _, cap := range req.GetVolumeCapabilities() {
		capv1, err := ConvertVolumeCapabilityFromv0Tov1(cap)

		if err != nil {
			return nil, err
		}
		caps = append(caps, capv1)
	}

	// CapacityRange from v0 -> v1
	capacityRange := &csi.CapacityRange{
		RequiredBytes: req.GetCapacityRange().GetRequiredBytes(),
		LimitBytes:    req.GetCapacityRange().GetLimitBytes(),
	}

	//call driver
	res, err := d.driver.CreateVolume(ctx, &csi.CreateVolumeRequest{
		Name:               req.GetName(),
		CapacityRange:      capacityRange,
		VolumeCapabilities: caps,
		Parameters:         req.GetParameters(),
		Secrets:            req.ControllerCreateSecrets,
	})
	if err != nil {
		return nil, err
	}

	return &csi_v0.CreateVolumeResponse{
		Volume: &csi_v0.Volume{
			CapacityBytes: res.Volume.CapacityBytes,
			Id:            res.Volume.GetVolumeId(),
			Attributes:    res.GetVolume().GetVolumeContext(),
		},
	}, err
}

func (d *CSIDriver_v0Support) DeleteVolume(
	ctx context.Context,
	req *csi_v0.DeleteVolumeRequest) (
	*csi_v0.DeleteVolumeResponse, error) {

	_, err := d.driver.DeleteVolume(ctx, &csi.DeleteVolumeRequest{
		VolumeId: req.GetVolumeId(),
		Secrets:  req.GetControllerDeleteSecrets(),
	})
	return &csi_v0.DeleteVolumeResponse{}, err
}

func (d *CSIDriver_v0Support) ControllerPublishVolume(
	ctx context.Context,
	req *csi_v0.ControllerPublishVolumeRequest) (
	*csi_v0.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume not supported")
}

func (d *CSIDriver_v0Support) ControllerUnpublishVolume(
	ctx context.Context,
	req *csi_v0.ControllerUnpublishVolumeRequest) (
	*csi_v0.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume not supported")
}

func (d *CSIDriver_v0Support) ValidateVolumeCapabilities(
	ctx context.Context,
	req *csi_v0.ValidateVolumeCapabilitiesRequest) (
	*csi_v0.ValidateVolumeCapabilitiesResponse, error) {

	// Change capabilities from v0 -> v1
	caps := []*csi.VolumeCapability{}
	for _, cap := range req.GetVolumeCapabilities() {
		capv1, err := ConvertVolumeCapabilityFromv0Tov1(cap)

		if err != nil {
			return &csi_v0.ValidateVolumeCapabilitiesResponse{
				Supported: false,
			}, nil
		}

		caps = append(caps, capv1)
	}
	request := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId:           req.GetVolumeId(),
		VolumeContext:      req.GetVolumeAttributes(),
		VolumeCapabilities: caps,
	}
	_, err := d.driver.ValidateVolumeCapabilities(ctx, request)

	if err != nil {
		return nil, err
	}

	return &csi_v0.ValidateVolumeCapabilitiesResponse{
		Supported: true,
	}, err
}

func (d *CSIDriver_v0Support) GetCapacity(
	ctx context.Context,
	req *csi_v0.GetCapacityRequest) (
	*csi_v0.GetCapacityResponse, error) {

	caps := []*csi.VolumeCapability{}
	for _, cap := range req.GetVolumeCapabilities() {

		// convert accesstype
		accessType := cap.GetMount()

		if accessType == nil {
			return &csi_v0.GetCapacityResponse{
				AvailableCapacity: 0,
			}, nil
		}

		accessMode := csi.VolumeCapability_AccessMode_Mode(cap.GetAccessMode().GetMode())

		caps = append(caps, &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{
					FsType:     accessType.GetFsType(),
					MountFlags: accessType.GetMountFlags(),
				},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: accessMode,
			},
		})
	}

	capacity, err := d.driver.GetCapacity(ctx, &csi.GetCapacityRequest{
		VolumeCapabilities: caps,
		Parameters:         req.GetParameters(),
	})

	return &csi_v0.GetCapacityResponse{
		AvailableCapacity: capacity.GetAvailableCapacity(),
	}, err

}

func (d *CSIDriver_v0Support) ControllerGetCapabilities(
	ctx context.Context,
	req *csi_v0.ControllerGetCapabilitiesRequest) (
	*csi_v0.ControllerGetCapabilitiesResponse, error) {

	caps := []*csi_v0.ControllerServiceCapability{
		{
			Type: &csi_v0.ControllerServiceCapability_Rpc{
				Rpc: &csi_v0.ControllerServiceCapability_RPC{
					Type: csi_v0.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
				},
			},
		},
		{
			Type: &csi_v0.ControllerServiceCapability_Rpc{
				Rpc: &csi_v0.ControllerServiceCapability_RPC{
					Type: csi_v0.ControllerServiceCapability_RPC_GET_CAPACITY,
				},
			},
		},
	}

	return &csi_v0.ControllerGetCapabilitiesResponse{
		Capabilities: caps,
	}, nil
}

func (d *CSIDriver_v0Support) ListVolumes(
	ctx context.Context,
	req *csi_v0.ListVolumesRequest) (
	*csi_v0.ListVolumesResponse, error) {

	return nil, status.Error(codes.Unimplemented, "")
}

func (d *CSIDriver_v0Support) CreateSnapshot(ctx context.Context,
	req *csi_v0.CreateSnapshotRequest) (*csi_v0.CreateSnapshotResponse, error) {

	return nil, status.Error(codes.Unimplemented, "")
}

func (d *CSIDriver_v0Support) DeleteSnapshot(ctx context.Context,
	req *csi_v0.DeleteSnapshotRequest) (*csi_v0.DeleteSnapshotResponse, error) {

	return nil, status.Error(codes.Unimplemented, "")
}

func (d *CSIDriver_v0Support) ListSnapshots(ctx context.Context,
	req *csi_v0.ListSnapshotsRequest) (*csi_v0.ListSnapshotsResponse, error) {

	return nil, status.Error(codes.Unimplemented, "")
}

func (d *CSIDriver_v0Support) GetPluginInfo(
	ctx context.Context,
	req *csi_v0.GetPluginInfoRequest) (
	*csi_v0.GetPluginInfoResponse, error) {
	pluginInfo, err := d.driver.GetPluginInfo(ctx, &csi.GetPluginInfoRequest{})

	return &csi_v0.GetPluginInfoResponse{
		Name:          pluginInfo.Name,
		VendorVersion: pluginInfo.VendorVersion,
		Manifest:      pluginInfo.Manifest,
	}, err
}

func (d *CSIDriver_v0Support) Probe(
	ctx context.Context,
	req *csi_v0.ProbeRequest) (
	*csi_v0.ProbeResponse, error) {

	res, err := d.driver.Probe(ctx, &csi.ProbeRequest{})

	return &csi_v0.ProbeResponse{
		Ready: res.Ready,
	}, err
}

func (d *CSIDriver_v0Support) GetPluginCapabilities(
	ctx context.Context,
	req *csi_v0.GetPluginCapabilitiesRequest) (
	*csi_v0.GetPluginCapabilitiesResponse, error) {

	return &csi_v0.GetPluginCapabilitiesResponse{
		Capabilities: []*csi_v0.PluginCapability{
			{
				Type: &csi_v0.PluginCapability_Service_{
					Service: &csi_v0.PluginCapability_Service{
						Type: csi_v0.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
		},
	}, nil
}

func (d *CSIDriver_v0Support) NodeStageVolume(
	ctx context.Context,
	req *csi_v0.NodeStageVolumeRequest) (
	*csi_v0.NodeStageVolumeResponse, error) {

	capv1, err := ConvertVolumeCapabilityFromv0Tov1(req.GetVolumeCapability())

	if err != nil {
		return nil, err
	}

	_, err = d.driver.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
		StagingTargetPath: req.GetStagingTargetPath(),
		VolumeId:          req.GetVolumeId(),
		VolumeCapability:  capv1,
		PublishContext:    req.GetVolumeAttributes(),
		Secrets:           req.GetNodeStageSecrets(),
	})

	return &csi_v0.NodeStageVolumeResponse{}, err
}

func (d *CSIDriver_v0Support) NodeUnstageVolume(
	ctx context.Context,
	req *csi_v0.NodeUnstageVolumeRequest) (
	*csi_v0.NodeUnstageVolumeResponse, error) {

	_, err := d.driver.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{
		StagingTargetPath: req.GetStagingTargetPath(),
		VolumeId:          req.GetVolumeId(),
	})

	return &csi_v0.NodeUnstageVolumeResponse{}, err
}

func ConvertVolumeCapabilityFromv0Tov1(capability *csi_v0.VolumeCapability) (*csi.VolumeCapability, error) {

	// convert accesstype
	accessType := capability.GetMount()

	if accessType == nil {
		return &csi.VolumeCapability{}, status.Error(codes.InvalidArgument, common.BlockVolumesUnsupported)
	}

	accessMode := csi.VolumeCapability_AccessMode_Mode(capability.AccessMode.GetMode())

	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{
				FsType:     accessType.GetFsType(),
				MountFlags: accessType.GetMountFlags(),
			},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: accessMode,
		},
	}, nil
}

func (d *CSIDriver_v0Support) NodePublishVolume(
	ctx context.Context,
	req *csi_v0.NodePublishVolumeRequest) (
	*csi_v0.NodePublishVolumeResponse, error) {

	capv1, err := ConvertVolumeCapabilityFromv0Tov1(req.GetVolumeCapability())

	if err != nil {
		return nil, err
	}

	request := &csi.NodePublishVolumeRequest{
		TargetPath:        req.TargetPath,
		VolumeId:          req.VolumeId,
		PublishContext:    req.PublishInfo,
		StagingTargetPath: req.StagingTargetPath,
		VolumeCapability:  capv1,
		Readonly:          req.Readonly,
		Secrets:           req.NodePublishSecrets,
		VolumeContext:     req.VolumeAttributes,
	}
	_, err = d.driver.NodePublishVolume(ctx, request)
	if err != nil {
		return nil, err
	}

	return &csi_v0.NodePublishVolumeResponse{}, nil
}

func (d *CSIDriver_v0Support) NodeUnpublishVolume(
	ctx context.Context,
	req *csi_v0.NodeUnpublishVolumeRequest) (
	*csi_v0.NodeUnpublishVolumeResponse, error) {
	request := &csi.NodeUnpublishVolumeRequest{
		TargetPath: req.TargetPath,
		VolumeId:   req.VolumeId,
	}
	_, err := d.driver.NodeUnpublishVolume(ctx, request)
	if err != nil {
		return nil, err
	}
	return &csi_v0.NodeUnpublishVolumeResponse{}, nil
}

func (d *CSIDriver_v0Support) NodeGetCapabilities(
	ctx context.Context,
	req *csi_v0.NodeGetCapabilitiesRequest) (
	*csi_v0.NodeGetCapabilitiesResponse, error) {

	return &csi_v0.NodeGetCapabilitiesResponse{
		Capabilities: []*csi_v0.NodeServiceCapability{
			{
				Type: &csi_v0.NodeServiceCapability_Rpc{
					Rpc: &csi_v0.NodeServiceCapability_RPC{
						Type: csi_v0.NodeServiceCapability_RPC_UNKNOWN,
					},
				},
			},
			{
				Type: &csi_v0.NodeServiceCapability_Rpc{
					Rpc: &csi_v0.NodeServiceCapability_RPC{
						Type: csi_v0.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (d *CSIDriver_v0Support) NodeGetInfo(ctx context.Context,
	req *csi_v0.NodeGetInfoRequest) (*csi_v0.NodeGetInfoResponse, error) {
	csiNodeResponse := &csi_v0.NodeGetInfoResponse{
		NodeId: d.driver.NodeID,
	}
	return csiNodeResponse, nil
}
func (d *CSIDriver_v0Support) NodeGetId(ctx context.Context,
	req *csi_v0.NodeGetIdRequest) (*csi_v0.NodeGetIdResponse, error) {
	csiNodeResponse := &csi_v0.NodeGetIdResponse{
		NodeId: d.driver.NodeID,
	}
	return csiNodeResponse, nil
}
