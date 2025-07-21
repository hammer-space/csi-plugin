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
	EmptyVolumeId                 = "volume ID cannot be empty"
	VolumeIdTooLong               = "volume ID cannot be longer than %d characters"
	SnapshotIdTooLong             = "shapshot ID cannot be longer than %d characters"
	ImproperlyFormattedSnapshotId = "shapshot ID should be of the format <datetime>|<share export path>, received %s"
	EmptyTargetPath               = "target path cannot be empty"
	EmptyStagingTargetPath        = "staging target path cannot be empty"
	EmptyVolumePath               = "volume Path cannot be empty"
	NoCapabilitiesSupplied        = "no capabilities supplied for volume %s" // volume id
	ConflictingCapabilities       = "cannot request a volume to be both raw and a filesystem"
	InvalidDeleteDelay            = "deleteDelay parameter must be an Integer. Value received '%s'"
	InvalidComment                = "failed to set comment, invalid value"
	InvalidShareNameSize          = "share name cannot be longer than 80 characters"
	InvalidCommentSize            = "share comment cannot be longer than 255 characters"
	EmptySnapshotId               = "snapshot ID cannot be empty"
	MissingSnapshotSourceVolumeId = "snapshot SourceVolumeId cannot be empty"
	MissingBlockBackingShareName  = "blockBackingShareName must be provided when creating BlockVolumes"
	MissingMountBackingShareName  = "mountBackingShareName must be provided when creating Filesystem volumes other than 'nfs'"
	BlockVolumeSizeNotSpecified   = "capacity must be specified for block volumes"
	ShareNotMounted               = "share is not in mounted state."

	InvalidExportOptions             = "export options must consist of 3 values: subnet,access,rootSquash, received '%s'"
	InvalidRootSquash                = "rootSquash must be a bool. Value received '%s'"
	InvalidAdditionalMetadataTags    = "extended Info must be of format key=value, received '%s'"
	InvalidObjectiveNameDoesNotExist = "cannot find objective with the name %s"

	VolumeExistsSizeMismatch = "requested volume exists, but has a different size. Existing: %d, Requested: %d"
	VolumeDeleteHasSnapshots = "volumes with snapshots cannot be deleted, delete snapshots first"
	VolumeBeingDeleted       = "the specified volume is currently being deleted"

	// Not Found errors
	VolumeNotFound              = "volume does not exist"
	FileNotFound                = "file does not exist"
	ShareNotFound               = "share does not exist"
	BackingShareNotFound        = "could not find specified backing share"
	SourceSnapshotNotFound      = "could not find source snapshots"
	SourceSnapshotShareNotFound = "could not find the share for the source snapshot"

	// Internal errors
	UnexpectedHSStatusCode    = "unexpected HTTP response from Hammerspace API: recieved status code %d, expected %d"
	OutOfCapacity             = "requested capacity %d exceeds available %d"
	LoopDeviceAttachFailed    = "failed setting up loop device: device=%s, filePath=%s"
	TargetPathUnknownFiletype = "target path exists but is not a block device nor directory"
	UnknownError              = "unknown internal error"

	// CSI v0
	BlockVolumesUnsupported = "block volumes are unsupported in CSI v0.3"
)
