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
    "golang.org/x/net/context"
    "k8s.io/kubernetes/pkg/util/mount"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"

    "github.com/container-storage-interface/spec/lib/go/csi"
    "github.com/hammer-space/csi-plugin/pkg/common"
    log "github.com/sirupsen/logrus"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

func (d *CSIDriver) NodeStageVolume(
    ctx context.Context,
    req *csi.NodeStageVolumeRequest) (
    *csi.NodeStageVolumeResponse, error) {

    if req.GetVolumeId() == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
    }

    if req.GetStagingTargetPath() == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyStagingTargetPath)
    }

    if req.GetVolumeCapability() == nil {
        return nil, status.Error(codes.InvalidArgument, common.NoCapabilitiesSupplied)
    }

    return &csi.NodeStageVolumeResponse{}, nil
}

func (d *CSIDriver) NodeUnstageVolume(
    ctx context.Context,
    req *csi.NodeUnstageVolumeRequest) (
    *csi.NodeUnstageVolumeResponse, error) {

    if req.GetVolumeId() == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
    }

    if req.GetStagingTargetPath() == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyStagingTargetPath)
    }

    return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *CSIDriver) publishShareBackedVolume(
    exportPath,
    targetPath string, mountFlags []string, readOnly bool) error{

    notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
    if err != nil {
        if os.IsNotExist(err) {
            if err := os.MkdirAll(targetPath, 0750); err != nil {
                return status.Error(codes.Internal, err.Error())
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

    if readOnly {
        mountFlags = append(mountFlags, "ro")
    }
    err = d.MountShareAtBestDataportal(exportPath, targetPath, mountFlags)
    return err
}

func (d *CSIDriver) publishFileBackedVolume(
    backingShareName, volumePath, targetPath, fsType string, mountFlags []string, readOnly bool) (error) {
    defer d.releaseVolumeLock(backingShareName)
    d.getVolumeLock(backingShareName)

    notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
    if err != nil {
        if os.IsNotExist(err) {
            if fsType != "" {
                if err := mount.New("").MakeDir(targetPath); err != nil {
                    return status.Error(codes.Internal, err.Error())
                }
            }else {
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

    // Ensure the backing share is mounted
    err = d.EnsureBackingShareMounted(backingShareName)
    if err != nil {
        return err
    }

    // Mount the file
    log.Infof("Mounting file-backed volume at %s", targetPath)
    filePath := common.BackingShareProvisioningDir + volumePath

    // If no fsType specified, mount as a device
    if fsType == "" {
        deviceNumber, err := common.EnsureFreeLoopbackDeviceFile()
        if err != nil {
            log.Error(err.Error())
            return err
        }
        deviceStr := fmt.Sprintf("/dev/loop%d", deviceNumber)

        losetupFlags := []string{}
        // is read only?
        if readOnly {
            losetupFlags = append(losetupFlags, "-r")
        }
        losetupFlags = append(losetupFlags, deviceStr)
        losetupFlags = append(losetupFlags, filePath)
        output, err := exec.Command("losetup", losetupFlags...).CombinedOutput()
        if err != nil {
            log.Errorf("issue setting up loop device: device=%s, filePath=%s, %s, %v",
                deviceStr, filePath, output, err.Error())
            exec.Command("losetup", "-d", deviceStr)
            d.UnmountBackingShareIfUnused(backingShareName)
            return status.Errorf(codes.Internal, common.LoopDeviceAttachFailed, deviceStr, filePath)
        }
        log.Infof("File %s attached to %s", filePath, deviceStr)

        // bind mount to target path
        err = common.BindMountDevice(deviceStr, targetPath)
        if err != nil {
            // clean up losetup
            // FIXME, sometimes this command succeeds and doesnt do the detach, make a retry here
            exec.Command("losetup", "-d", deviceStr)
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

    if fsType == "nfs" {
        err := d.publishShareBackedVolume(req.GetVolumeId(), req.GetTargetPath(), mountFlags, req.GetReadonly())
        return &csi.NodePublishVolumeResponse{}, err
    } else {
        var backingShareName string
        if volumeMode == "Block"{
            backingShareName = req.GetVolumeContext()["blockBackingShareName"]
        } else {
            backingShareName = req.GetVolumeContext()["mountBackingShareName"]
        }
        log.Infof("Found backing share %s for volume %s", backingShareName, req.GetVolumeId())

        err := d.publishFileBackedVolume(
            backingShareName, req.GetVolumeId(), req.GetTargetPath(), fsType, mountFlags, req.GetReadonly())
        return &csi.NodePublishVolumeResponse{}, err

    }

    return &csi.NodePublishVolumeResponse{}, nil
}

func (d *CSIDriver) unpublishFileBackedVolume(
    volumePath, targetPath string) (error) {

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
    command := exec.Command("umount", "-f", targetPath)
    output, err := command.CombinedOutput()
    if err != nil {
        log.Errorf("could not remove bind mount, %s", err)
        return status.Error(codes.Internal, err.Error())
    }

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
    fi, err := os.Stat(targetPath)
    if err != nil {
        log.Infof("target path does not exist on this host, %s", targetPath)
        return &csi.NodeUnpublishVolumeResponse{}, nil
    }

    switch mode := fi.Mode(); {
    case mode&os.ModeDevice != 0: // if target path is a device, it's block
        err := d.unpublishFileBackedVolume(req.GetVolumeId(), targetPath)
        if err != nil {
            return nil, err
        }
    case mode.IsDir(): // if target path is a directory, it's filesystem
        err := common.UnmountFilesystem(targetPath)
        if err != nil {
            return nil, status.Error(codes.Internal, err.Error())
        }
    default:
        return nil, status.Error(codes.InvalidArgument, common.TargetPathUnknownFiletype)
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

    // Check if volume is published at path
    _, err := os.Stat(req.GetVolumePath())
    if err != nil {
        return nil, status.Error(codes.NotFound, common.VolumeNotFound)
    }

    // Check if volume is on a backing share
    isFileBacked := false
    _, err = os.Stat(common.BackingShareProvisioningDir + req.GetVolumeId())
    if err == nil {
        isFileBacked = true
    }

    if isFileBacked {
        // we must stat the actual file, not the dev files, to get the size
        fileInfo, err := os.Stat(common.BackingShareProvisioningDir + req.GetVolumeId())
        if err != nil {
            return nil, status.Error(codes.NotFound, common.VolumeNotFound)
        }
        return &csi.NodeGetVolumeStatsResponse{
            Usage: []*csi.VolumeUsage{
                &csi.VolumeUsage{
                    Unit:  csi.VolumeUsage_BYTES,
                    Total: fileInfo.Size(),
                },
            },
        }, nil
    } else {
        volumeName := GetVolumeNameFromPath(req.GetVolumeId())
        share, err := d.hsclient.GetShare(volumeName)
        if err != nil {
            return nil, status.Error(codes.NotFound, common.ShareNotFound)
        }

        available, _ := strconv.ParseInt(share.Space.Available, 10, 64)
        used, _ := strconv.ParseInt(share.Space.Used, 10, 64)
        total, _ := strconv.ParseInt(share.Space.Total, 10, 64)

        return &csi.NodeGetVolumeStatsResponse{
            Usage: []*csi.VolumeUsage{
                &csi.VolumeUsage{
                    Unit:      csi.VolumeUsage_BYTES,
                    Available: available,
                    Total:     total,
                    Used:      used,
                },
            },
        }, nil
    }

    return nil, status.Error(codes.NotFound, common.VolumeNotFound)
}
func (d *CSIDriver) NodeExpandVolume(
    ctx context.Context,
    req *csi.NodeExpandVolumeRequest) (
    *csi.NodeExpandVolumeResponse, error) {
    return nil, status.Error(codes.Unimplemented, "NodeExpandVolume not supported")
}