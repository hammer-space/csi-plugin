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
	"fmt"
	"os"
	"os/exec"
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
	"k8s.io/kubernetes/pkg/util/mount"
)

func (d *CSIDriver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTarget := req.GetStagingTargetPath()

	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}
	if stagingTarget == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing")
	}

	// Create staging directory if it doesn't exist
	if err := os.MkdirAll(stagingTarget, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
	}

	// Check if already mounted
	notMounted, err := common.SafeIsLikelyNotMountPoint(stagingTarget)
	if err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "Could not check mount point: %v", err)
	}
	if !notMounted {
		// Already mounted
		return &csi.NodeStageVolumeResponse{}, nil
	}

	mountOptions := []string{""} // Add any required NFS options here
	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	log.Infof("Mounting volume %s at staging target %s with fsType %s", volumeID, stagingTarget, fsType)
	if fsType == "nfs" || fsType == "" {
		// Mount the NFS share
		err = d.MountShareAtBestDataportal(volumeID, stagingTarget, mountOptions, req.GetVolumeContext()["fqdn"])
		if err != nil {
			log.Errorf("Failed to mount NFS share %s at %s: %v", volumeID, stagingTarget, err)
		}
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
			log.Infof("Staging target path %s does not exist, nothing to unmount", stagingTarget)
			return &csi.NodeUnstageVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "Failed to unmount staging target path %s: %v", stagingTarget, err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *CSIDriver) publishShareBackedVolume(exportPath, targetPath, stagingTargetPath string, mountFlags []string, readOnly bool, fqdn string) error {
	// Check if target path exists and is not mounted
	log.Infof("Publishing share-backed volume %s to target path %s", exportPath, targetPath)
	notMnt, err := common.SafeIsLikelyNotMountPoint(targetPath)
	if err != nil {
		log.Warnf("Error checking mount status of %s: %v", targetPath, err)
		// Create target path directory if it doesn't exist
		if os.IsNotExist(err) {
			if err := os.MkdirAll(targetPath, 0750); err != nil {
				log.Errorf("Failed to create target path %s: %v", targetPath, err)
				return status.Error(codes.Internal, err.Error())
			}
			notMnt = true
		} else {
			log.Errorf("Failed to check mount status of %s: %v", targetPath, err)
			return status.Error(codes.Internal, err.Error())
		}
	}

	if !notMnt {
		log.Debugf("Volume already published at %s", targetPath)
		return nil
	}

	if readOnly {
		mountFlags = append(mountFlags, "ro")
	}
	// Bind mount from staging to target
	if stagingTargetPath != "" && stagingTargetPath != targetPath {
		log.Infof("Attempting bind mount from %s to %s", stagingTargetPath, targetPath)
		err = common.BindMountDevice(stagingTargetPath, targetPath)
		if err != nil {
			log.Warnf("Initial bind mount from %s to %s failed: %v", stagingTargetPath, targetPath, err)
		} else {
			notMount, statErr := common.SafeIsLikelyNotMountPoint(targetPath)
			if statErr != nil {
				log.Warnf("Could not determine mount status of %s: %v", targetPath, statErr)
			} else if notMount {
				log.Warnf("Bind mount from %s to %s appears to have failed (target is not a mount point)", stagingTargetPath, targetPath)
			} else {
				log.Infof("Bind mount succeeded from %s to %s", stagingTargetPath, targetPath)
				return nil
			}
		}
	}

	log.Infof("Bind mount didnt work for %s -> %s, trying to mount share directly", exportPath, targetPath)
	err = d.MountShareAtBestDataportal(exportPath, targetPath, mountFlags, fqdn)
	return err
}

func (d *CSIDriver) publishFileBackedVolume(
	backingShareName, volumePath, targetPath, fsType string, mountFlags []string, readOnly bool, fqdn string) error {
	defer d.releaseVolumeLock(backingShareName)
	d.getVolumeLock(backingShareName)

	notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if fsType != "" {
				if err := mount.New("").MakeDir(targetPath); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
			} else {
				if err := mount.New("").MakeFile(targetPath); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
			}
			notMnt = true
		} else {
			return status.Error(codes.Internal, err.Error())
		}
	}
	if !notMnt {
		log.Debugf("Volume already published at %s", targetPath)
		return nil
	}

	hsVolume := &common.HSVolume{
		FQDN:               fqdn,
		FSType:             fsType,
		ClientMountOptions: mountFlags,
	}
	log.Infof("check publish file backed volume %v", hsVolume)

	// Ensure the backing share is mounted
	err = d.EnsureBackingShareMounted(backingShareName, hsVolume)
	if err != nil {
		return err
	}

	// Mount the file
	log.Infof("Mounting file-backed volume at %s", targetPath)
	filePath := common.ShareStagingDir + volumePath

	// If no fsType specified, mount as a device
	if fsType == "" {
		filePath := common.ShareStagingDir + volumePath

		deviceStr, err := AttachLoopDeviceWithRetry(filePath, readOnly)
		if err != nil {
			log.Errorf("failed to attach loop device: %v", err)
			CleanupLoopDevice(deviceStr)
			d.UnmountBackingShareIfUnused(backingShareName)
			return status.Errorf(codes.Internal, common.LoopDeviceAttachFailed, deviceStr, filePath)
		}
		log.Infof("File %s attached to %s", filePath, deviceStr)

		// Bind mount loop device to target path
		err = common.BindMountDevice(deviceStr, targetPath)
		if err != nil {
			log.Errorf("bind mount failed for %s: %v", deviceStr, err)
			CleanupLoopDevice(deviceStr)
			d.UnmountBackingShareIfUnused(backingShareName)
			return err
		}
	} else {
		if readOnly {
			mountFlags = append(mountFlags, "ro")
		}
		err = common.MountFilesystem(filePath, targetPath, fsType, mountFlags)
		if err != nil {
			d.UnmountBackingShareIfUnused(backingShareName)
			return err
		}
	}
	return nil
}

func (d *CSIDriver) NodePublishVolume(
	ctx context.Context,
	req *csi.NodePublishVolumeRequest) (
	*csi.NodePublishVolumeResponse, error) {

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}

	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyTargetPath)
	}

	if req.GetVolumeCapability() == nil {
		return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, req.GetVolumeId())
	}

	defer d.releaseVolumeLock(req.GetVolumeId())
	d.getVolumeLock(req.GetVolumeId())

	log.Infof("Attempting to publish volume %s", req.GetVolumeId())

	var volumeMode, fsType string
	var mountFlags []string
	fqdn := req.GetVolumeContext()["fqdn"]
	cap := req.GetVolumeCapability()
	switch cap.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		volumeMode = "Block"
	case *csi.VolumeCapability_Mount:
		volumeMode = "Filesystem"
		fsType = cap.GetMount().FsType
		if fsType == "" {
			fsType = req.GetVolumeContext()["fsType"]
			if fsType == "" {
				fsType = "nfs"
			}
		}
		mountFlags = cap.GetMount().MountFlags
	default:
		return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, req.GetVolumeId())
	}
	var err error
	if fsType == "nfs" {
		err = d.publishShareBackedVolume(req.GetVolumeId(), req.GetTargetPath(), req.StagingTargetPath, mountFlags, req.GetReadonly(), fqdn)
	} else {
		var backingShareName string
		if volumeMode == "Block" {
			backingShareName = req.GetVolumeContext()["blockBackingShareName"]
		} else {
			backingShareName = req.GetVolumeContext()["mountBackingShareName"]
		}
		log.Infof("Found backing share %s for volume %s", backingShareName, req.GetVolumeId())

		err = d.publishFileBackedVolume(
			backingShareName, req.GetVolumeId(), req.GetTargetPath(), fsType, mountFlags, req.GetReadonly(), fqdn)

	}

	return &csi.NodePublishVolumeResponse{}, err
}

func (d *CSIDriver) unpublishFileBackedVolume(
	volumePath, targetPath string) error {

	//determine backing share
	backingShareName := filepath.Dir(volumePath)

	defer d.releaseVolumeLock(backingShareName)
	d.getVolumeLock(backingShareName)

	deviceMinor, err := common.GetDeviceMinorNumber(targetPath)
	if err != nil {
		log.Errorf("could not determine corresponding device path for target path, %s, %v", targetPath, err)
		return status.Error(codes.Internal, err.Error())
	}
	lodevice := fmt.Sprintf("/dev/loop%d", deviceMinor)
	log.Infof("found device %s for mount %s", lodevice, targetPath)

	// Remove bind mount
	output, err := common.ExecCommand("umount", "-f", targetPath)
	if err != nil {
		log.Errorf("could not remove bind mount, %s", err)
		return status.Error(codes.Internal, err.Error())
	}
	log.Infof("unmounted the targetPath %s. Command output %v ", targetPath, output)
	// delete target path
	err = os.Remove(targetPath)
	if err != nil {
		log.Errorf("could not remove target path, %v", err)
		return status.Error(codes.Internal, err.Error())
	}

	// detach from loopback device
	log.Infof("detaching loop device, %s", lodevice)
	output, err = exec.Command("losetup", "-d", lodevice).CombinedOutput()
	if err != nil {
		log.Errorf("%s, %v", output, err.Error())
		return status.Error(codes.Internal, err.Error())
	}

	// Unmount backing share if appropriate
	unmounted, err := d.UnmountBackingShareIfUnused(backingShareName)
	if unmounted {
		log.Infof("unmounted backing share, %s", backingShareName)
	}
	if err != nil {
		log.Errorf("unmounted backing share, %s, failed: %v", backingShareName, err)
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}
func (d *CSIDriver) NodeUnpublishVolume(
	ctx context.Context,
	req *csi.NodeUnpublishVolumeRequest) (
	*csi.NodeUnpublishVolumeResponse, error) {

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
		if err := d.unpublishFileBackedVolume(req.GetVolumeId(), targetPath); err != nil {
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

func (d *CSIDriver) NodeGetCapabilities(
	ctx context.Context,
	req *csi.NodeGetCapabilitiesRequest) (
	*csi.NodeGetCapabilitiesResponse, error) {

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

func (d *CSIDriver) NodeGetInfo(ctx context.Context,
	req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {

	// Determine if this node is a data portal
	dataPortals, err := d.hsclient.GetDataPortals(d.NodeID)
	if err != nil {
		log.Errorf("Could not list data-portals, %s", err.Error())
	}
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
	return csiNodeResponse, nil
}

func (d *CSIDriver) NodeGetVolumeStats(ctx context.Context,
	req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {

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

func (d *CSIDriver) NodeExpandVolume(
	ctx context.Context,
	req *csi.NodeExpandVolumeRequest) (
	*csi.NodeExpandVolumeResponse, error) {

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
	share, _ := d.hsclient.GetShare(volumeName)
	if share != nil {
		typeMount = true
		if isMounted, _ := common.IsShareMounted(share.ExportPath); !isMounted {
			return nil, status.Error(codes.FailedPrecondition, common.ShareNotMounted)
		}
	} else {
		fileBacked = true
	}

	//  Check if the specified backing share or file exists
	if share == nil {
		backingFileExists, err := d.hsclient.DoesFileExist(req.GetVolumeId())
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
