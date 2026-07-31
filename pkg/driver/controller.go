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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jpillora/backoff"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	timestamp "google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/kubernetes/pkg/util/slice"

	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hammer-space/csi-plugin/pkg/common"
)

const (
	MaxNameLength                  int = 128
	MaxHammerspaceVolumeNameLength int = 80
	restoreVolumeNameSuffix            = "-restore"
)

var (
	recentlyCreatedSnapshots   = map[string]*csi.Snapshot{}
	recentlyCreatedSnapshotsMu sync.Mutex
	tracer                     = otel.Tracer("hammerspace-csi/controller")
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

	if params["cacheEnabled"] != "" {
		cacheEnabled, err := strconv.ParseBool(params["cacheEnabled"])
		if err != nil {
			vParams.CacheEnabled = false
		}
		vParams.CacheEnabled = cacheEnabled
	}

	if params["fqdn"] != "" {
		_, err := common.ResolveFQDN(params["fqdn"])
		if err != nil {
			log.Warnf("fully qualified domain name not specified. Err %v", err.Error())
			vParams.FQDN = ""
		} else {
			vParams.FQDN = params["fqdn"]
		}
	}

	// objectiveTarget controls where objectives are applied for file-backed
	// volumes, and therefore whether CreateVolume pays for the per-file
	// visibility poll (which exists only to gate the per-file objective-set):
	//   share (default) - objectives live on the backing SHARE only; skip the
	//                     per-file objective-set and its Anvil visibility poll,
	//                     so CreateVolume returns as soon as the local mkfs
	//                     completes. Best for the common single-site shape.
	//   file / both     - additionally apply per-file objectives (pays the
	//                     poll). Use for per-volume / multi-site policy.
	switch target := params["objectiveTarget"]; target {
	case "", "share":
		vParams.ObjectiveTarget = "share"
	case "file", "both":
		vParams.ObjectiveTarget = target
	default:
		return vParams, status.Errorf(codes.InvalidArgument,
			"invalid objectiveTarget %q (must be one of: share, file, both)", target)
	}

	return vParams, nil
}

// checkFileBackedMinSize rejects a file-backed volume whose requested size is
// below the per-fsType minimum (xfsprogs 6.4+ deprecates sub-300MiB XFS; ext4
// below ~20MiB has almost no usable space). Non file-backed fsTypes and sizes
// at/above the floor return nil.
func checkFileBackedMinSize(fsType string, requestedSize int64) error {
	const mib = 1024 * 1024
	switch fsType {
	case "xfs":
		if requestedSize < common.MinXfsSizeBytes {
			return status.Errorf(codes.InvalidArgument, common.XfsSizeBelowMinimum,
				common.MinXfsSizeBytes, common.MinXfsSizeBytes/mib,
				requestedSize, requestedSize/mib)
		}
	case "ext4":
		if requestedSize < common.MinExt4SizeBytes {
			return status.Errorf(codes.InvalidArgument, common.Ext4SizeBelowMinimum,
				common.MinExt4SizeBytes, common.MinExt4SizeBytes/mib,
				requestedSize, requestedSize/mib)
		}
	case "ext3":
		// ext3 is no longer a supported file-backed filesystem — reject it up
		// front (rather than silently formatting it like ext4 with no size
		// floor). Use ext4 or xfs.
		return status.Error(codes.InvalidArgument, common.Ext3NotSupported)
	}
	return nil
}

func getMountFlagsFromCapabilities(capabilities []*csi.VolumeCapability) []string {
	for _, capability := range capabilities {
		if mount := capability.GetMount(); mount != nil {
			return append([]string{}, mount.MountFlags...)
		}
	}

	return nil
}

func (d *CSIDriver) ensureNFSDirectoryExists(ctx context.Context, backingShareName string, hsVolume *common.HSVolume) error {
	// Check if backing share exists
	unlock, err := d.acquireVolumeLock(ctx, backingShareName)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return err
	}
	defer unlock()

	backingShare, err := d.ensureBackingShareExists(ctx, backingShareName, hsVolume)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", err.Error())
	}

	// generate unique target path on host for setting file metadata
	targetPath := common.ShareStagingDir + backingShare.ExportPath
	deviceFile := targetPath + "/" + hsVolume.Name

	// mount the share to create the directory
	defer d.UnmountBackingShareIfUnused(ctx, backingShare.Name)
	err = d.EnsureBackingShareMounted(ctx, backingShare.Name, hsVolume) // check if share is mounted
	if err != nil {
		log.Errorf("failed to ensure backing share is mounted, %v", err)
		return err
	}

	// create NFS directory inside base share
	err = common.MakeEmptyRawFolder(deviceFile)
	if err != nil {
		log.Errorf("failed to create backing folder for volume, %v", err)
		return err
	}

	return nil
}

func (d *CSIDriver) restoreNFSDirectoryFromSnapshot(ctx context.Context, backingShareName string, hsVolume *common.HSVolume) error {
	unlock, err := d.acquireVolumeLock(ctx, backingShareName)
	if err != nil {
		return err
	}
	defer unlock()

	backingShare, err := d.ensureBackingShareExists(ctx, backingShareName, hsVolume)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", err.Error())
	}

	defer d.UnmountBackingShareIfUnused(ctx, backingShare.Name)
	err = d.EnsureBackingShareMounted(ctx, backingShare.Name, hsVolume)
	if err != nil {
		log.Errorf("failed to ensure backing share is mounted, %v", err)
		return err
	}

	mountedBackingSharePath := common.ShareStagingDir + backingShare.ExportPath
	destinationDir := filepath.Join(mountedBackingSharePath, hsVolume.Name)

	if len(hsVolume.SourceSnapFilePaths) > 0 {
		err = common.MakeEmptyRawFolder(destinationDir)
		if err != nil {
			log.Errorf("failed to create restored NFS directory %s, %v", destinationDir, err)
			return err
		}

		sourceVolumePath := path.Clean(hsVolume.SourceSnapVolumePath)
		for _, fileSnapshotPath := range hsVolume.SourceSnapFilePaths {
			sourceFilePath, relativeFilePath, err := getSourcePathFromFileSnapshot(sourceVolumePath, fileSnapshotPath)
			if err != nil {
				return status.Errorf(codes.Internal, "failed to parse file snapshot path %s: %v", fileSnapshotPath, err)
			}

			destinationFilePath := path.Join(common.SharePathPrefix, backingShareName, hsVolume.Name, relativeFilePath)
			err = d.hsclient.RestoreFileSnapToDestination(ctx, fileSnapshotPath, destinationFilePath)
			if err != nil {
				log.Errorf("failed to restore NFS file snapshot %s to %s, %v", fileSnapshotPath, destinationFilePath, err)
				return status.Errorf(codes.Internal, "failed to restore NFS file snapshot %s: %v", fileSnapshotPath, err)
			}

			log.Debugf("restored NFS file snapshot source=%s relative=%s destination=%s", sourceFilePath, relativeFilePath, destinationFilePath)
		}

		return nil
	}

	sourceDirName := path.Base(hsVolume.SourceSnapVolumePath)
	if sourceDirName == "." || sourceDirName == "/" || sourceDirName == "" {
		return status.Errorf(codes.InvalidArgument, "invalid source snapshot volume path %q", hsVolume.SourceSnapVolumePath)
	}

	sourceSnapshotDir := path.Join(common.SharePathPrefix, backingShareName, ".snapshot", hsVolume.SourceSnapPath, sourceDirName)
	destinationPath := path.Join(common.SharePathPrefix, backingShareName, hsVolume.Name)
	err = d.hsclient.RestoreFileSnapToDestination(ctx, sourceSnapshotDir, destinationPath)
	if err != nil {
		log.Errorf("failed to clone NFS directory snapshot %s to %s, %v", sourceSnapshotDir, destinationPath, err)
		return status.Errorf(codes.Internal, "failed to clone NFS directory snapshot: %v", err)
	}

	return nil
}

func getSourcePathFromFileSnapshot(sourceVolumePath, fileSnapshotPath string) (string, string, error) {
	const fileSnapshotMarker = "/.fsnapshot/"

	cleanSourceVolumePath := path.Clean(sourceVolumePath)
	splitSnapshotPath := strings.SplitN(fileSnapshotPath, fileSnapshotMarker, 2)
	if len(splitSnapshotPath) != 2 {
		return "", "", fmt.Errorf("snapshot path does not contain %s", fileSnapshotMarker)
	}

	sourceFilePath := path.Clean(splitSnapshotPath[0])
	if sourceFilePath == cleanSourceVolumePath {
		snapshotRelativePath := path.Dir(splitSnapshotPath[1])
		if snapshotRelativePath == "." || snapshotRelativePath == "/" || snapshotRelativePath == "" || strings.HasPrefix(snapshotRelativePath, "../") {
			return "", "", fmt.Errorf("snapshot path %s does not contain a relative file path under source volume path %s", fileSnapshotPath, cleanSourceVolumePath)
		}
		return path.Join(cleanSourceVolumePath, snapshotRelativePath), snapshotRelativePath, nil
	}
	if !strings.HasPrefix(sourceFilePath, cleanSourceVolumePath+"/") {
		return "", "", fmt.Errorf("snapshot source file path %s is not under source volume path %s", sourceFilePath, cleanSourceVolumePath)
	}

	relativeFilePath := strings.TrimPrefix(sourceFilePath, cleanSourceVolumePath+"/")
	if relativeFilePath == "" || relativeFilePath == "." || strings.HasPrefix(relativeFilePath, "../") {
		return "", "", fmt.Errorf("snapshot path %s does not contain a relative file path under source volume path %s", fileSnapshotPath, cleanSourceVolumePath)
	}

	return sourceFilePath, relativeFilePath, nil
}

func (d *CSIDriver) ensureShareBackedVolumeExists(ctx context.Context, hsVolume *common.HSVolume) error {
	ctx, span := tracer.Start(ctx, "ensureShareBackedVolumeExists", trace.WithAttributes(
		attribute.String("volume.name", hsVolume.Name),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "ensureShareBackedVolumeExists")(nil)

	// Check if the Mount Volume Exists
	share, err := d.hsclient.GetShare(ctx, hsVolume.Name)
	if err != nil {
		return fmt.Errorf("failed to get share: %w", err)
	}
	if share != nil {
		if share.Size != hsVolume.Size {
			return status.Errorf(codes.AlreadyExists, common.VolumeExistsSizeMismatch, share.Size, hsVolume.Size)
		}

		if share.ShareState == "REMOVED" {
			return status.Errorf(codes.Aborted, common.VolumeBeingDeleted)
		}
		return err
	}

	if hsVolume.SourceSnapPath != "" {
		// Create from snapshot
		sourceShare, err := d.hsclient.GetShare(ctx, hsVolume.SourceSnapShareName)
		if err != nil {
			log.Errorf("Failed to restore from snapshot, %v", err)
			return status.Error(codes.Internal, common.UnknownError)
		}
		if sourceShare == nil {
			return status.Error(codes.NotFound, common.SourceSnapshotShareNotFound)
		}
		snapshots, err := d.hsclient.GetShareSnapshots(ctx, hsVolume.SourceSnapShareName)
		if err != nil {
			log.Errorf("Failed to restore from snapshot, %v", err)
			return status.Error(codes.Internal, common.UnknownError)
		}

		snapshotName := path.Base(hsVolume.SourceSnapPath)
		if !slice.ContainsString(snapshots, snapshotName, strings.TrimSpace) {
			return status.Error(codes.NotFound, common.SourceSnapshotNotFound)
		}

		restoredPath, err := d.hsclient.CreateShareFromSnapshot(
			ctx,
			hsVolume.Name,
			hsVolume.Path,
			hsVolume.Size,
			hsVolume.Objectives,
			hsVolume.ExportOptions,
			hsVolume.DeleteDelay,
			hsVolume.Comment,
			hsVolume.SourceSnapShareName,
			sourceShare.ExportPath,
			hsVolume.SourceSnapPath,
		)

		if err != nil {
			return status.Errorf(codes.Internal, "%s", err.Error())
		}
		hsVolume.Path = restoredPath
	} else {
		// Share is not there, try creating a new share
		err = d.hsclient.CreateShare(
			ctx,
			hsVolume.Name,
			hsVolume.Path,
			hsVolume.Size,
			hsVolume.Objectives,
			hsVolume.ExportOptions,
			hsVolume.DeleteDelay,
			hsVolume.Comment,
		)

		if err != nil {
			return status.Errorf(codes.Internal, "%s", err.Error())
		}
	}
	// generate unique target path on host for setting file metadata
	// mount -t nfs 10:200.../share1 /tmp/metadata-mounts/share1
	targetPath := common.ShareStagingDir + "/metadata-mounts" + hsVolume.Path
	log.Debugf("Creating empty folder with path %s", targetPath)

	defer common.UnmountFilesystem(ctx, targetPath)

	log.Debugf("Created empty folder with path %s", targetPath)
	err = d.publishShareBackedVolume(ctx, hsVolume.Path, targetPath, hsVolume.MountFlags, hsVolume.FQDN)
	if err != nil {
		log.Warnf("failed to get share backed volume on hsVolumePath %s targetPath %s. Err %v", hsVolume.Path, targetPath, err)
	}
	log.Debugf("Published share backed volume %s on targetpath %s", hsVolume.Path, targetPath)

	// The hs client expects a trailing slash for directories
	err = common.SetMetadataTags(ctx, targetPath+"/", hsVolume.AdditionalMetadataTags)
	if err != nil {
		log.Warnf("failed to set additional metadata on share %v", err)
	}
	log.Debugf("Apply metadata finshed on published share backed volume %s", targetPath)

	return nil
}

func (d *CSIDriver) ensureBackingShareExists(ctx context.Context, backingShareName string, hsVolume *common.HSVolume) (*common.ShareResponse, error) {
	ctx, span := tracer.Start(ctx, "ensureBackingShareExists", trace.WithAttributes(
		attribute.String("backing_share", backingShareName),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "ensureBackingShareExists")(nil)

	share, err := d.hsclient.GetShare(ctx, backingShareName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", err.Error())
	}
	if share == nil {
		err = d.hsclient.CreateShare(
			ctx,
			backingShareName,
			hsVolume.Path,
			-1,
			hsVolume.Objectives,
			hsVolume.ExportOptions,
			hsVolume.DeleteDelay,
			hsVolume.Comment,
		)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s", err.Error())
		}
		share, err = d.hsclient.GetShare(ctx, backingShareName)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s", err.Error())
		}
		log.Infof("Checking if get share response back non nil share.")
		if share == nil {
			log.Errorf("Error while creating share from ensure backing share exist method.")
			return nil, fmt.Errorf("requested share [%s] not found", backingShareName)
		}
		// generate unique target path on host for setting file metadata
		targetPath := common.ShareStagingDir + "/metadata-mounts" + hsVolume.Path
		defer common.UnmountFilesystem(ctx, targetPath)
		err = d.publishShareBackedVolume(ctx, hsVolume.Path, targetPath, hsVolume.MountFlags, hsVolume.FQDN)
		if err != nil {
			log.Warnf("failed to get share backed volume on hsVolumePath %s targetPath %s. Err %v", hsVolume.Path, targetPath, err)
		}
		err = common.SetMetadataTags(ctx, targetPath+"/", hsVolume.AdditionalMetadataTags)
		if err != nil {
			log.Warnf("failed to set additional metadata on share %v", err)
		}
	}

	return share, err
}

func (d *CSIDriver) ensureDeviceFileExists(ctx context.Context, backingShare *common.ShareResponse, hsVolume *common.HSVolume) error {
	log.WithFields(log.Fields{
		"backingShare": backingShare,
		"hsVolume":     hsVolume,
	}).Debug("ensureDeviceFileExists is called.")

	hsVolume.Path = backingShare.ExportPath + "/" + hsVolume.Name
	log.Debugf("checking if file exist %s", hsVolume.Path)

	// Step 1: Check if file already exists in metadata
	file, err := d.hsclient.GetFile(ctx, hsVolume.Path)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", err.Error())
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

	// Step 2: Validate size and capacity
	if hsVolume.Size <= 0 {
		return status.Error(codes.InvalidArgument, common.BlockVolumeSizeNotSpecified)
	}
	available := backingShare.Space.Available
	if hsVolume.Size > available {
		return status.Errorf(codes.OutOfRange, common.OutOfCapacity, hsVolume.Size, available)
	}

	backingDir := common.ShareStagingDir + backingShare.ExportPath
	deviceFile := backingDir + "/" + hsVolume.Name

	// Step 3: Create file from snapshot or empty
	if hsVolume.SourceSnapPath != "" {
		// Restore from snapshot
		err := d.hsclient.RestoreFileSnapToDestination(ctx, hsVolume.SourceSnapPath, hsVolume.Path)
		if err != nil {
			log.Errorf("Failed to restore from snapshot, %v", err)
			return status.Error(codes.NotFound, common.UnknownError)
		}
	} else {
		// Create empty file. Take a refcounted reference on the backing mount so it
		// stays mounted for the duration of this create but is NOT held under the
		// per-backing-share lock; the mkfs below therefore runs concurrently with
		// other creates on the same share. The share is unmounted only once the last
		// in-flight create releases (see acquire/releaseBackingMount).
		err = d.acquireBackingMount(ctx, backingShare, hsVolume)
		if err != nil {
			log.Errorf("failed to ensure backing share is mounted, %v", err)
			return err
		}
		defer d.releaseBackingMount(ctx, backingShare)

		log.Debugf("ensureDeviceFileExists mounted backing share %s", backingShare.Name)

		err = common.MakeEmptyRawFile(ctx, deviceFile, hsVolume.Size)
		if err != nil {
			log.Errorf("failed to create backing file for volume, %v", err)
			return err
		}

		// Add filesystem
		log.Debugf("ensureDeviceFileExists created empty raw file over backing share %s and path %s", backingShare.Name, deviceFile)
		if hsVolume.FSType != "" {
			err = common.FormatDevice(ctx, deviceFile, hsVolume.FSType)
			if err != nil {
				log.Errorf("failed to format volume, %v", err)
				return err
			}
		}
		log.Debugf("ensureDeviceFileExists formatted file %s, with fstype %s", deviceFile, hsVolume.FSType)
	}

	// Step 4: Apply objectives + metadata on a fresh deadline, but inherit
	// trace context so spans stay attached to the CreateVolume trace.
	// context.WithoutCancel detaches from the gRPC handler's cancellation
	// (which would otherwise kill the long poll loop) while preserving the
	// OTel span context attached via tracer.Start above.
	metadataCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()

	// objectiveTarget=share (default): the backing share already carries the
	// objectives, so we skip the per-file objective-set AND the Anvil
	// visibility poll that exists only to gate it. CreateVolume then returns
	// as soon as the local mkfs above completes (seconds, not tens of seconds),
	// and we avoid the GET /files 500-storm the poll generates under load.
	// Metadata tags still apply - they operate on the freshly-created local
	// file over the mount and need no Anvil round-trip.
	if hsVolume.ObjectiveTarget == "file" || hsVolume.ObjectiveTarget == "both" {
		err = d.applyObjectiveAndMetadata(metadataCtx, backingShare, hsVolume, deviceFile)
		if err != nil {
			log.Warnf("Unable to apply objective and metadata over backing share %s, device path %s: %v", backingShare.Name, deviceFile, err)
		}
	} else {
		log.Debugf("objectiveTarget=share: skipping per-file objective+visibility poll for %s", hsVolume.Path)
		if err := common.SetMetadataTags(metadataCtx, deviceFile, hsVolume.AdditionalMetadataTags); err != nil {
			log.Warnf("Failed to set additional metadata on backing file for volume %s: %v", deviceFile, err)
		}
	}

	return nil
}

// ensure from hs system /share/file exist to apply objective and metadata
func (d *CSIDriver) applyObjectiveAndMetadata(ctx context.Context, backingShare *common.ShareResponse, hsVolume *common.HSVolume, deviceFile string) error {
	ctx, span := tracer.Start(ctx, "applyObjectiveAndMetadata", trace.WithAttributes(
		attribute.String("backing_share", backingShare.Name),
		attribute.String("path", hsVolume.Path),
	))
	defer span.End()

	// Poll Anvil's metadata API until the backing file we just created over
	// NFS becomes visible to the management plane. The loop is the dominant
	// cost of CreateVolume in our traces (often tens of seconds), so it gets
	// its own span and per-attempt count.
	pollCtx, pollSpan := tracer.Start(ctx, "applyObjectiveAndMetadata.waitForFileVisible", trace.WithAttributes(
		attribute.String("path", hsVolume.Path),
	))
	b := &backoff.Backoff{
		Max:    1 * time.Second,
		Factor: 1.5,
		Jitter: true,
	}
	startTime := time.Now()
	var backingFileExists bool
	var err error
	attempts := 0
	for time.Since(startTime) < (10 * time.Minute) {
		dur := b.Duration()
		time.Sleep(dur)
		attempts++
		// Wait for file to exist on metadata server
		log.Debugf("Checking existance of file %s", hsVolume.Path)
		backingFileExists, err = d.hsclient.DoesFileExist(pollCtx, hsVolume.Path)
		if err != nil {
			log.Warnf("Error checking file existence: %v", err)
			time.Sleep(time.Second)
			continue
		}
		if backingFileExists {
			log.Debugf("Successfully found backing file %s", hsVolume.Path)
			break
		}
		log.Warnf("File does not exist yet: %s", hsVolume.Path)
	}
	pollSpan.SetAttributes(
		attribute.Int("attempts", attempts),
		attribute.Bool("file_visible", backingFileExists),
	)
	pollSpan.End()

	if !backingFileExists {
		log.Errorf("backing file failed to show up in API after 10 minutes")
		return err
	}

	if len(hsVolume.Objectives) > 0 {
		filePath := GetVolumeNameFromPath(hsVolume.Path)
		err = d.hsclient.SetObjectives(ctx, backingShare.Name, filePath, hsVolume.Objectives)
		if err != nil {
			log.Errorf("failed to set objectives on backing file for volume: %v\n", err)
			return err
		}
	}

	// Set additional metadata on file
	err = common.SetMetadataTags(ctx, deviceFile, hsVolume.AdditionalMetadataTags)
	if err != nil {
		log.Errorf("Failed to set additional metadata on backing file for volume: %v\n", err)
	}
	return err
}

func (d *CSIDriver) ensureFileBackedVolumeExists(ctx context.Context, hsVolume *common.HSVolume, backingShareName string) error {

	log.WithFields(log.Fields{
		"backingShareName": backingShareName,
		"hsVolume":         hsVolume,
	}).Debugf("ensureFileBackedVolumeExists is called.")
	// The backing share is a shared resource: two concurrent first-volume creates
	// must not both CreateShare it. Serialize ONLY the share create-if-not-exists
	// under the per-backing-share lock, then release it immediately. The per-volume
	// device file created afterwards is independent, so releasing the lock here lets
	// file creation (mkfs) run concurrently across the provisioner worker threads
	// instead of serializing every file on the share behind this one lock. The
	// backing mount is kept alive for the duration by acquire/releaseBackingMount.
	unlock, err := d.acquireVolumeLock(ctx, backingShareName)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return err
	}
	backingShare, err := d.ensureBackingShareExists(ctx, backingShareName, hsVolume)
	unlock()
	if err != nil {
		return status.Errorf(codes.Internal, "%s", err.Error())
	}
	log.Debugf("Backing share existed %s", backingShareName)

	return d.ensureDeviceFileExists(ctx, backingShare, hsVolume)
}

func (d *CSIDriver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (_ *csi.CreateVolumeResponse, err error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/CreateVolume", trace.WithAttributes(
		attribute.String("volume_name", req.Name),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Controller/CreateVolume")(&err)

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
	var fsType, volumeMode string
	var blockRequested, filesystemRequested, fileBacked bool

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
				fileBacked = false
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
		volumeName, err = formatCreateVolumeName(req.Name, vParams.VolumeNameFormat, snap != nil)
		if err != nil {
			return nil, err
		}
	} else if filesystemRequested {
		volumeMode = "Filesystem"
		if snap != nil && fsType == "nfs" && vParams.MountBackingShareName == "" && req.Parameters["csi.storage.k8s.io/pvc/name"] != "" {
			volumeName, err = formatCreateVolumeName(req.Parameters["csi.storage.k8s.io/pvc/name"], common.DefaultVolumeNameFormat, true)
			if err != nil {
				return nil, err
			}
		} else {
			volumeName, err = formatCreateVolumeName(req.Name, vParams.VolumeNameFormat, snap != nil)
			if err != nil {
				return nil, err
			}
		}
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

	// Reject file-backed volumes below the per-fsType minimum, failing fast with
	// codes.InvalidArgument so kubelet surfaces the reason instead of silently
	// formatting a broken FS. See checkFileBackedMinSize / the config.go floors.
	if fileBacked {
		if err := checkFileBackedMinSize(fsType, requestedSize); err != nil {
			return nil, err
		}
	}

	var backingShareName string
	if blockRequested {
		backingShareName = vParams.BlockBackingShareName
	} else if filesystemRequested {
		backingShareName = vParams.MountBackingShareName
		if backingShareName == "" && fsType == "nfs" {
			backingShareName = volumeName
		}
	}
	volumePath := common.SharePathPrefix + backingShareName
	var volID string = volumePath
	if fileBacked {
		// file-backed volumes live *within* the backing share
		volID = fmt.Sprintf("%s/%s", volumePath, volumeName)
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
		Path:                   volumePath,
		FSType:                 fsType,
		AdditionalMetadataTags: vParams.AdditionalMetadataTags,
		Comment:                vParams.Comment,
		FQDN:                   vParams.FQDN,
		MountFlags:             getMountFlagsFromCapabilities(req.VolumeCapabilities),
		ObjectiveTarget:        vParams.ObjectiveTarget,
	}

	// if it's file backed, we should check capacity of backing share
	if requestedSize > 0 {
		freeCapacity, err := common.GetCacheData("FREE_CAPACITY")
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
			log.Infof("getting free capacity from (/cntl/state) api response")
			// Call your function to get the free capacity from the API response here
			available, err = d.hsclient.GetClusterAvailableCapacity(ctx)
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		}

		if available < requestedSize {
			return nil, status.Errorf(codes.OutOfRange, common.OutOfCapacity, requestedSize, available)
		}
	}

	// Check if objectives exist on the cluster
	var clusterObjectiveNames []string
	cachedObjectiveList, err := common.GetCacheData("OBJECTIVE_LIST_NAMES")
	if err != nil {
		log.Warnf("Unable to read cached objective list; continuing volume create without objective validation: %v", err)
	}
	if cachedObjectiveList != nil {
		if objectives, ok := cachedObjectiveList.([]string); ok && len(objectives) > 0 {
			// If cached objective list is not nil and not empty, assign it to clusterObjectiveNames
			clusterObjectiveNames = objectives
		}
	} else {
		// If cached objective list is nil or empty, fetch it from the API
		clusterObjectiveNames, err = d.hsclient.ListObjectiveNames(ctx)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	for _, o := range vParams.Objectives {
		log.Debugf("Checking for objective inside the objective list.")
		if !IsValueInList(o, clusterObjectiveNames) {
			log.WithFields(log.Fields{
				"Supplied objective list": clusterObjectiveNames,
			}).Errorf("No objective found in objective list")
			return nil, status.Errorf(codes.InvalidArgument, common.InvalidObjectiveNameDoesNotExist, o)
		}
		log.Debugf("Found objective supplied in Storage class objective params.")
	}

	// Create Volume
	// Acquire BEFORE defer; with timeout so we never hang forever
	unlock, err := d.acquireVolumeLock(ctx, volumeName)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return nil, err
	}
	defer unlock()

	if snap != nil {
		sourceSnapName, err := GetSnapshotNameFromSnapshotId(snap.GetSnapshotId())
		if err != nil {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		hsVolume.SourceSnapPath = sourceSnapName

		sourceSnapVolumePath, err := GetSnapshotSourceVolumeId(snap.GetSnapshotId())
		if err != nil {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		hsVolume.SourceSnapVolumePath = sourceSnapVolumePath

		sourceSnapShareName, err := GetShareNameFromSnapshotId(snap.GetSnapshotId())
		if err != nil {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		hsVolume.SourceSnapShareName = sourceSnapShareName

		sourceSnapFilePaths, err := GetFileSnapshotPathsFromSnapshotId(snap.GetSnapshotId())
		if err != nil {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		hsVolume.SourceSnapFilePaths = sourceSnapFilePaths

		log.Info("using snapshot as volume source")
	}

	log.Infof("Volume Mode=%s, fsType=%s, Block=%t, FileBacked=%t", volumeMode, fsType, blockRequested, fileBacked)

	if !fileBacked && fsType == "nfs" && vParams.MountBackingShareName != "" {
		// This function is called when user want new nfs share inside one base share
		log.Debugf("Creating share for NFS volume inside base NFS share dir %s with path %s", vParams.MountBackingShareName, hsVolume.Path)
		if hsVolume.SourceSnapPath != "" {
			err = d.restoreNFSDirectoryFromSnapshot(ctx, backingShareName, hsVolume)
		} else {
			err = d.ensureNFSDirectoryExists(ctx, backingShareName, hsVolume)
		}
		if err != nil {
			log.Errorf("failed to ensure base NFS share (%s): %v", backingShareName, err)
			return nil, status.Errorf(codes.Internal, "failed to ensure base NFS share (%s): %v", backingShareName, err)
		}
		// mark the NFS created folder as a backing share, so that it can be used as ID for volumeDelete
		hsVolume.Path = common.SharePathPrefix + backingShareName + "/" + hsVolume.Name
		volID = fmt.Sprintf("%s/%s", volumePath, volumeName)
	} else if fileBacked {
		// This function will be called in case of Block and File backed share
		log.Debugf("Creating share for File system volume (block or files) inside base backingshare name dir %s with path %s", backingShareName, hsVolume.Path)
		err = d.ensureFileBackedVolumeExists(ctx, hsVolume, backingShareName)
		if err != nil {
			return nil, err
		}

	} else {
		// NOTE
		// No way in product to restore snapshot of one share to restore to another share.
		// The way we are going to achive this to make NFS share inside some base share dir (eg- k8s-nfs-share)
		// In that case all new created share will have path like /k8s-nfs-share/pvc-csi-uuid
		// Then we create snapshot of that share /pvc-csi-uuid which will be inside /k8s-nfs-share/.snapshot
		// Then restore the snapshot to the new created share from snapshot content source.
		log.Debugf("Creating share for NFS volume with path %s", hsVolume.Path)
		err = d.ensureShareBackedVolumeExists(ctx, hsVolume)
		if err != nil {
			return nil, err
		}
		volID = hsVolume.Path
	}

	// Create Response
	volContext := make(map[string]string)
	volContext["size"] = strconv.FormatInt(hsVolume.Size, 10)
	volContext["mode"] = volumeMode
	if fqdn := req.GetParameters()["fqdn"]; fqdn != "" {
		volContext["fqdn"] = fqdn
	}
	switch volumeMode {
	case "Block":
		volContext["blockBackingShareName"] = hsVolume.BlockBackingShareName
	case "Filesystem":
		volContext["mountBackingShareName"] = hsVolume.MountBackingShareName
		volContext["fsType"] = fsType
	}

	log.Infof("Total time taken for create volume %v", time.Since(startTime))

	resp := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: hsVolume.Size,
			VolumeId:      volID,
			VolumeContext: volContext,
		},
	}

	if snap != nil {
		resp.Volume.ContentSource = &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: snap.GetSnapshotId(),
				},
			},
		}
	}

	log.WithField("response", resp).Info("volume was created")
	return resp, nil
}

func (d *CSIDriver) deleteFileBackedVolume(ctx context.Context, filepath string) error {
	ctx, span := tracer.Start(ctx, "deleteFileBackedVolume", trace.WithAttributes(
		attribute.String("file.path", filepath),
	))
	defer span.End()
	var exists bool
	if exists, _ = d.hsclient.DoesFileExist(ctx, filepath); exists {
		log.Debugf("found file-backed volume to delete, %s", filepath)
	}

	// Check if file has snapshots and fail
	snaps, _ := d.hsclient.GetFileSnapshots(ctx, filepath)
	if len(snaps) > 0 {
		return status.Errorf(codes.FailedPrecondition, common.VolumeDeleteHasSnapshots)
	}

	residingShareName := path.Base(path.Dir(filepath))

	hsVolume := &common.HSVolume{
		FQDN:       "",
		MountFlags: nil,
	}

	if exists {
		// mount share and delete file
		destination := common.ShareStagingDir + path.Dir(filepath)
		// grab and defer a lock here for the backing share
		// Acquire BEFORE defer; with timeout so we never hang forever
		unlock, err := d.acquireVolumeLock(ctx, residingShareName)
		if err != nil {
			// surfaces to kubelet instead of hanging forever
			return err
		}
		defer unlock()
		// Route the mount through acquire/releaseBackingMount — the same refcounted
		// mechanism the create path uses — instead of a bare EnsureBackingShareMounted
		// plus a deferred UnmountBackingShareIfUnused. This makes delete a first-class
		// participant in the backing-mount refcount: it holds a reference for the whole
		// delete (so a concurrent create that has already taken its own reference can't
		// have the share unmounted out from under it), and it serializes its mount and
		// unmount under the same per-directory lock (mountLockFor) as create rather than
		// mutating the mount with no shared lock held. GetShare gives us the ShareResponse
		// acquireBackingMount needs; the file is known to exist here, so the share does too.
		backingShare, err := d.hsclient.GetShare(ctx, residingShareName)
		if err != nil || backingShare == nil {
			log.Errorf("failed to get backing share %s while deleting file-backed volume: %v", residingShareName, err)
			return status.Errorf(codes.Internal, "unable to get backing share %s: %v", residingShareName, err)
		}
		if err = d.acquireBackingMount(ctx, backingShare, hsVolume); err != nil {
			log.Errorf("failed to ensure backing share is mounted, %v", err)
			return status.Errorf(codes.Internal, "%s", err.Error())
		}
		defer d.releaseBackingMount(ctx, backingShare)
		// Delete File
		volumeName := GetVolumeNameFromPath(filepath)
		err = common.DeleteFile(destination + "/" + volumeName)
		if err != nil {
			return status.Errorf(codes.Internal, "%s", err.Error())
		}
	}

	return nil
}

func (d *CSIDriver) deleteShareBackedVolume(ctx context.Context, share *common.ShareResponse) error {
	// Check for snapshots
	snaps, err := d.hsclient.GetShareSnapshots(ctx, share.Name)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", err.Error())
	}
	if len(snaps) > 0 {
		return status.Errorf(codes.FailedPrecondition, common.VolumeDeleteHasSnapshots)
	}

	deleteDelay := int64(-1)
	if v, exists := share.ExtendedInfo["csi_delete_delay"]; exists {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			deleteDelay = parsed
		} else {
			log.Warnf("csi_delete_delay extended info, %s, should be an integer, on share %s; falling back to cluster defaults", v, share.Name)
		}
	}
	err = d.hsclient.DeleteShare(ctx, share.Name, deleteDelay)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", err.Error())
	}
	return nil
}

func (d *CSIDriver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (_ *csi.DeleteVolumeResponse, err error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/DeleteVolume", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Controller/DeleteVolume")(&err)

	volumeId := req.GetVolumeId()
	log.Infof("Delete volume request for volume id, %s", volumeId)
	//  If the volume is not specified, return error
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}

	unlock, err := d.acquireVolumeLock(ctx, volumeId)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return nil, err
	}
	defer unlock()

	// A file-backed volume lives *inside* a backing share, so its volume ID is
	// structurally distinguishable from a share-backed one. Decide from the ID
	// instead of probing Anvil with a GetShare that, for file-backed volumes,
	// always 404s.
	if isFileBackedVolumeID(volumeId) {
		err = d.deleteFileBackedVolume(ctx, volumeId)
		return &csi.DeleteVolumeResponse{}, err
	}

	volumeName := GetVolumeNameFromPath(volumeId)
	share, err := d.hsclient.GetShare(ctx, volumeName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", err.Error())
	}
	if share == nil { // legacy/single-segment ID with no share: treat as file-backed
		err = d.deleteFileBackedVolume(ctx, volumeId)
		return &csi.DeleteVolumeResponse{}, err
	}
	// Share exists and is a Filesystem
	err = d.deleteShareBackedVolume(ctx, share)
	return &csi.DeleteVolumeResponse{}, err
}

// ControllerGetVolume implements the ControllerServer interface for CSI.
// This is a stub implementation; you should update it to provide actual logic as needed.
func (c *CSIDriver) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ControllerGetVolume is not implemented")
}

// ControllerModifyVolume implements the ControllerServer interface for CSI.
// This is a stub implementation; you should update it to provide actual logic as needed.
func (c *CSIDriver) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ControllerGetVolume is not implemented")
}

func (d *CSIDriver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume not supported")
}

func (d *CSIDriver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume not supported")
}

func (d *CSIDriver) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	var requestedSize int64
	if req.GetCapacityRange().GetLimitBytes() != 0 {
		requestedSize = req.GetCapacityRange().GetLimitBytes()
	} else {
		requestedSize = req.GetCapacityRange().GetRequiredBytes()
	}
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/ExpandVolume", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
		attribute.Int64("volume.space.requested", req.GetCapacityRange().GetRequiredBytes()),
	))
	defer span.End()

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.VolumeNotFound)
	}

	// Decide file-backed vs share-backed structurally from the volume ID, avoiding
	// a GetShare probe that always 404s for file-backed volumes. The branches below
	// still confirm the volume actually exists (GetFile / GetShare).
	fileBacked := isFileBackedVolumeID(req.GetVolumeId())
	if !fileBacked {
		volumeName := GetVolumeNameFromPath(req.GetVolumeId())
		share, _ := d.hsclient.GetShare(ctx, volumeName)
		if share == nil {
			// Fallback for legacy/unexpected IDs: confirm via file existence.
			backingFileExists, ferr := d.hsclient.DoesFileExist(ctx, req.GetVolumeId())
			if ferr != nil {
				log.Error(ferr)
			}
			if !backingFileExists {
				return nil, status.Error(codes.NotFound, common.VolumeNotFound)
			}
			fileBacked = true
		}
	}

	if fileBacked {
		file, err := d.hsclient.GetFile(ctx, req.GetVolumeId())
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
				backingShare, err := d.hsclient.GetShare(ctx, backingShareName)
				if err != nil {
					return nil, fmt.Errorf("share not found %w", err)
				}
				var available int64 = 0
				if backingShare != nil {
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
		share, err := d.hsclient.GetShare(ctx, shareName)
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
			err = d.hsclient.UpdateShareSize(ctx, shareName, requestedSize)
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

func (d *CSIDriver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/ValidateVolumeCapabilities", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
		attribute.Int("capabilities.count", len(req.VolumeCapabilities)),
	))
	defer span.End()

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
	share, _ := d.hsclient.GetShare(ctx, volumeName)
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
		backingFileExists, err := d.hsclient.DoesFileExist(ctx, req.GetVolumeId())
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

func (d *CSIDriver) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/ListVolumes", trace.WithAttributes())
	defer span.End()

	// get list of volumes
	if req.MaxEntries < 0 {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf(
			"[ListVolumes] Invalid max entries request %v, must not be negative ", req.MaxEntries))
	}

	vlist, err := d.hsclient.ListVolumes(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("ListVolumes failed: %v", err))
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

func (d *CSIDriver) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/GetCapacity", trace.WithAttributes())
	defer span.End()

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

	var available int64 = 0
	//  Check if the specified backing share or file exists
	if fileBacked {
		var backingShareName string
		if blockRequested {
			backingShareName = vParams.BlockBackingShareName
		} else {
			backingShareName = vParams.MountBackingShareName
		}
		backingShare, err := d.hsclient.GetShare(ctx, backingShareName)
		if err != nil {
			available = 0
		}
		if backingShare != nil {
			available = backingShare.Space.Available
		}

	} else {
		// Return all capacity of cluster for share backed volumes
		available, err = d.hsclient.GetClusterAvailableCapacity(ctx)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	return &csi.GetCapacityResponse{
		AvailableCapacity: available,
	}, nil

}

func (d *CSIDriver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {

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

func (d *CSIDriver) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (_ *csi.CreateSnapshotResponse, err error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/CreateSnapshot", trace.WithAttributes(
		attribute.String("snapshot.name", req.GetName()),
		attribute.String("source.volume.id", req.GetSourceVolumeId()),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Controller/CreateSnapshot")(&err)

	// Check arguments
	log.WithFields(log.Fields{
		"Name":  req.Name,
		"Param": req.SourceVolumeId,
	}).Infof("Create snapshot request recived.")

	if len(req.GetName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, common.EmptySnapshotId)
	}

	if len(req.GetName()) > MaxNameLength {
		return nil, status.Errorf(codes.InvalidArgument, common.SnapshotIdTooLong, MaxNameLength)
	}
	if len(req.GetSourceVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, common.MissingSnapshotSourceVolumeId)
	}

	unlock, err := d.acquireSnapshotLock(ctx, req.Name)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return nil, err
	}
	defer unlock()

	// FIXME: Check to see if snapshot already exists?
	//  (using their id somehow?, update the share extended info maybe?) what about for file-backed volumes?
	// do we update extended info on backing share?
	// recentlyCreatedSnapshots is shared across all snapshot names, unlike
	// acquireSnapshotLock above which only serializes calls for this one name,
	// so every access to the map itself must go through its own mutex.
	recentlyCreatedSnapshotsMu.Lock()
	cachedSnapshot, exists := recentlyCreatedSnapshots[req.GetName()]
	recentlyCreatedSnapshotsMu.Unlock()
	if !exists {
		sourceVolumeID := req.GetSourceVolumeId()
		var snapID string
		// find source volume (is it file or share?
		volumeName := GetVolumeNameFromPath(req.GetSourceVolumeId())
		// Decide file- vs share-backed structurally from the source volume ID,
		// avoiding a GetShare probe that 404s for file-backed sources; fall back to
		// GetShare only for a non-file-backed ID (legacy/unexpected handles).
		fileBackedSource := isFileBackedVolumeID(req.GetSourceVolumeId())
		if !fileBackedSource {
			share, gerr := d.hsclient.GetShare(ctx, volumeName)
			if gerr != nil {
				return nil, status.Errorf(codes.Internal, "%s", gerr.Error())
			}
			if share == nil {
				fileBackedSource = true
			}
		}
		// Consistency-freeze: for FILE-BACKED source volumes only, locate
		// every pod that has this volume mounted and issue
		// `fsfreeze --freeze` on the mount path. This forces XFS/ext4 to
		// quiesce (log flushed, no in-flight transactions) so Anvil's
		// byte-level snapshot captures a clean on-disk state. If the pod
		// is missing fsfreeze, or we can't find pods for this volume, we
		// log and proceed — matching the best-effort semantics of Velero
		// pre-hooks. See freezer.go.
		//
		// A share-backed (NFS) volume has Anvil owning the filesystem
		// end-to-end, with no local journal to quiesce; FIFREEZE also fails
		// EOPNOTSUPP on NFS mounts. Skip the freeze in that case.
		var frozen []FrozenTarget
		if d.freezer != nil && fileBackedSource {
			frozen = d.freezer.FreezeForVolumeHandle(ctx, req.GetSourceVolumeId())
		}
		// Create the snapshot
		var hsSnapName string
		if !fileBackedSource {
			hsSnapName, err = d.hsclient.SnapshotShare(ctx, volumeName)
			snapID = GetSnapshotIDFromSnapshotName(hsSnapName, sourceVolumeID)
		} else {
			hsSnapName, err = d.hsclient.SnapshotFile(ctx, sourceVolumeID)
			if err == nil {
				snapID = GetSnapshotIDFromSnapshotName(hsSnapName, sourceVolumeID)
			} else {
				fileSnapshotErr := err
				backingShareName := GetBackingShareNameFromPath(sourceVolumeID)
				if backingShareName != "" && backingShareName != volumeName {
					backingShare, shareErr := d.hsclient.GetShare(ctx, backingShareName)
					if shareErr != nil {
						return nil, status.Errorf(codes.Internal, "%s", shareErr.Error())
					}
					if backingShare != nil {
						log.WithFields(log.Fields{
							"sourceVolumeID":   sourceVolumeID,
							"backingShareName": backingShareName,
							"fileSnapshotErr":  fileSnapshotErr,
						}).Info("falling back to directory-scoped file snapshots for directory-backed NFS volume")
						hsSnapNames, snapshotFilesErr := d.hsclient.SnapshotFiles(ctx, path.Join(sourceVolumeID, "*"))
						err = snapshotFilesErr
						if err == nil {
							snapID, err = GetFileSnapshotsIDFromSnapshotNames(hsSnapNames, sourceVolumeID)
						}
					}
				}
				if snapID == "" && err == nil {
					err = fileSnapshotErr
				}
			}
		}
		// Always unfreeze, even if snapshot failed — otherwise the app pod
		// stays blocked on writes indefinitely. Use a context detached from the
		// gRPC request cancellation (context.WithoutCancel): if the snapshotter
		// sidecar's deadline expires while SnapshotFile/SnapshotShare above is
		// still running, a cancelled ctx would make the unfreeze exec fail fast
		// without ever reaching the pod, leaving the workload's filesystem frozen.
		if d.freezer != nil && fileBackedSource {
			d.freezer.Unfreeze(context.WithoutCancel(ctx), frozen)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s", err.Error())
		}

		now := time.Now()
		timeTaken := &timestamp.Timestamp{
			Seconds: now.Unix(),
			Nanos:   int32(now.UnixNano() % time.Second.Nanoseconds()),
		}
		snapshotResponse := &csi.Snapshot{
			SnapshotId:     snapID,
			SourceVolumeId: sourceVolumeID,
			CreationTime:   timeTaken,
			ReadyToUse:     true,
		}
		// FIXME: this is a hack to reduce the chance we create a snapshot twice
		recentlyCreatedSnapshotsMu.Lock()
		recentlyCreatedSnapshots[req.GetName()] = snapshotResponse
		recentlyCreatedSnapshotsMu.Unlock()
		cachedSnapshot = snapshotResponse
	} else {
		if cachedSnapshot.SourceVolumeId != req.GetSourceVolumeId() {
			return nil, status.Errorf(codes.AlreadyExists, "snapshot already exists for a different volume")
		}
	}
	return &csi.CreateSnapshotResponse{
		Snapshot: cachedSnapshot,
	}, nil
}

func (d *CSIDriver) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/DeleteSnapshot", trace.WithAttributes(
		attribute.String("snapshot.id", req.GetSnapshotId()),
	))
	defer span.End()

	snapshotId := req.GetSnapshotId()
	if len(snapshotId) == 0 {
		return nil, status.Error(codes.InvalidArgument, common.EmptySnapshotId)
	}

	snapshotName, nameErr := GetSnapshotNameFromSnapshotId(snapshotId)
	sourceVolumeID, volErr := GetSnapshotSourceVolumeId(snapshotId)
	if nameErr != nil || volErr != nil {
		log.Warnf("DeleteSnapshot: malformed snapshot ID %s; treating as success (idempotent)", snapshotId)
		return &csi.DeleteSnapshotResponse{}, nil
	}

	// If the snapshot does not exist then return an idempotent response.

	// File-vs-share discriminator, decided structurally from the source volume
	// path (the "|"-suffix of the snapshot ID): a file-backed volume is a FILE
	// inside a backing share (multi-segment path -> file snapshot); a native NFS
	// volume IS a share (single-segment path -> share snapshot). This avoids a
	// GetShare probe that 404s for every file-backed snapshot; GetShare is kept
	// as a fallback for non-file-backed paths.
	// (Historical: the original `GetVolumeNameFromPath(path) != ""` test was
	// ALWAYS true, so every delete was routed to DeleteShareSnapshot and
	// file-backed snapshots were orphaned on the Anvil, blocking source-volume
	// deletion.)
	shareName := GetVolumeNameFromPath(sourceVolumeID)

	var err error
	if isFileBackedVolumeID(sourceVolumeID) {
		err = d.hsclient.DeleteFileSnapshot(ctx, sourceVolumeID, snapshotName)
	} else {
		share, gerr := d.hsclient.GetShare(ctx, shareName)
		if gerr != nil {
			return nil, status.Error(codes.Internal, gerr.Error())
		}
		if share != nil {
			err = d.hsclient.DeleteShareSnapshot(ctx, shareName, snapshotName)
		} else {
			err = d.hsclient.DeleteFileSnapshot(ctx, sourceVolumeID, snapshotName)
		}
	}

	if err != nil {
		// https://github.com/container-storage-interface/spec/blob/master/spec.md#controller-deletesnapshot
		if strings.Contains(err.Error(), "not found") {
			log.Infof("DeleteSnapshot: snapshot %s not found, treating as success", snapshotId)
			return &csi.DeleteSnapshotResponse{}, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Delete snapshot
	return &csi.DeleteSnapshotResponse{}, nil
}

func (d *CSIDriver) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	// Start a span for tracing
	ctx, span := tracer.Start(ctx, "Controller/ListSnapshots", trace.WithAttributes(
		attribute.String("snapshot.id", req.GetSnapshotId()),
		attribute.String("source.volume.id", req.GetSourceVolumeId()),
	))
	defer span.End()

	if req.MaxEntries < 0 {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf(
			"[ListSnapshots] Invalid max entries request %v, must not be negative ", req.MaxEntries))
	}

	// Initialize a slice to hold the snapshot entries
	var snapshots []*csi.ListSnapshotsResponse_Entry

	// Fetch all snapshots from the backend storage
	backendSnapshots, err := d.hsclient.ListSnapshots(ctx, req.SnapshotId, req.SourceVolumeId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if backendSnapshots == nil {
		log.Infof("No snapshot found backendSnapshots %v", backendSnapshots)
		return &csi.ListSnapshotsResponse{
			Entries: snapshots,
		}, nil
	}

	// Apply filtering based on snapshot_id and source_volume_id
	for _, snapshot := range backendSnapshots {
		// Filter by snapshot_id if provided
		if req.GetSnapshotId() != "" && snapshot.Id != req.GetSnapshotId() {
			continue
		}

		// Filter by source_volume_id if provided
		if req.GetSourceVolumeId() != "" && snapshot.SourceVolumeId != req.GetSourceVolumeId() {
			continue
		}

		// Build the SnapshotEntry for each matching snapshot
		snapshotEntry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SizeBytes:      snapshot.Size,
				SnapshotId:     snapshot.Id,
				ReadyToUse:     snapshot.ReadyToUse,
				SourceVolumeId: snapshot.SourceVolumeId,
				CreationTime: &timestamp.Timestamp{
					Seconds: snapshot.Created,
				},
			},
		}

		// Add the snapshot entry to the response
		snapshots = append(snapshots, snapshotEntry)
	}

	// Return the ListSnapshotsResponse with filtered snapshots
	return &csi.ListSnapshotsResponse{
		Entries: snapshots,
	}, nil
}
