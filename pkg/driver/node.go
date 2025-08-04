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
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/common"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *CSIDriver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {

	// Determine if this node is a data portal
	dataPortals, err := d.hsclient.GetDataPortals(ctx, d.NodeID)
	if err != nil {
		log.WithFields(log.Fields{
			"Node ID": d.NodeID,
		}).Errorf("Could not list data-portals, %s", err.Error())
		return nil, err
	}

	log.WithFields(log.Fields{
		"dataPortals": dataPortals,
	}).Debugf("Recived data portal list")
	var isDataPortal bool
	for _, p := range dataPortals {
		if p.Node.Name == d.NodeID {
			isDataPortal = true
		}
	}

	csiNodeResponse := &csi.NodeGetInfoResponse{
		NodeId: d.NodeID,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				common.TopologyKeyDataPortal: strconv.FormatBool(isDataPortal),
			},
		},
	}
	log.WithFields(log.Fields{
		"csiNodeResponse": csiNodeResponse,
	}).Debugf("NodeGetInfo was successful.")

	return csiNodeResponse, nil
}

func (d *CSIDriver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}

	if req.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumePath)
	}

	// Check if path exists
	info, err := os.Stat(req.GetVolumePath())
	if err != nil {
		log.Errorf("volume path not found: %s, err: %v", req.GetVolumePath(), err)
		return nil, status.Error(codes.NotFound, common.VolumeNotFound)
	}

	// If it's a block device, use Stat_t
	if IsBlockDevice(info) {
		log.Infof("Detected block volume: %s", req.GetVolumePath())

		file, err := os.Open(req.GetVolumePath())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to open block device: %v", err)
		}
		defer file.Close()

		// Get size using ioctl
		var size int64
		// BLKGETSIZE64: ioctl to get size in bytes for block devices
		const BLKGETSIZE64 = 0x80081272
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), BLKGETSIZE64, uintptr(unsafe.Pointer(&size)))
		if errno != 0 {
			return nil, status.Errorf(codes.Internal, "failed to get block size: %v", errno)
		}

		// CSI spec: for block volumes, 'used' and 'available' may be omitted.
		// Reporting Available = 0 can be misinterpreted as disk full.
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{
				{
					Unit:  csi.VolumeUsage_BYTES,
					Total: size,
					// Used and Available intentionally omitted per CSI spec
				},
				{
					Unit: csi.VolumeUsage_INODES,
					// All inode fields omitted (optional)
				},
			},
		}, nil
	}

	// Default: File or NFS mount — use Statfs
	var st syscall.Statfs_t
	err = syscall.Statfs(req.GetVolumePath(), &st)
	if err != nil {
		log.Errorf("statfs failed on %s: %v", req.GetVolumePath(), err)
		return nil, status.Error(codes.Internal, common.FileNotFound)
	}

	total := int64(st.Bsize) * int64(st.Blocks)
	available := int64(st.Bsize) * int64(st.Bavail)
	used := int64(st.Bsize) * int64(st.Blocks-st.Bfree)

	inodestotal := int64(st.Files)
	inodesavail := int64(st.Ffree)
	inodesused := inodestotal - inodesavail

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: available,
				Total:     total,
				Used:      used,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: inodesavail,
				Total:     inodestotal,
				Used:      inodesused,
			},
		},
	}, nil
}

func (d *CSIDriver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTarget := req.GetStagingTargetPath()

	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}
	if stagingTarget == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing")
	}
	log.WithFields(log.Fields{
		"volume_id":      volumeID,
		"staging_target": stagingTarget,
	}).Debug("NodeStageVolume creating staging directory")

	// Create staging directory if it doesn't exist
	if err := os.MkdirAll(stagingTarget, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
	}
	log.Debugf("Checking is staging target is already mounted.")
	// Check if already mounted
	mounted, err := common.SafeIsMountPoint(stagingTarget)
	if err != nil && !os.IsNotExist(err) {
		log.Errorf("error while checking staging target mount")
		return nil, status.Errorf(codes.Internal, "Could not check mount point: %v", err)
	}

	if mounted {
		log.Debugf("Node staging path already mounted.")
		// Already mounted
		return &csi.NodeStageVolumeResponse{}, nil
	}

	log.Infof("Mounting volume  %s at staging target %s", volumeID, stagingTarget)

	// 1. Ensure the root NFS export is mounted once per node
	if err := d.EnsureRootExportMounted(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "root export mount failed: %v", err)
	}

	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *CSIDriver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTarget := req.GetStagingTargetPath()

	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}
	if stagingTarget == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing")
	}

	// Unmount if mounted
	err := common.UnmountFilesystem(stagingTarget)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warnf("Staging target path %s does not exist, nothing to unmount", stagingTarget)
		}
		log.Warnf("Failed to unmount staging target path %s: %v", stagingTarget, err)
	}

	csiNodeStagingVolumePath := filepath.Join(common.BaseBackingShareMountPath, volumeID)
	err = common.UnmountFilesystem(csiNodeStagingVolumePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warnf("csiNodeStagingVolumePath %s does not exist, nothing to unmount", csiNodeStagingVolumePath)
		}
		log.Warnf("Failed to unmount csiNodeStagingVolumePath %s: %v", csiNodeStagingVolumePath, err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *CSIDriver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {

	volume_id := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	volumeCapability := req.GetVolumeCapability()

	if volume_id == "" || targetPath == "" || volumeCapability == nil {
		log.WithFields(log.Fields{
			"volume_id":        volume_id,
			"targetPath":       targetPath,
			"volumeCapability": volumeCapability,
		}).Errorf("Invalid arguments")
		if volume_id == "" {
			return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
		}
		if targetPath == "" {
			return nil, status.Error(codes.InvalidArgument, common.EmptyTargetPath)
		}
		if volumeCapability == nil {
			return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, volume_id)
		}
	}

	defer d.releaseVolumeLock(volume_id)
	d.getVolumeLock(volume_id)

	log.Infof("Attempting to publish volume %s", volume_id)

	var volumeContext = req.GetVolumeContext()
	var readOnly bool = req.GetReadonly()
	var mountFlags []string
	var fsType, backingShareName string

	switch volumeCapability.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		backingShareName = volumeContext["blockBackingShareName"]
	case *csi.VolumeCapability_Mount:
		backingShareName = volumeContext["mountBackingShareName"]
		fsType = volumeCapability.GetMount().FsType
		if fsType == "" {
			fsType = volumeContext["fsType"]
			if fsType == "" {
				fsType = "nfs"
			}
		}
		mountFlags = volumeCapability.GetMount().MountFlags
	default:
		return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, volume_id)
	}

	// For NFS
	if fsType == "nfs" && backingShareName == "" {
		err := d.publishShareBackedVolume(ctx, volume_id, targetPath, mountFlags, readOnly, volumeContext["fqdn"])
		if err != nil {
			return nil, err
		}
	} else if fsType == "nfs" && backingShareName != "" {
		err := d.publishShareBackedDirBasedVolume(ctx, backingShareName, volume_id, targetPath, req.StagingTargetPath, fsType, mountFlags, readOnly, volumeContext["fqdn"])
		if err != nil {
			return nil, err
		}
	} else {
		err := d.publishFileBackedVolume(ctx, backingShareName, volume_id, targetPath, fsType, mountFlags, readOnly, volumeContext["fqdn"])
		if err != nil {
			return nil, err
		}
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *CSIDriver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, common.EmptyTargetPath)
	}

	log.Infof("Attempting to unpublish volume %s", req.GetVolumeId())
	defer d.releaseVolumeLock(req.GetVolumeId())
	d.getVolumeLock(req.GetVolumeId())

	targetPath := req.GetTargetPath()
	fi, err := os.Lstat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Infof("target path does not exist on this host: %s", targetPath)
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "error stating target path: %v", err)
	}

	// Resolve symlink before checking type
	if fi.Mode()&os.ModeSymlink != 0 {
		resolvedPath, err := filepath.EvalSymlinks(targetPath)
		if err != nil {
			log.Warnf("Broken symlink at %s: %v", targetPath, err)
			// remove the symlink path
			if rmErr := os.Remove(targetPath); rmErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to remove broken symlink: %v", rmErr)
			}
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}

		log.Infof("Resolved symlink targetPath=%s → %s", req.GetTargetPath(), resolvedPath)
		targetPath = resolvedPath
		fi, err = os.Stat(targetPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to stat resolved path: %v", err)
		}
	}

	switch mode := fi.Mode(); {
	case IsBlockDevice(fi): // block device
		log.Infof("Detected block device at target path %s", targetPath)
		if err := d.unpublishFileBackedVolume(ctx, req.GetVolumeId(), targetPath); err != nil {
			return nil, err
		}
	case mode.IsDir(): // directory for mount volumes
		log.Infof("Detected directory mount at target path %s", targetPath)
		if err := common.UnmountFilesystem(targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	default:
		// Unknown file type, attempt cleanup
		log.Warnf("Target path %s exists but is not a block device nor directory. Removing...", targetPath)
		if err := os.Remove(targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to remove unexpected target path: %v", err)
		}
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *CSIDriver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_UNKNOWN,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (d *CSIDriver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {

	var requestedSize int64
	if req.GetCapacityRange().GetLimitBytes() != 0 {
		requestedSize = req.GetCapacityRange().GetLimitBytes()
	} else {
		requestedSize = req.GetCapacityRange().GetRequiredBytes()
	}

	// Find Share
	typeMount := false
	fileBacked := false

	volumeName := GetVolumeNameFromPath(req.GetVolumeId())
	share, _ := d.hsclient.GetShare(ctx, volumeName)
	if share != nil {
		typeMount = true
		if isMounted := common.IsShareMounted(share.ExportPath); !isMounted {
			return nil, status.Error(codes.FailedPrecondition, common.ShareNotMounted)
		}
	} else {
		fileBacked = true
	}

	//  Check if the specified backing share or file exists
	if share == nil {
		backingFileExists, err := d.hsclient.DoesFileExist(ctx, req.GetVolumeId())
		if err != nil {
			log.Error(err)
		}
		if !backingFileExists {
			return nil, status.Error(codes.InvalidArgument, common.VolumeNotFound)
		} else {
			fileBacked = true
		}
	}
	switch req.GetVolumeCapability().GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		typeMount = false
	case *csi.VolumeCapability_Mount:
		typeMount = true
	}

	if fileBacked {
		// Ensure it's file-backed, otherwise no-op
		// Resize device
		err := common.ExpandDeviceFileSize(common.ShareStagingDir+req.GetVolumeId(), requestedSize)
		if err != nil {
			return nil, err
		}
		if typeMount {
			err = common.ExpandFilesystem(common.ShareStagingDir+req.GetVolumeId(), req.VolumeCapability.GetMount().FsType)
			if err != nil {
				return nil, err
			}
		}
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: requestedSize,
		}, nil
	} else {
		return nil, nil
	}
}
