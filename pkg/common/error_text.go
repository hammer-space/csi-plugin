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

package common

// These are error messages that may be returned as responses via the gRPC API
// Convention is to be lowercase with no ending punctuation
const (
	// Validation errors
	EmptyVolumeId                 = "Volume ID cannot be empty"
	VolumeIdTooLong               = "Volume ID cannot be longer than %d characters"
	SnapshotIdTooLong             = "Shapshot ID cannot be longer than %d characters"
	ImproperlyFormattedSnapshotId = "Shapshot ID should be of the format <datetime>|<share export path>, received %s"
	EmptyTargetPath               = "Target path cannot be empty"
	EmptyStagingTargetPath        = "Staging target path cannot be empty"
	EmptyVolumePath               = "Volume Path cannot be empty"
	NoCapabilitiesSupplied        = "No capabilities supplied for volume %s" // volume id
	ConflictingCapabilities       = "Cannot request a volume to be both raw and a filesystem"
	InvalidDeleteDelay            = "deleteDelay parameter must be an Integer. Value received '%s'"
	InvalidComment                = "Failed to set comment, invalid value"
	InvalidShareNameSize          = "Share name cannot be longer than 80 characters"
	InvalidCommentSize            = "Share comment cannot be longer than 255 characters"
	EmptySnapshotId               = "Snapshot ID cannot be empty"
	MissingSnapshotSourceVolumeId = "Snapshot SourceVolumeId cannot be empty"
	MissingBlockBackingShareName  = "blockBackingShareName must be provided when creating BlockVolumes"
	MissingMountBackingShareName  = "mountBackingShareName must be provided when creating Filesystem volumes other than 'nfs'"
	BlockVolumeSizeNotSpecified   = "Capacity must be specified for block volumes"
	ShareNotMounted               = "Share is not in mounted state."

	InvalidExportOptions             = "Export options must consist of 3 values: subnet,access,rootSquash, received '%s'"
	InvalidRootSquash                = "rootSquash must be a bool. Value received '%s'"
	InvalidAdditionalMetadataTags    = "Extended Info must be of format key=value, received '%s'"
	InvalidObjectiveNameDoesNotExist = "Cannot find objective with the name %s"

	VolumeExistsSizeMismatch = "Requested volume exists, but has a different size. Existing: %s, Requested: %s"

	VolumeDeleteHasSnapshots = "Volumes with snapshots cannot be deleted, delete snapshots first"
	VolumeBeingDeleted       = "The specified volume is currently being deleted"

	// Not Found errors
	VolumeNotFound              = "Volume does not exist"
	FileNotFound                = "File does not exist"
	ShareNotFound               = "Share does not exist"
	BackingShareNotFound        = "Could not find specified backing share"
	SourceSnapshotNotFound      = "Could not find source snapshots"
	SourceSnapshotShareNotFound = "Could not find the share for the source snapshot"

	// Internal errors
	UnexpectedHSStatusCode    = "Unexpected HTTP response from Hammerspace API: recieved status code %d, expected %d"
	OutOfCapacity             = "Requested capacity %d exceeds available %d"
	LoopDeviceAttachFailed    = "Failed setting up loop device: device=%s, filePath=%s"
	TargetPathUnknownFiletype = "Target path exists but is not a block device nor directory"
	UnknownError              = "Unknown internal error"

	// CSI v0
	BlockVolumesUnsupported = "Block volumes are unsupported in CSI v0.3"
)
