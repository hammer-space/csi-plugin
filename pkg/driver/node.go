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
    cap := req.GetVolumeCapability()
    switch cap.GetAccessType().(type) {
    case *csi.VolumeCapability_Block:
        volumeMode = "Block"
    case *csi.VolumeCapability_Mount:
        volumeMode = "Filesystem"
        fsType = cap.GetMount().FsType
        if fsType == "" {
            fsType = "nfs"
        }
    default:
        return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, req.GetVolumeId())
    }
    if volumeMode == "Filesystem" && fsType == "nfs"{
        path := req.GetVolumeId()
        targetPath := req.GetTargetPath()
        notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
        if err != nil {
            if os.IsNotExist(err) {
                if err := os.MkdirAll(targetPath, 0750); err != nil {
                    return nil, status.Error(codes.Internal, err.Error())
                }
                notMnt = true
            } else {
                return nil, status.Error(codes.Internal, err.Error())
            }
        }

        if !notMnt {
            log.Debugf("Volume already published at %s", targetPath)
            return &csi.NodePublishVolumeResponse{}, nil
        }

        mo := req.GetVolumeCapability().GetMount().GetMountFlags()
        if req.GetReadonly() {
            mo = append(mo, "ro")
        }
        err = d.MountShareAtBestDataportal(path, targetPath, mo)
        if err != nil {
            return nil, err
        }
    } else if volumeMode == "Filesystem"{
        //TODO: file backed mount volumes

    } else if volumeMode == "Block" {

        // Lock on backing share
        backingShareName := req.GetVolumeContext()["blockBackingShareName"]
        defer d.releaseVolumeLock(backingShareName)
        d.getVolumeLock(backingShareName)
        targetPath := req.GetTargetPath()
        notMnt, err := mount.New("").IsLikelyNotMountPoint(targetPath)
        if err != nil {
            if os.IsNotExist(err) {
                if err := mount.New("").MakeFile(targetPath); err != nil {
                    return nil, status.Error(codes.Internal, err.Error())
                }
                notMnt = true
            } else {
                return nil, status.Error(codes.Internal, err.Error())
            }
        }
        if !notMnt {
            log.Debugf("Volume already published at %s", targetPath)
            return &csi.NodePublishVolumeResponse{}, nil
        }

        // Ensure the backing share is mounted
        err = d.EnsureBackingShareMounted(backingShareName)
        if err != nil {
            return nil, err
        }

        // Mount the file
        log.Infof("Mounting block volume at %s", targetPath)
        filePath := common.BackingShareProvisioningDir + req.GetVolumeId()

        deviceNumber, err := common.EnsureFreeLoopbackDeviceFile()
        if err != nil {
            log.Error(err.Error())
            return nil, err
        }
        deviceStr := fmt.Sprintf("/dev/loop%d", deviceNumber)

        losetupFlags := []string{}
        // is read only?
        if req.GetReadonly() {
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
            return nil, status.Errorf(codes.Internal, common.LoopDeviceAttachFailed, deviceStr, filePath)
        }
        log.Infof("File %s attached to %s", filePath, deviceStr)

        // bind mount to target path
        err = common.BindMountDevice(deviceStr, targetPath)
        if err != nil {
            // clean up losetup
            // FIXME, sometimes this command succeeds and doesnt do the detach, make a retry here
            exec.Command("losetup", "-d", deviceStr)
            d.UnmountBackingShareIfUnused(backingShareName)
            return nil, err
        }
    }

    return &csi.NodePublishVolumeResponse{}, nil
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
    case mode&os.ModeDevice != 0: // if target path is a device, it's block TODO: or a file-backed mount volume
        // find loopback device location from target path
        deviceMinor, err := common.GetDeviceMinorNumber(targetPath)
        if err != nil {
            log.Errorf("could not determine corresponding device path for target path, %s, %v", targetPath, err)
            return nil, status.Error(codes.Internal, err.Error())
        }
        lodevice := fmt.Sprintf("/dev/loop%d", deviceMinor)
        log.Infof("found device %s for mount %s", lodevice, targetPath)

        // Remove bind mount
        command := exec.Command("umount", "-f", targetPath)
        output, err := command.CombinedOutput()
        if err != nil {
            log.Errorf("could not remove bind mount, %s", err)
            return nil, status.Error(codes.Internal, err.Error())
        }

        // delete target path
        err = os.Remove(targetPath)
        if err != nil {
            log.Errorf("could not remove target path, %v", err)
            return nil, status.Error(codes.Internal, err.Error())
        }

        //determine backing share
        backingShareName := filepath.Dir(req.GetVolumeId())

        defer d.releaseVolumeLock(backingShareName)
        d.getVolumeLock(backingShareName)

        // detach from loopback device
        log.Infof("detaching loop device, %s", lodevice)
        output, err = exec.Command("losetup", "-d", lodevice).CombinedOutput()
        if err != nil {
            log.Errorf("%s, %v", output, err.Error())
            return nil, status.Error(codes.Internal, err.Error())
        }

        // Unmount backing share if appropriate
        unmounted, err := d.UnmountBackingShareIfUnused(backingShareName)
        if unmounted {
            log.Infof("unmounted backing share, %s", backingShareName)
        }
        if err != nil {
            log.Errorf("unmounted backing share, %s, failed: %v", backingShareName, err)
            return nil, status.Error(codes.Internal, err.Error())
        }

    case mode.IsDir(): // if target path is a directory, it's filesystem
        err := common.UnmountShare(targetPath)
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
    csiNodeResponse := &csi.NodeGetInfoResponse{
        NodeId: d.NodeID,
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

    fi, err := os.Stat(req.GetVolumePath())
    if err != nil {
        return nil, status.Error(codes.NotFound, common.VolumeNotFound)
    }

    switch mode := fi.Mode(); {
    case mode&os.ModeDevice != 0:
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
    case mode.IsDir():
        volumeName := d.GetVolumeNameFromPath(req.GetVolumeId())
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
