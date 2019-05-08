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
	VolumeIdTooLong               = "Volume ID cannot be longer than %d characters"
	SnapshotIdTooLong             = "Shapshot ID cannot be longer than %d characters"
	EmptyTargetPath               = "target Path cannot be empty"
	EmptyStagingTargetPath        = "staging Target Path cannot be empty"
	EmptyVolumePath               = "volume Path cannot be empty"
	NoCapabilitiesSupplied        = "no capabilities supplied for volume %s" // volume id
	ConflictingCapabilities       = "cannot request a volume be both raw and a filesystem"
	InvalidDeleteDelay            = "deleteDelay parameter must be an Integer. Value received '%s'"
	EmptySnapshotId               = "snapshot Id cannot be empty"
	MissingSnapshotSourceVolumeId = "snapshot SourceVolumeId cannot be empty"
	MissingBlockBackingShareName  = "blockBackingShareName must be provided when creating BlockVolumes"
	BlockVolumeSizeNotSpecified   = "capacity must be specified for block volumes"

	InvalidExportOptions = "export options must consist of 3 values. Value received '%s'"
	InvalidRootSquash    = "rootSquash must be a bool. Value received '%s'"

	VolumeExistsSizeMismatch = "requested volume exists, but has a different size. Existing: %d, Requested: %d"

	VolumeDeleteHasSnapshots = "volumes with snapshots cannot be deleted, delete snapshots first"

	// Not Found errors
	VolumeNotFound       = "volume does not exist"
	ShareNotFound        = "share does not exist"
	ShareNotMounted      = "share not mounted"
	BackingShareNotFound = "could not find specified backing share"

	// Internal errors
	UnexpectedHSStatusCode    = "unexpected HTTP response from Hammerspace API: recieved status code %d, expected %d"
	OutOfCapacity             = "requested capacity %d exceeds available %d"
	LoopDeviceAttachFailed    = "failed setting up loop device: device=%s, filePath=%s"
	TargetPathUnknownFiletype = "target path exists but is not a block device nor directory"
)
