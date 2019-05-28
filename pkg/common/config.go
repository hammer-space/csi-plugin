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

import "time"

const (
    CsiPluginName = "com.hammerspace.csi"

    // Directory on hosts where backing shares for file-backed volumes will be mounted
    // Must end with a "/"
    BackingShareProvisioningDir = "/tmp/"
    SharePathPrefix             = "/"
    DefaultBackingFileSizeBytes = 1073741824

    // Topology keys
    TopologyKeyDataPortal       = "topology.csi.hammerspace.com/is-data-portal"
)

var (
    // These should be set at compile time
    Version = "NONE"
    Githash = "NONE"

    CsiVersion = "1"

    // The list of export path prefixes to try to use, in order, when mounting to a data portal with NFS v3
    DefaultDataPortalMountPrefixes = [...]string{"/mnt/data-portal", "/"}
    DataPortalMountPrefix = ""
    CommandExecTimeout = 300 * time.Second  // Seconds
)
