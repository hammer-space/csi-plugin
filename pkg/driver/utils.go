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
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	common "github.com/hammer-space/csi-plugin/pkg/common"
)

func IsValueInList(value string, list []string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func GetVolumeNameFromPath(path string) string {
	return filepath.Base(path)
}

func GetSnapshotNameFromSnapshotId(snapshotId string) (string, error) {
	tokens := strings.SplitN(snapshotId, "|", 2)
	if len(tokens) != 2 {
		return "", fmt.Errorf(common.ImproperlyFormattedSnapshotId, snapshotId)
	}
	return tokens[0], nil
}

func GetShareNameFromSnapshotId(snapshotId string) (string, error) {
	tokens := strings.SplitN(snapshotId, "|", 2)
	if len(tokens) != 2 {
		return "", fmt.Errorf(common.ImproperlyFormattedSnapshotId, snapshotId)
	}
	return path.Base(tokens[1]), nil
}

// generate snapshot ID to be stored by the CO
// <created snapshot name>|<sharepath or filepath>
func GetSnapshotIDFromSnapshotName(hsSnapName, sourceVolumeID string) string {
	return fmt.Sprintf("%s|%s", hsSnapName, sourceVolumeID)
}

func (d *CSIDriver) EnsureBackingShareMounted(backingShareName, fqdn string) error {
	backingShare, err := d.hsclient.GetShare(backingShareName)
	if err != nil {
		return status.Errorf(codes.NotFound, err.Error())
	}
	if backingShare != nil {
		backingDir := common.ShareStagingDir + backingShare.ExportPath
		// Mount backing share
		if isMounted, _ := common.IsShareMounted(backingDir); !isMounted {
			mo := []string{}
			err := d.MountShareAtBestDataportal(backingShare.ExportPath, backingDir, mo, fqdn)
			if err != nil {
				log.Errorf("failed to mount backing share, %v", err)
				return err
			}

			log.Infof("mounted backing share, %s", backingDir)
		} else {
			log.Infof("backing share already mounted, %s", backingDir)
		}
		return nil
	}
	return nil
}

func (d *CSIDriver) UnmountBackingShareIfUnused(backingShareName string) (bool, error) {
	backingShare, err := d.hsclient.GetShare(backingShareName)
	if err != nil || backingShare == nil {
		if backingShare == nil {
			log.Infof("backing share %s, dosent exist", backingShareName)
		}
		return false, err
	}
	mountPath := common.ShareStagingDir + backingShare.ExportPath
	if isMounted, _ := common.IsShareMounted(mountPath); !isMounted {
		return true, nil
	}
	// If any loopback devices are using the mount
	output, err := exec.Command("losetup", "-a").CombinedOutput()
	if err != nil {
		return false, status.Errorf(codes.Internal,
			"could not list backing files for loop devices, %v", err)
	}
	devices := strings.Split(string(output), "\n")
	for _, d := range devices {
		if d != "" {
			device := strings.Split(d, " ")
			backingFile := strings.Trim(device[len(device)-1], ":()")
			if strings.Index(backingFile, mountPath) == 0 {
				log.Infof("backing share, %s, still in use by, %s", mountPath, devices[0])
				return false, nil
			}
		}
	}

	log.Infof("unmounting backing share %s", mountPath)
	err = common.UnmountFilesystem(mountPath)
	if err != nil {
		log.Errorf("failed to unmount backing share %s", mountPath)
		return false, err
	}

	return true, err
}

func (d *CSIDriver) MountShareAtBestDataportal(shareExportPath, targetPath string, mountFlags []string, fqdn string) error {
	var err error
	var fipaddr string = ""

	log.Infof("Finding best host exporting %s", shareExportPath)

	portals, err := d.hsclient.GetDataPortals(d.NodeID)
	if err != nil {
		log.Errorf("Could not create list of data-portals, %v", err)
	}

	extracted_endpoint, err := common.ResolveFQDN(fqdn)
	if extracted_endpoint != "" && err == nil { // if fqdn is provided use that ip instead of floatingips
		// check if rpcinfo gives a response
		ok, err := common.CheckNFSExports(extracted_endpoint)
		if err != nil {
			log.Warnf("Could not get exports for fqdn ip at %s. Error: %v", extracted_endpoint, err)
		}
		if ok {
			fipaddr = extracted_endpoint
		}
	} else {
		// Always look for floating data portal IPs
		fipaddr, err = d.hsclient.GetPortalFloatingIp()
		if err != nil {
			log.Errorf("Could not contact Anvil for floating IPs, %v", err)
		}
	}

	MountToDataPortal := func(portal common.DataPortal, mount_options []string) bool {
		addr := ""
		if len(fipaddr) > 0 {
			addr = fipaddr
			log.Infof("Floating IP address detected: %s", fipaddr)
		} else {
			addr = portal.Node.MgmtIpAddress.Address
		}
		export := ""
		// Use configured prefix if specified
		if common.DataPortalMountPrefix != "" {
			export = fmt.Sprintf("%s:%s%s", addr, common.DataPortalMountPrefix, shareExportPath)
		} else {
			// grab exports with showmount
			exports, err := common.GetNFSExports(addr)
			if err != nil {
				log.Infof("Could not get exports for data-portal at %s, %s. Error: %v", addr, portal.Uoid["uuid"], err)
				return false
			}
			log.Infof("Found exports for data-portal %s, %v", addr, exports)

			// Check configured prefix
			// Check the default prefixes
			for _, mountPrefix := range common.DefaultDataPortalMountPrefixes {
				for _, e := range exports {
					if e == fmt.Sprintf("%s%s", mountPrefix, shareExportPath) {
						export = fmt.Sprintf("%s:%s%s", addr, mountPrefix, shareExportPath)
						log.Infof("Found export %s", export)
						break
					}
				}
				if export != "" {
					break
				}
			}
			if export == "" {
				log.Infof("Could not find any matching export on data-portal, %s.", portal.Uoid["uuid"])
				return false
			}
		}
		mo := append(mountFlags, mount_options...)
		err = common.MountShare(export, targetPath, mo)
		if err != nil {
			log.Infof("Could not mount via data-portal, %s. Error: %v", portal.Uoid["uuid"], err)
		} else {
			log.Infof("Mounted via data-portal, %s.", portal.Uoid["uuid"])
			return true
		}
		return false
	}

	log.Infof("Attempting to mount via NFS 4.2.")
	mounted := false
	for _, p := range portals {
		mounted = MountToDataPortal(p, append(mountFlags, "nfsvers=4.2"))
		if mounted {
			break
		}
	}
	if !mounted {
		log.Infof("Could not mount via NFS 4.2, falling back to NFS 3.")
		for _, p := range portals {
			mounted = MountToDataPortal(p, append(mountFlags, "nfsvers=3,nolock"))
			if mounted {
				break
			}
		}
	}
	if mounted {
		return nil
	}
	return fmt.Errorf("could not mount to any data-portals")
}
