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

import (
	"time"
)

const (
	CsiPluginName = "com.hammerspace.csi"

	// Directory on hosts where backing shares for file-backed volumes will be mounted
	// Must end with a "/"
	ShareStagingDir             = "/tmp"
	SharePathPrefix             = "/"
	DefaultBackingFileSizeBytes = 1073741824
	DefaultVolumeNameFormat     = "%s"

	// MinXfsSizeBytes is the minimum size (300 MiB) below which xfsprogs 6.4+
	// warns "Filesystem should be larger than 300MB" and marks the resulting
	// filesystem "deprecated and will not be supported in future releases".
	// The mkfs.xfs command still returns 0 in that case, so we must reject
	// the request in CreateVolume before formatting.
	MinXfsSizeBytes = 300 * 1024 * 1024

	// MinExt4SizeBytes is the minimum size (20 MiB) below which we refuse to
	// format ext4. mkfs.ext4 accepts filesystems as small as ~8 MiB but the
	// journal + reserved-block overhead means anything smaller than ~20 MiB
	// has almost no usable space; we reject early so users get a clean error
	// instead of a "filesystem full" surprise on the first write.
	MinExt4SizeBytes = 20 * 1024 * 1024

	// Topology keys
	TopologyKeyDataPortal = "topology.csi.hammerspace.com/is-data-portal"
)

var (
	// These should be set at compile time
	Version = "NONE"
	Githash = "NONE"

	CsiVersion = "1"

	// The list of export path prefixes to try to use, in order, when mounting to a data portal
	DefaultDataPortalMountPrefixes = [...]string{"/", "/mnt/data-portal", ""}
	DataPortalMountPrefix          = ""
	CommandExecTimeout             = 300 * time.Second // Seconds
	UseAnvil                       bool
	BaseBackingShareMountPath      = "/var/lib/hammerspace/rootmount"
	BaseVolumeMarkerSourcePath     = "/var/lib/hammerspace/volumes"
)

// Extended info to be set on every share created by the driver
func GetCommonExtendedInfo() map[string]string {
	extendedInfo := map[string]string{
		"csi_created_by_plugin_name":     CsiPluginName,
		"csi_created_by_plugin_version":  Version,
		"csi_created_by_plugin_git_hash": Githash,
		"csi_created_by_csi_version":     CsiVersion,
	}
	return extendedInfo
}
