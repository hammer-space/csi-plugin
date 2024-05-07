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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jpillora/backoff"
	timestamp "google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/kubernetes/pkg/util/slice"

	"github.com/container-storage-interface/spec/lib/go/csi"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	client "github.com/hammer-space/csi-plugin/pkg/client"
	"github.com/hammer-space/csi-plugin/pkg/common"
)

const (
	MaxNameLength int = 128
)

var (
	recentlyCreatedSnapshots = map[string]*csi.Snapshot{}
)

func parseVolParams(params map[string]string) (common.HSVolumeParameters, error) {
	vParams := common.HSVolumeParameters{}

	if deleteDelayParam, exists := params["deleteDelay"]; exists {
		var err error
		vParams.DeleteDelay, err = strconv.ParseInt(deleteDelayParam, 10, 64)
		if err != nil {
			return vParams, status.Errorf(codes.InvalidArgument, common.InvalidDeleteDelay, deleteDelayParam)
		}

	} else {
		vParams.DeleteDelay = -1
	}

	if commentParam, exists := params["comment"]; exists {
		// Max comment length in system manager is 255
		if len(commentParam) > 255 {
			return vParams, status.Errorf(codes.InvalidArgument, common.InvalidCommentSize)
		} else {
			vParams.Comment = commentParam
		}
	} else {
		vParams.Comment = "Created by CSI driver"
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
			vParams.ExportOptions = make([]common.ShareExportOptions, len(exportOptionsList))
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
		vParams.VolumeNameFormat = common.DefaultVolumeNameFormat
	}

	if extendedInfoParam, exists := params["additionalMetadataTags"]; exists {
		vParams.AdditionalMetadataTags = map[string]string{}
		if exists {
			extendedInfoList := strings.Split(extendedInfoParam, ",")
			for _, m := range extendedInfoList {
				extendedInfo := strings.Split(m, "=")
				//assert options is len 2
				if len(extendedInfo) != 2 {
					return vParams, status.Errorf(codes.InvalidArgument, common.InvalidAdditionalMetadataTags, m)
				}
				key := strings.TrimSpace(extendedInfo[0])
				value := strings.TrimSpace(extendedInfo[1])

				vParams.AdditionalMetadataTags[key] = value
			}
		}
	}

	return vParams, nil
}

func (d *CSIDriver) ensureShareBackedVolumeExists(
	ctx context.Context,
	hsVolume *common.HSVolume) error {

	//// Check if Mount Volume Exists
	share, err := d.hsclient.GetShare(hsVolume.Name)
	if err != nil {
		return status.Errorf(codes.Internal, err.Error())
	}
	if share != nil { // It exists!
		if share.Size > 0 && (share.Size != hsVolume.Size) {
			return status.Errorf(
				codes.AlreadyExists,
				common.VolumeExistsSizeMismatch,
				share.Size,
				hsVolume.Size)
		}
		if share.ShareState == "REMOVED" {
			return status.Errorf(codes.Aborted, common.VolumeBeingDeleted)
		}
		// FIXME: Check that it's objectives, export options, deleteDelay(extended info),
		//  etc match (optional functionality with CSI 1.0)

		return nil
	}
	if hsVolume.SourceSnapPath != "" {
		// Create from snapshot
		sourceShare, err := d.hsclient.GetShare(hsVolume.SourceSnapShareName)
		if err != nil {
			log.Errorf("Failed to restore from snapshot, %v", err)
			return status.Error(codes.Internal, common.UnknownError)
		}
		if sourceShare == nil {
			return status.Error(codes.NotFound, common.SourceSnapshotShareNotFound)
		}
		snapshots, err := d.hsclient.GetShareSnapshots(hsVolume.SourceSnapShareName)
		if err != nil {
			log.Errorf("Failed to restore from snapshot, %v", err)
			return status.Error(codes.Internal, common.UnknownError)
		}

		snapshotName := path.Base(hsVolume.SourceSnapPath)
		if !slice.ContainsString(snapshots, snapshotName, strings.TrimSpace) {
			return status.Error(codes.NotFound, common.SourceSnapshotNotFound)
		}

		err = d.hsclient.CreateShareFromSnapshot(
			hsVolume.Name,
			hsVolume.Path,
			hsVolume.Size,
			hsVolume.Objectives,
			hsVolume.ExportOptions,
			hsVolume.DeleteDelay,
			hsVolume.Comment,
			hsVolume.SourceSnapPath,
		)

		if err != nil {
			return status.Errorf(codes.Internal, err.Error())
		}
	} else { // Create empty share
		// Create the Mountvolume
		err = d.hsclient.CreateShare(
			hsVolume.Name,
			hsVolume.Path,
			hsVolume.Size,
			hsVolume.Objectives,
			hsVolume.ExportOptions,
			hsVolume.DeleteDelay,
			hsVolume.Comment,
		)

		if err != nil {
			return status.Errorf(codes.Internal, err.Error())
		}
	}
	// generate unique target path on host for setting file metadata
	targetPath := common.ShareStagingDir + "metadata-mounts" + hsVolume.Path
	defer common.UnmountFilesystem(targetPath)
	err = d.publishShareBackedVolume(hsVolume.Path, targetPath, []string{}, false)
	if err != nil {
		log.Warnf("failed to set additional metadata on share %v", err)
	}
	// The hs client expects a trailing slash for directories
	err = common.SetMetadataTags(targetPath+"/", hsVolume.AdditionalMetadataTags)
	if err != nil {
		log.Warnf("failed to set additional metadata on share %v", err)
	}
	return nil
}

func (d *CSIDriver) ensureBackingShareExists(backingShareName string, hsVolume *common.HSVolume) (*common.ShareResponse, error) {
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
			hsVolume.Comment,
		)
		if err != nil {
			return share, status.Errorf(codes.Internal, err.Error())
		}
		share, err = d.hsclient.GetShare(backingShareName)
		if err != nil {
			return share, status.Errorf(codes.Internal, err.Error())
		}

		// generate unique target path on host for setting file metadata
		targetPath := common.ShareStagingDir + "metadata-mounts" + hsVolume.Path
		defer common.UnmountFilesystem(targetPath)
		err = d.publishShareBackedVolume(hsVolume.Path, targetPath, []string{}, false)
		if err != nil {
			log.Warnf("failed to get share backed volume on hsVolumePath %s targetPath %s. Err %v", hsVolume.Path, targetPath, err)
		}
		err = common.SetMetadataTags(targetPath+"/", hsVolume.AdditionalMetadataTags)
		if err != nil {
			log.Warnf("failed to set additional metadata on share %v", err)
		}
	}

	return share, err
}

func (d *CSIDriver) ensureDeviceFileExists(
	ctx context.Context,
	backingShare *common.ShareResponse,
	hsVolume *common.HSVolume) error {

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
	available := backingShare.Space.Available
	if hsVolume.Size > available {
		return status.Errorf(codes.OutOfRange, common.OutOfCapacity, hsVolume.Size, available)
	}

	backingDir := common.ShareStagingDir + backingShare.ExportPath

	deviceFile := backingDir + "/" + hsVolume.Name
	if hsVolume.SourceSnapPath != "" {
		// Create from snapshot
		err := d.hsclient.RestoreFileSnapToDestination(hsVolume.SourceSnapPath, hsVolume.Path)
		if err != nil {
			log.Errorf("Failed to restore from snapshot, %v", err)
			return status.Error(codes.NotFound, common.UnknownError)
		}
	} else {
		// Create empty device file
		//// Mount Backing Share

		defer d.UnmountBackingShareIfUnused(backingShare.Name)
		err = d.EnsureBackingShareMounted(backingShare.Name) // check if share is mounted
		if err != nil {
			log.Errorf("failed to ensure backing share is mounted, %v", err)
			return err
		}

		//// Create an empty file of the correct size

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
		Max:    2 * time.Second,
		Factor: 1.5,
		Jitter: true,
	}
	startTime := time.Now()
	var backingFileExists bool
	for time.Since(startTime) < (10 * time.Minute) {
		dur := b.Duration()
		time.Sleep(dur)
		output, err := common.ExecCommand("ls", deviceFile)
		log.Infof("file exist -> %s", string(output))
		//Wait for file to exists on metadata server
		// backingFileExists, err = d.hsclient.DoesFileExist(hsVolume.Path)
		if err != nil {
			time.Sleep(time.Second)
		} else {
			break
		}
	}
	if !backingFileExists {
		log.Errorf("backing file failed to show up in API after 10 minutes")
		return err
	}

	if len(hsVolume.Objectives) > 0 {
		err = d.hsclient.SetObjectives(backingShare.ExportPath, "/"+hsVolume.Name, hsVolume.Objectives, true)
		if err != nil {
			log.Warnf("failed to set objectives on backing file for volume %v", err)
		}
	}

	// Set additional metadata on file
	err = common.SetMetadataTags(deviceFile, hsVolume.AdditionalMetadataTags)
	if err != nil {
		log.Warnf("failed to set additional metadata on backing file for volume %v", err)
	}

	return nil
}

func (d *CSIDriver) ensureFileBackedVolumeExists(
	ctx context.Context,
	hsVolume *common.HSVolume,
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

	startTime := time.Now()
	// Validate Parameters
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
			fsType = vParams.FSType
			if fsType == "" {
				fsType = "nfs"
			} else if fsType != "nfs" {
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

	// Check we have available capacity
	cr := req.CapacityRange
	var requestedSize int64 = 0
	if cr != nil {
		if cr.LimitBytes != 0 {
			requestedSize = cr.LimitBytes
		} else {
			requestedSize = cr.RequiredBytes
		}
	} else if fileBacked {
		requestedSize = common.DefaultBackingFileSizeBytes
	}

	hsVolume := &common.HSVolume{
		DeleteDelay:            vParams.DeleteDelay,
		ExportOptions:          vParams.ExportOptions,
		Objectives:             vParams.Objectives,
		BlockBackingShareName:  vParams.BlockBackingShareName,
		MountBackingShareName:  vParams.MountBackingShareName,
		Size:                   requestedSize,
		Name:                   volumeName,
		VolumeMode:             volumeMode,
		FSType:                 fsType,
		AdditionalMetadataTags: vParams.AdditionalMetadataTags,
		Comment:                vParams.Comment,
	}
	var backingShare *common.ShareResponse
	// if it's file backed, we should check capacity of backing share
	var backingShareName string
	if blockRequested {
		backingShareName = vParams.BlockBackingShareName
	} else {
		backingShareName = vParams.MountBackingShareName
	}
	backingShare, err = d.hsclient.GetShare(backingShareName)
	if err != nil {
		log.Infof("share dosent exist ensuring share exist.")
		backingShare, err = d.ensureBackingShareExists(backingShare.Name, hsVolume)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	if requestedSize > 0 {
		freeCapacity, err := client.GetCacheData("FREE_CAPACITY")
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		var available int64

		if freeCapacity != nil {
			switch v := freeCapacity.(type) {
			case int64:
				available = v
			default:
				return nil, status.Error(codes.Internal, "unexpected type for free capacity")
			}
		} else {
			log.Infof("getting free capacity from api response")
			// Call your function to get the free capacity from the API response here
			available, err = d.hsclient.GetClusterAvailableCapacity()
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		}

		if available < requestedSize {
			return nil, status.Errorf(codes.OutOfRange, common.OutOfCapacity, requestedSize, available)
		}
	}

	//// Check if objectives exist on the cluster
	var clusterObjectiveNames []string
	cachedObjectiveList, err := client.GetCacheData("OBJECTIVE_LIST_NAMES")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if cachedObjectiveList != nil {
		if objectives, ok := cachedObjectiveList.([]string); ok && len(objectives) > 0 {
			// If cached objective list is not nil and not empty, assign it to clusterObjectiveNames
			clusterObjectiveNames = objectives
		}
	} else {
		// If cached objective list is nil or empty, fetch it from the API
		clusterObjectiveNames, err = d.hsclient.ListObjectiveNames()
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	for _, o := range vParams.Objectives {
		if !IsValueInList(o, clusterObjectiveNames) {
			return nil, status.Errorf(codes.InvalidArgument, common.InvalidObjectiveNameDoesNotExist, o)
		}
	}

	// Create Volume
	defer d.releaseVolumeLock(volumeName)
	d.getVolumeLock(volumeName)

	if snap != nil {
		sourceSnapName, err := GetSnapshotNameFromSnapshotId(snap.GetSnapshotId())
		if err != nil {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		hsVolume.SourceSnapPath = sourceSnapName

		sourceSnapShareName, err := GetShareNameFromSnapshotId(snap.GetSnapshotId())
		if err != nil {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		hsVolume.SourceSnapShareName = sourceSnapShareName
	}

	if fileBacked {
		err = d.ensureFileBackedVolumeExists(ctx, hsVolume, backingShareName)
		if err != nil {
			return nil, err
		}

	} else {
		// TODO/FIXME: create from snapshot
		// Workaround:
		// create new share (with weird path)
		// restore snap to weird path
		// move weird path to proper location
		// NOTE: Expect this to change when we change restore from snapshot in the core product.

		hsVolume.Path = common.SharePathPrefix + volumeName
		err = d.ensureShareBackedVolumeExists(ctx, hsVolume)
		if err != nil {
			return nil, err
		}
	}

	// Create Response
	volContext := make(map[string]string)
	volContext["size"] = strconv.FormatInt(hsVolume.Size, 10)
	volContext["mode"] = volumeMode

	if volumeMode == "Block" {
		volContext["blockBackingShareName"] = hsVolume.BlockBackingShareName
	} else if volumeMode == "Filesystem" && fsType != "nfs" {
		volContext["mountBackingShareName"] = hsVolume.MountBackingShareName
		volContext["fsType"] = fsType
	}

	log.Infof("Total time taken for create volume %v", time.Since(startTime))
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: hsVolume.Size,
			VolumeId:      hsVolume.Path,
			VolumeContext: volContext,
		},
	}, nil
}

func (d *CSIDriver) deleteFileBackedVolume(filepath string) error {
	var exists bool
	if exists, _ = d.hsclient.DoesFileExist(filepath); exists {
		log.Debugf("found file-backed volume to delete, %s", filepath)
	}

	// Check if file has snapshots and fail
	snaps, _ := d.hsclient.GetFileSnapshots(filepath)
	if len(snaps) > 0 {
		return status.Errorf(codes.FailedPrecondition, common.VolumeDeleteHasSnapshots)
	}

	residingShareName := path.Base(path.Dir(filepath))

	if exists {
		// mount share and delete file
		destination := common.ShareStagingDir + path.Dir(filepath)
		// grab and defer a lock here for the backing share
		defer d.releaseVolumeLock(residingShareName)
		d.getVolumeLock(residingShareName)
		defer d.UnmountBackingShareIfUnused(residingShareName)
		err := d.EnsureBackingShareMounted(residingShareName) // check if share is mounted
		if err != nil {
			log.Errorf("failed to ensure backing share is mounted, %v", err)
			return status.Errorf(codes.Internal, err.Error())
		}
		//// Delete File
		volumeName := GetVolumeNameFromPath(filepath)
		err = common.DeleteFile(destination + "/" + volumeName)
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
		if v > "0" {
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
	volumeId := req.GetVolumeId()
	//  If the volume is not specified, return error
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}

	defer d.releaseVolumeLock(volumeId)
	d.getVolumeLock(volumeId)

	volumeName := GetVolumeNameFromPath(volumeId)
	share, err := d.hsclient.GetShare(volumeName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}
	if share == nil { // Share does not exist, may be a file-backed volume
		err = d.deleteFileBackedVolume(volumeId)

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

func (d *CSIDriver) ControllerExpandVolume(
	ctx context.Context,
	req *csi.ControllerExpandVolumeRequest) (
	*csi.ControllerExpandVolumeResponse, error) {
	var requestedSize int64
	if req.GetCapacityRange().GetLimitBytes() != 0 {
		requestedSize = req.GetCapacityRange().GetLimitBytes()
	} else {
		requestedSize = req.GetCapacityRange().GetRequiredBytes()
	}

	// Find Share
	//typeBlock := false
	//typeMount := false
	fileBacked := false

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.VolumeNotFound)
	}

	volumeName := GetVolumeNameFromPath(req.GetVolumeId())
	share, _ := d.hsclient.GetShare(volumeName)
	if share == nil {
		fileBacked = true
	}

	//  Check if the specified backing share or file exists
	if share == nil {
		backingFileExists, err := d.hsclient.DoesFileExist(req.GetVolumeId())
		if err != nil {
			log.Error(err)
		}
		if !backingFileExists {
			return nil, status.Error(codes.NotFound, common.VolumeNotFound)
		} else {
			fileBacked = true
		}
	}

	if fileBacked {
		file, err := d.hsclient.GetFile(req.GetVolumeId())
		if file == nil || err != nil {
			return nil, status.Error(codes.NotFound, common.VolumeNotFound)
		} else {
			log.Debugf("found file-backed volume to resize, %s", req.GetVolumeId())
			// Check backing share size to determine if we can handle new size (look at create volume for how we do this)
			// && check the size of the file only resize if requested is larger than what we have
			// if we are good, then return saying we need a resize on next mount
			if file.Size >= requestedSize {
				return &csi.ControllerExpandVolumeResponse{
					CapacityBytes:         file.Size,
					NodeExpansionRequired: false,
				}, nil
			} else {
				// if required - current > available on backend share
				sizeDiff := requestedSize - file.Size
				backingShareName := path.Base(path.Dir(req.GetVolumeId()))
				backingShare, err := d.hsclient.GetShare(backingShareName)
				var available int64
				if err != nil {
					available = 0
				} else {
					available = backingShare.Space.Available
				}

				if available-sizeDiff < 0 {
					return nil, status.Error(codes.OutOfRange, common.OutOfCapacity)
				}

				return &csi.ControllerExpandVolumeResponse{
					CapacityBytes:         requestedSize,
					NodeExpansionRequired: true,
				}, nil
			}

		}

	} else {
		//Check size: only resize if requested is larger than what we have

		shareName := GetVolumeNameFromPath(req.GetVolumeId())
		if shareName == "" {
			return nil, status.Error(codes.NotFound, common.VolumeNotFound)
		}
		share, err := d.hsclient.GetShare(shareName)
		if share == nil {
			return nil, status.Error(codes.NotFound, common.ShareNotFound)
		}
		var currentSize int64
		if err != nil {
			currentSize = 0
		} else {
			currentSize = share.Space.Available
		}

		if currentSize < requestedSize {
			err = d.hsclient.UpdateShareSize(shareName, requestedSize)
			if err != nil {
				return nil, status.Error(codes.Internal, common.UnknownError)
			}
		}

		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         requestedSize,
			NodeExpansionRequired: false,
		}, nil
	}

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

	volumeName := GetVolumeNameFromPath(req.GetVolumeId())
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
		backingFileExists, err := d.hsclient.DoesFileExist(req.GetVolumeId())
		if err != nil {
			log.Error(err)
		}
		if !backingFileExists {
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
		} else if c.GetMount() != nil {
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
	// get list of volumes
	if req.MaxEntries < 0 {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf(
			"[ListVolumes] Invalid max entries request %v, must not be negative ", req.MaxEntries))
	}

	vlist, err := d.hsclient.ListVolumes()
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("ListVolumes failed with error %v", err))
	}

	ventries := make([]*csi.ListVolumesResponse_Entry, 0, len(vlist))
	publishedNodeIds := make([]string, 0, len(ventries))
	for _, v := range vlist {
		ventry := csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      v.Name,
				CapacityBytes: v.Capacity,
			},
			Status: &csi.ListVolumesResponse_VolumeStatus{
				PublishedNodeIds: publishedNodeIds,
			},
		}

		ventries = append(ventries, &ventry)
	}
	return &csi.ListVolumesResponse{
		Entries: ventries,
	}, nil
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
			available = backingShare.Space.Available
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
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
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
		volumeName := GetVolumeNameFromPath(req.GetSourceVolumeId())
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

		snapID := GetSnapshotIDFromSnapshotName(hsSnapName, req.GetSourceVolumeId())
		now := time.Now()
		timeTaken := &timestamp.Timestamp{
			Seconds: now.Unix(),
			Nanos:   int32(now.UnixNano() % time.Second.Nanoseconds()),
		}
		snapshotResponse := &csi.Snapshot{
			SnapshotId:     snapID,
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

	shareName := GetVolumeNameFromPath(path)

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

	if req.MaxEntries < 0 {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf(
			"[ListSnapshots] Invalid max entries request %v, must not be negative ", req.MaxEntries))
	}

	slist, err := d.hsclient.ListSnapshots()
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("ListSnapshots failed with error %v", err))
	}

	ventries := make([]*csi.ListSnapshotsResponse_Entry, 0, len(slist))
	for _, v := range slist {
		ventry := csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     v.Name,
				SourceVolumeId: v.Name,
				CreationTime: &timestamp.Timestamp{
					Seconds: v.Created,
				},
			},
		}

		ventries = append(ventries, &ventry)
	}
	return &csi.ListSnapshotsResponse{
		Entries: ventries,
	}, nil
}
