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
    "github.com/golang/protobuf/ptypes/timestamp"
    "github.com/jpillora/backoff"
    "strconv"
    "strings"
    "time"

    "github.com/container-storage-interface/spec/lib/go/csi"
    log "github.com/sirupsen/logrus"
    "golang.org/x/net/context"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"

    "github.com/hammer-space/csi-plugin/pkg/common"
)

const (
    MaxNameLength           int = 128
    DefaultVolumeNameFormat     = "%s"
)

var (
    recentlyCreatedSnapshots = map[string]*csi.Snapshot{}
)

type HSVolumeParameters struct {
    DeleteDelay           int64
    ExportOptions         []common.ShareExportOptions
    Objectives            []string
    BlockBackingShareName string
    MountBackingShareName string
    VolumeNameFormat      string
    FSType                string
}

type HSVolume struct {
    DeleteDelay           int64
    ExportOptions         []common.ShareExportOptions
    Objectives            []string
    BlockBackingShareName string
    MountBackingShareName string
    Size                  int64
    Name                  string
    Path                  string
    VolumeMode            string
    SourceSnapPath        string
    FSType                string
}

func parseVolParams(params map[string]string) (HSVolumeParameters, error) {
    vParams := HSVolumeParameters{}

    if deleteDelayParam, exists := params["deleteDelay"]; exists {
        var err error
        vParams.DeleteDelay, err = strconv.ParseInt(deleteDelayParam, 10, 64)
        if err != nil {
            return vParams, status.Errorf(codes.InvalidArgument, common.InvalidDeleteDelay, deleteDelayParam)
        }

    } else {
        vParams.DeleteDelay = -1
    }

    if objectivesParam, exists := params["objectives"]; exists {
        if exists {
            splitObjectives := strings.Split(objectivesParam, ",")
            vParams.Objectives = make([]string, 0, len(splitObjectives))
            for _, o := range splitObjectives {
                trimmedObj := strings.TrimSpace(o)
                if trimmedObj != "" {
                    vParams.Objectives = append(vParams.Objectives, trimmedObj)
                }
            }
        }
    }

    vParams.BlockBackingShareName = params["blockBackingShareName"]
    vParams.MountBackingShareName = params["mountBackingShareName"]
    vParams.FSType = params["fsType"]

    if exportOptionsParam, exists := params["exportOptions"]; exists {
        if exists {
            exportOptionsList := strings.Split(exportOptionsParam, ";")
            vParams.ExportOptions = make([]common.ShareExportOptions, len(exportOptionsList), len(exportOptionsList))
            for i, o := range exportOptionsList {
                options := strings.Split(o, ",")
                //assert options is len 3
                if len(options) != 3 {
                    return vParams, status.Errorf(codes.InvalidArgument, common.InvalidExportOptions, o)
                }

                rootSquashStr := strings.TrimSpace(options[2])
                rootSquash, err := strconv.ParseBool(rootSquashStr)
                if err != nil {
                    return vParams, status.Errorf(codes.InvalidArgument, common.InvalidRootSquash, rootSquashStr)
                }

                vParams.ExportOptions[i] = common.ShareExportOptions{
                    Subnet:            strings.TrimSpace(options[0]),
                    AccessPermissions: strings.TrimSpace(options[1]),
                    RootSquash:        rootSquash,
                }
            }
        }
    }

    if volumeNameFormat, exists := params["volumeNameFormat"]; exists {
        if strings.Count(volumeNameFormat, "%s") != 1 {
            return vParams, status.Error(codes.InvalidArgument,
                "volumeNameFormat must contain \"%s\" exactly once")
        }
        if strings.Contains(volumeNameFormat, "/") {
            return vParams, status.Errorf(codes.InvalidArgument,
                "volumeNameFormat must not contain forward slashes")
        }
        vParams.VolumeNameFormat = volumeNameFormat
    } else {
        vParams.VolumeNameFormat = DefaultVolumeNameFormat
    }

    return vParams, nil
}

func (d *CSIDriver) ensureShareBackedVolumeExists(
    ctx context.Context,
    hsVolume *HSVolume) error {

    //// Check if Mount Volume Exists
    share, err := d.hsclient.GetShare(hsVolume.Name)
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    if share != nil { // It exists!
        if share.Size != hsVolume.Size {
            return status.Errorf(
                codes.AlreadyExists,
                common.VolumeExistsSizeMismatch,
                share.Size,
                hsVolume.Size)
        }
        // FIXME: Check that it's objectives, export options, deleteDelay(extended info),
        //  etc match (optional functionality with CSI 1.0)

        return nil
    }

    // Create the Mountvolume
    err = d.hsclient.CreateShare(
        hsVolume.Name,
        hsVolume.Path,
        hsVolume.Size,
        hsVolume.Objectives,
        hsVolume.ExportOptions,
        hsVolume.DeleteDelay,
    )

    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    return nil
}

func (d *CSIDriver) ensureBackingShareExists(backingShareName string, hsVolume *HSVolume) (*common.ShareResponse, error){
    share, err := d.hsclient.GetShare(backingShareName)
    if err != nil {
        return share, status.Errorf(codes.Internal, err.Error())
    }
    if share == nil {
        err = d.hsclient.CreateShare(
            backingShareName,
            "/"+backingShareName,
            -1,
            []string{},
            hsVolume.ExportOptions,
            hsVolume.DeleteDelay,
        )
        if err != nil {
            return share, status.Errorf(codes.Internal, err.Error())
        }
        share, err = d.hsclient.GetShare(backingShareName)
        if err != nil {
            return share, status.Errorf(codes.Internal, err.Error())
        }
    }

    return share, err
}

func (d *CSIDriver) ensureDeviceFileExists(
    ctx context.Context,
    backingShare *common.ShareResponse,
    hsVolume *HSVolume) error {

    // Check if File Exists
    hsVolume.Path = backingShare.ExportPath + "/" + hsVolume.Name
    file, err := d.hsclient.GetFile(hsVolume.Path)
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    if file != nil {
        if file.Size != hsVolume.Size {
            return status.Errorf(
                codes.AlreadyExists,
                common.VolumeExistsSizeMismatch,
                file.Size,
                hsVolume.Size)
        }
        return nil
    }

    if hsVolume.Size <= 0 {
        return status.Error(codes.InvalidArgument, common.BlockVolumeSizeNotSpecified)
    }
    available, _ := strconv.ParseInt(backingShare.Space.Available, 10, 64)
    if hsVolume.Size > available {
        return status.Errorf(codes.OutOfRange, common.OutOfCapacity, hsVolume.Size, available)
    }

    if hsVolume.SourceSnapPath != "" {
        // Create from snapshot
        d.hsclient.RestoreFileSnapToDestination(hsVolume.SourceSnapPath, hsVolume.Path)
    } else {
        // Create empty device file
        //// Mount Backing Share

        defer d.UnmountBackingShareIfUnused(backingShare.Name)
        d.EnsureBackingShareMounted(backingShare.Name)
        //// Create an empty file of the correct size
        backingDir := common.BackingShareProvisioningDir + backingShare.ExportPath
        deviceFile := backingDir+"/"+hsVolume.Name
        err = common.MakeEmptyRawFile(deviceFile, hsVolume.Size)
        if err != nil {
            log.Errorf("failed to create backing file for volume, %v", err)
            return err
        }

        // Add filesystem
        if hsVolume.FSType != "" {
            err = common.FormatDevice(deviceFile, hsVolume.FSType)
            if err != nil {
                log.Errorf("failed to format volume, %v", err)
                return err
            }
        }

    }

    b := &backoff.Backoff{
        Max:    10 * time.Second,
        Factor: 1.5,
        Jitter: true,
    }
    startTime := time.Now()

    for time.Now().Sub(startTime) < 10 * time.Minute {
        dur := b.Duration()
        time.Sleep(dur)
        err = d.hsclient.SetObjectives(backingShare.ExportPath, "/" + hsVolume.Name, hsVolume.Objectives, true)
        if err != nil {
            log.Warnf("failed to set objectives on backing file for volume %v", err)
            time.Sleep(time.Second)
        } else {
            break
        }
    }
    if err != nil {
        log.Errorf("failed to set objectives on backing file for volume %v after retrying for 10 minutes", err)
        return err
    }

    return nil
}

func (d *CSIDriver) ensureFileBackedVolumeExists(
    ctx context.Context,
    hsVolume *HSVolume,
    backingShareName string) error {


    //// Check if backing share exists
    defer d.releaseVolumeLock(backingShareName)
    d.getVolumeLock(backingShareName)


    backingShare, err := d.ensureBackingShareExists(backingShareName, hsVolume)
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }

    err = d.ensureDeviceFileExists(ctx, backingShare, hsVolume)

    return err
}

func (d *CSIDriver) CreateVolume(
    ctx context.Context,
    req *csi.CreateVolumeRequest) (
    *csi.CreateVolumeResponse, error) {

    if req.Name == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
    }
    if len(req.Name) > MaxNameLength {
        return nil, status.Errorf(codes.InvalidArgument, common.VolumeIdTooLong, MaxNameLength)
    }
    if req.VolumeCapabilities == nil {
        return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, req.Name)
    }

    vParams, err := parseVolParams(req.Parameters)
    if err != nil {
        return nil, err
    }

    // Check for snapshot source specified
    cs := req.VolumeContentSource
    snap := cs.GetSnapshot()



    // Get volumeMode
    var volumeMode string
    var blockRequested bool
    var filesystemRequested bool
    var fileBacked bool
    var fsType string
    for _, cap := range req.VolumeCapabilities {
        switch cap.AccessType.(type) {
        case *csi.VolumeCapability_Block:
            blockRequested = true
            fileBacked = true
        case *csi.VolumeCapability_Mount:
            filesystemRequested = true
            fsType = cap.GetMount().GetFsType()
            if fsType == "" {
                fsType = vParams.FSType
            }
            if fsType != "nfs" {
                fileBacked = true
            }
        }

    }

    var volumeName string

    if blockRequested && filesystemRequested { // ensure they are not conflicting capabilities in the list
        return nil, status.Errorf(codes.InvalidArgument, common.ConflictingCapabilities)
    } else if blockRequested {
        volumeMode = "Block"
        volumeName = fmt.Sprintf(vParams.VolumeNameFormat, req.Name)
    } else if filesystemRequested {
        volumeMode = "Filesystem"
        volumeName = fmt.Sprintf(vParams.VolumeNameFormat, req.Name)
    } else {
        return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, req.Name)
    }

    cr := req.CapacityRange
    var requestedSize int64
    if cr != nil {
        if cr.LimitBytes != 0 {
            requestedSize = cr.LimitBytes
        } else {
            requestedSize = cr.RequiredBytes
        }
    } else if blockRequested || fsType != "nfs"{
        requestedSize = common.DefaultBackingFileSizeBytes
    } else {
        requestedSize = 0
    }

    if requestedSize > 0 {
        var available int64
        if fileBacked {
            // if it's file backed, we should check capacity of backing share
            var backingShareName string
            if blockRequested {
                backingShareName = vParams.BlockBackingShareName
            } else {
                backingShareName = vParams.MountBackingShareName
            }
            backingShare, err := d.hsclient.GetShare(backingShareName)
            if backingShare == nil || err != nil {
                available, err = d.hsclient.GetClusterAvailableCapacity()
                if err != nil {
                    return nil, status.Error(codes.Internal, err.Error())
                }
            } else {
                available, _ = strconv.ParseInt(backingShare.Space.Available, 10, 64)
            }
        } else {
            available, err = d.hsclient.GetClusterAvailableCapacity()
            if err != nil {
                return nil, status.Error(codes.Internal, err.Error())
            }
        }
        if available < requestedSize {
            return nil, status.Errorf(codes.OutOfRange, common.OutOfCapacity, requestedSize, available)
        }
    }

    defer d.releaseVolumeLock(volumeName)
    d.getVolumeLock(volumeName)

    hsVolume := &HSVolume{
        DeleteDelay:           vParams.DeleteDelay,
        ExportOptions:         vParams.ExportOptions,
        Objectives:            vParams.Objectives,
        BlockBackingShareName: vParams.BlockBackingShareName,
        MountBackingShareName: vParams.MountBackingShareName,
        Size:                  requestedSize,
        Name:                  volumeName,
        VolumeMode:            volumeMode,
        FSType:                fsType,
    }
    if snap != nil {
        hsVolume.SourceSnapPath = strings.SplitN(snap.GetSnapshotId(), "|", 2)[0]
    }

    if volumeMode == "Filesystem" {
        // TODO/FIXME: create from snapshot
        // Workaround:
        // create new share (with weird path)
        // restore snap to weird path
        // move weird path to proper location

        if fsType == "nfs" || fsType == "" {
            hsVolume.Path = common.SharePathPrefix + volumeName
            err = d.ensureShareBackedVolumeExists(ctx, hsVolume)
            if err != nil {
                return nil, err
            }
        } else {
            if hsVolume.MountBackingShareName == "" {
                return nil, status.Error(codes.InvalidArgument, common.MissingMountBackingShareName)
            }

            backingShareName := hsVolume.MountBackingShareName
            err = d.ensureFileBackedVolumeExists(ctx, hsVolume, backingShareName)
            if err != nil {
                return nil, err
            }
        }


    } else if volumeMode == "Block" {
        if hsVolume.BlockBackingShareName == "" {
            return nil, status.Error(codes.InvalidArgument, common.MissingBlockBackingShareName)
        }

        backingShareName := hsVolume.BlockBackingShareName
        err = d.ensureFileBackedVolumeExists(ctx, hsVolume, backingShareName)
        if err != nil {
            return nil, err
        }
    }

    volContext := make(map[string]string)
    volContext["size"] = strconv.FormatInt(hsVolume.Size, 10)
    volContext["mode"] = volumeMode

    if volumeMode == "Block" {
        volContext["blockBackingShareName"] = hsVolume.BlockBackingShareName
    } else if volumeMode == "Filesystem" && fsType != "nfs"{
        volContext["mountBackingShareName"] = hsVolume.MountBackingShareName
        volContext["fsType"] = fsType
    }

    return &csi.CreateVolumeResponse{
        Volume: &csi.Volume{
            CapacityBytes: hsVolume.Size,
            VolumeId:      hsVolume.Path,
            VolumeContext: volContext,
        },
    }, nil
}

func (d *CSIDriver) deleteFileBackedVolume(filepath string) error {
    // look for volume file in all shares
    // FIXME: Optimize this by getting backing share info from the filepath
    // Could also be a help function, findBackingShare

    volumeName := d.GetVolumeNameFromPath(filepath)
    var residingShare *common.ShareResponse
    shares, _ := d.hsclient.ListShares()
    for _, share := range shares {
        if exists, _ := d.hsclient.DoesFileExist(share.ExportPath + "/" + volumeName); exists {
            log.Debugf("found file-backed volume to delete, %s", filepath)
            residingShare = &share
            break
        }
    }

    // Check if file has snapshots and fail
    snaps, _ := d.hsclient.GetFileSnapshots(filepath)
    if len(snaps) > 0 {
        return status.Errorf(codes.FailedPrecondition, common.VolumeDeleteHasSnapshots)
    }

    if residingShare != nil {
        // mount share and delete file
        destination := common.BackingShareProvisioningDir + residingShare.ExportPath
        // grab and defer a lock here for the backing share
        defer d.releaseVolumeLock(residingShare.Name)
        d.getVolumeLock(residingShare.Name)
        defer d.UnmountBackingShareIfUnused(residingShare.Name)
        d.EnsureBackingShareMounted(residingShare.ExportPath)

        //// Delete File
        volumeName := d.GetVolumeNameFromPath(filepath)
        err := common.DeleteFile(destination + "/" + volumeName)
        if err != nil {
            return status.Errorf(codes.Internal, err.Error())
        }
    }

    return nil
}

func (d *CSIDriver) deleteShareBackedVolume(share *common.ShareResponse) error {
    // Check for snapshots
    snaps, err := d.hsclient.GetShareSnapshots(share.Name)
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    if len(snaps) > 0 {
        return status.Errorf(codes.FailedPrecondition, common.VolumeDeleteHasSnapshots)
    }

    deleteDelay := int64(-1)
    if v, exists := share.ExtendedInfo["csi_delete_delay"]; exists {
        if exists {
            deleteDelay, err = strconv.ParseInt(v, 10, 64)
            if err != nil {
                log.Warnf("csi_delete_delay extended info, %s, should be an integer, on share %s; falling back to cluster defaults",
                    v, share.Name)
            }
        }
    }
    err = d.hsclient.DeleteShare(share.Name, deleteDelay)
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    return nil
}

func (d *CSIDriver) DeleteVolume(
    ctx context.Context,
    req *csi.DeleteVolumeRequest) (
    *csi.DeleteVolumeResponse, error) {
    //  If the volume is not specified, return error
    if req.GetVolumeId() == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
    }

    defer d.releaseVolumeLock(req.GetVolumeId())
    d.getVolumeLock(req.GetVolumeId())

    volumeName := d.GetVolumeNameFromPath(req.GetVolumeId())
    share, err := d.hsclient.GetShare(volumeName)
    if err != nil {
        return nil, status.Errorf(codes.Internal, err.Error())
    }
    if share == nil { // Share does not exist, may be a file-backed volume
        err = d.deleteFileBackedVolume(req.GetVolumeId())

        return &csi.DeleteVolumeResponse{}, err
    } else { // Share exists and is a Filesystem
        err = d.deleteShareBackedVolume(share)
        return &csi.DeleteVolumeResponse{}, err
    }

}

func (d *CSIDriver) ControllerPublishVolume(
    ctx context.Context,
    req *csi.ControllerPublishVolumeRequest) (
    *csi.ControllerPublishVolumeResponse, error) {
    return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume not supported")
}

func (d *CSIDriver) ControllerUnpublishVolume(
    ctx context.Context,
    req *csi.ControllerUnpublishVolumeRequest) (
    *csi.ControllerUnpublishVolumeResponse, error) {
    return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume not supported")
}

func (d *CSIDriver) ValidateVolumeCapabilities(
    ctx context.Context,
    req *csi.ValidateVolumeCapabilitiesRequest) (
    *csi.ValidateVolumeCapabilitiesResponse, error) {

    // Validate Arguments
    if req.GetVolumeId() == "" {
        return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
    }
    if len(req.GetVolumeCapabilities()) == 0 {
        return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, req.VolumeId)
    }

    // Find Share
    typeBlock := false
    typeMount := false
    fileBacked := false

    volumeName := d.GetVolumeNameFromPath(req.GetVolumeId())
    share, _ := d.hsclient.GetShare(volumeName)
    if share != nil {
        typeMount = true
    }

    vParams, err := parseVolParams(req.Parameters)
    if err != nil {
        return nil, err
    }

    typeBlock = vParams.BlockBackingShareName != ""
    typeMount = vParams.MountBackingShareName != ""

    //  Check if the specified backing share or file exists
    if share == nil {
        backingFile, err := d.hsclient.GetFile(req.GetVolumeId())
        if err != nil {
            log.Error(err)
        }
        if backingFile == nil {
            return nil, status.Error(codes.NotFound, common.VolumeNotFound)
        } else {
            fileBacked = true
        }
    }

    if fileBacked {
        log.Infof("Validating volume capabilities for file-backed volume %s", volumeName)
    } else if share != nil {
        log.Infof("Validating volume capabilities for share-backed volume %s", volumeName)
    }

    // Calculate Capabilties
    confirmedCapabilities := make([]*csi.VolumeCapability, 0, len(req.VolumeCapabilities))
    for _, c := range req.VolumeCapabilities {
        if (c.GetBlock() != nil) && typeBlock {
            // We have decided to allow multi writer for block devices
            //if c.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
            confirmedCapabilities = append(confirmedCapabilities, c)
            //}
        } else if (c.GetMount() != nil){
            //if it's a file backed, do not allow multinode
            if !(fileBacked &&
                 c.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER) {
                confirmedCapabilities = append(confirmedCapabilities, c)
            } else if typeMount {
                confirmedCapabilities = append(confirmedCapabilities, c)
            }
        }
    }

    // FIXME: Confirm the specified parameters are satisfied. objectives, export options, etc etc
    // This is optional per CSI 1.0.0

    return &csi.ValidateVolumeCapabilitiesResponse{
        Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
            VolumeCapabilities: confirmedCapabilities,
        },
    }, nil
}

func (d *CSIDriver) ListVolumes(
    ctx context.Context,
    req *csi.ListVolumesRequest) (
    *csi.ListVolumesResponse, error) {

    return nil, status.Error(codes.Unimplemented, "")
}

func (d *CSIDriver) GetCapacity(
    ctx context.Context,
    req *csi.GetCapacityRequest) (
    *csi.GetCapacityResponse, error) {

    var blockRequested bool
    var filesystemRequested bool
    fileBacked := false
    var fsType string
    for _, cap := range req.VolumeCapabilities {
        switch cap.AccessType.(type) {
        case *csi.VolumeCapability_Block:
            blockRequested = true
            fileBacked = true
        case *csi.VolumeCapability_Mount:
            filesystemRequested = true
            fsType = cap.GetMount().FsType
            if fsType != "nfs" {
                fileBacked = true
            }
        }
    }

    if blockRequested && filesystemRequested { // ensure they are not conflicting capabilities in the list
        return &csi.GetCapacityResponse{
            AvailableCapacity: 0,
        }, nil
    }

    vParams, err := parseVolParams(req.Parameters)
    if err != nil {
        return nil, err
    }

    var available int64
    //  Check if the specified backing share or file exists
    if fileBacked {
        var backingShareName string
        if blockRequested {
            backingShareName = vParams.BlockBackingShareName
        } else {
            backingShareName = vParams.MountBackingShareName
        }
        backingShare, err := d.hsclient.GetShare(backingShareName)
        if err != nil {
            available = 0
        } else {
            available, _ = strconv.ParseInt(backingShare.Space.Available, 10, 64)
        }

    } else {
        // Return all capacity of cluster for share backed volumes
        available, err = d.hsclient.GetClusterAvailableCapacity()
        if err != nil {
            return nil, status.Error(codes.Internal, err.Error())
        }
    }

    return &csi.GetCapacityResponse{
        AvailableCapacity: available,
    }, nil

}

func (d *CSIDriver) ControllerGetCapabilities(
    ctx context.Context,
    req *csi.ControllerGetCapabilitiesRequest) (
    *csi.ControllerGetCapabilitiesResponse, error) {

    caps := []*csi.ControllerServiceCapability{
        {
            Type: &csi.ControllerServiceCapability_Rpc{
                Rpc: &csi.ControllerServiceCapability_RPC{
                    Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
                },
            },
        },
        /*
            {
                Type: &csi.ControllerServiceCapability_Rpc{
                    Rpc: &csi.ControllerServiceCapability_RPC{
                        Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
                    },
                },
            },
        */
        {
            Type: &csi.ControllerServiceCapability_Rpc{
                Rpc: &csi.ControllerServiceCapability_RPC{
                    Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
                },
            },
        },

        /*		{
                Type: &csi.ControllerServiceCapability_Rpc{
                    Rpc: &csi.ControllerServiceCapability_RPC{
                        Type: csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
                    },
                },
            },*/

        {
            Type: &csi.ControllerServiceCapability_Rpc{
                Rpc: &csi.ControllerServiceCapability_RPC{
                    Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
                },
            },
        },
    }

    return &csi.ControllerGetCapabilitiesResponse{
        Capabilities: caps,
    }, nil
}

func (d *CSIDriver) CreateSnapshot(ctx context.Context,
    req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
    // Check arguments
    if len(req.GetName()) == 0 {
        return nil, status.Error(codes.InvalidArgument, common.EmptySnapshotId)
    }

    if len(req.GetName()) > MaxNameLength {
        return nil, status.Errorf(codes.InvalidArgument, common.SnapshotIdTooLong, MaxNameLength)
    }
    if len(req.GetSourceVolumeId()) == 0 {
        return nil, status.Error(codes.InvalidArgument, common.MissingSnapshotSourceVolumeId)
    }

    defer d.releaseSnapshotLock(req.GetName())
    d.getSnapshotLock(req.GetName())

    // FIXME: Check to see if snapshot already exists?
    //  (using their id somehow?, update the share extended info maybe?) what about for file-backed volumes?
    // do we update extended info on backing share?
    if _, exists := recentlyCreatedSnapshots[req.GetName()]; !exists {
        // find source volume (is it file or share?
        volumeName := d.GetVolumeNameFromPath(req.GetSourceVolumeId())
        share, err := d.hsclient.GetShare(volumeName)
        if err != nil {
            return nil, status.Errorf(codes.Internal, err.Error())
        }
        // Create the snapshot
        var hsSnapName string
        if share != nil {
            hsSnapName, err = d.hsclient.SnapshotShare(volumeName)
        } else {
            hsSnapName, err = d.hsclient.SnapshotFile(req.GetSourceVolumeId())
        }
        if err != nil {
            return nil, status.Errorf(codes.Internal, err.Error())
        }

        // generate snapshot name <sharepath or filepath>|<created snapshot name>
        snapName := fmt.Sprintf("%s|%s", hsSnapName, req.GetSourceVolumeId())
        now := time.Now()
        timeTaken := &timestamp.Timestamp{
            Seconds: now.Unix(),
            Nanos:   int32(now.UnixNano() % time.Second.Nanoseconds()),
        }
        snapshotResponse := &csi.Snapshot{
            SnapshotId:     snapName,
            SourceVolumeId: req.GetSourceVolumeId(),
            CreationTime:   timeTaken,
            ReadyToUse:     true,
        }
        // FIXME: this is a hack to reduce the chance we create a snapshot twice
        recentlyCreatedSnapshots[req.GetName()] = snapshotResponse
    } else {
        if recentlyCreatedSnapshots[req.GetName()].SourceVolumeId != req.GetSourceVolumeId() {
            return nil, status.Errorf(codes.AlreadyExists, "snapshot already exists for a different volume")
        }
    }
    return &csi.CreateSnapshotResponse{
        Snapshot: recentlyCreatedSnapshots[req.GetName()],
    }, nil
}

func (d *CSIDriver) DeleteSnapshot(ctx context.Context,
    req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {

    //  If the snapshot is not specified, return error
    if len(req.SnapshotId) == 0 {
        return nil, status.Error(codes.InvalidArgument, common.EmptySnapshotId)
    }
    snapshotId := req.GetSnapshotId()
    // Split into share name and backend snapshot name
    splitSnapId := strings.SplitN(snapshotId, "|", 2)
    if len(splitSnapId) != 2 {
        return &csi.DeleteSnapshotResponse{}, nil
    }
    snapshotName, path := splitSnapId[0], splitSnapId[1]

    // If the snapshot does not exist then return an idempotent response.

    shareName := d.GetVolumeNameFromPath(path)

    // delete if it's a share snap
    err := d.hsclient.DeleteShareSnapshot(shareName, snapshotName)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    // delete if it's a file snap
    err = d.hsclient.DeleteFileSnapshot(path, snapshotName)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    // Delete snapshot
    return &csi.DeleteSnapshotResponse{}, nil
}

func (d *CSIDriver) ListSnapshots(ctx context.Context,
    req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {

    return nil, status.Error(codes.Unimplemented, "")
}
