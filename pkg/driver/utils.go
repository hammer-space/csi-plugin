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
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	common "github.com/hammer-space/csi-plugin/pkg/common"
)

var (
	maxRetries    int           = 5
	retryInterval time.Duration = 1 * time.Second
)

func init() {
	retryCountStr := os.Getenv("UNMOUNT_RETRY_COUNT")
	if retryCountStr != "" {
		if count, err := strconv.Atoi(retryCountStr); err == nil && count >= 0 && count <= 10 {
			maxRetries = count
		} else {
			log.Warnf("Invalid UNMOUNT_RETRY_COUNT=%s; using default %d", retryCountStr, maxRetries)
		}
	}

	retryIntervalStr := os.Getenv("UNMOUNT_RETRY_INTERVAL")
	if retryIntervalStr != "" {
		if interval, err := time.ParseDuration(retryIntervalStr); err == nil && interval >= 100*time.Millisecond {
			retryInterval = interval
		} else {
			log.Warnf("Invalid or too short UNMOUNT_RETRY_INTERVAL=%s; using default %s", retryIntervalStr, retryInterval)
		}
	}

	log.Infof("Unmount retry config: maxRetries=%d, retryInterval=%s", maxRetries, retryInterval)
}

func IsBlockDevice(fileInfo os.FileInfo) bool {
	mode := fileInfo.Mode()
	return mode&os.ModeDevice != 0 && mode&os.ModeCharDevice == 0
}

// AttachLoopDeviceWithRetry binds a loop device to a filePath with retry support for EBUSY
func AttachLoopDeviceWithRetry(filePath string, readOnly bool) (string, error) {
	// Step 1: Check if file is already attached
	output, err := exec.Command("losetup", "-j", filePath).CombinedOutput()
	if err == nil && strings.TrimSpace(string(output)) != "" {
		// Example output: "/dev/loop3: [12345]:123 (/path/to/file)"
		fields := strings.Split(string(output), ":")
		if len(fields) > 0 {
			device := strings.TrimSpace(fields[0])
			log.Infof("Backing file %s already attached to loop device %s", filePath, device)
			return device, nil
		}
	}

	// Step 2: Attach with retry if not already attached
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		flags := []string{"--find", "--show"}
		if readOnly {
			flags = append(flags, "-r")
		}
		flags = append(flags, filePath)

		output, err := exec.Command("losetup", flags...).CombinedOutput()
		deviceStr := strings.TrimSpace(string(output))

		if err == nil && deviceStr != "" {
			log.Infof("losetup attached file %s to %s", filePath, deviceStr)
			return deviceStr, nil
		}

		log.Warnf("losetup attempt %d failed: %v - output: %s", i+1, err, string(output))

		if strings.Contains(string(output), "resource busy") || strings.Contains(string(output), "device is busy") {
			lastErr = fmt.Errorf("device busy on attempt %d: %w", i+1, err)
			time.Sleep(retryInterval)
			continue
		}

		return "", fmt.Errorf("losetup failed on attempt %d: %s, %w", i+1, string(output), err)
	}

	return "", fmt.Errorf("failed to attach loop device for %s after %d retries: %w", filePath, maxRetries, lastErr)
}

// CleanupLoopDevice detaches a loop device if it exists
func CleanupLoopDevice(dev string) {
	if _, err := os.Stat(dev); os.IsNotExist(err) {
		log.Warnf("Loop device %s does not exist, skipping cleanup", dev)
		return
	}

	for i := 0; i < maxRetries; i++ {
		out, err := exec.Command("losetup", "-d", dev).CombinedOutput()
		if err == nil {
			log.Infof("Loop device %s detached successfully", dev)
			return
		}
		log.Warnf("Attempt %d: Failed to detach loop device %s: %v. Output: %s", i+1, dev, err, string(out))
		time.Sleep(retryInterval)
	}

	log.Errorf("Failed to detach loop device %s after %d retries", dev, maxRetries)
}

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

func (d *CSIDriver) EnsureBackingShareMounted(backingShareName string, hsVol *common.HSVolume) error {
	backingShare, err := d.hsclient.GetShare(backingShareName)
	if err != nil {
		return status.Errorf(codes.NotFound, "%s", err.Error())
	}
	if backingShare != nil {
		backingDir := common.ShareStagingDir + backingShare.ExportPath
		// Mount backing share
		if isMounted, _ := common.IsShareMounted(backingDir); !isMounted {
			err := d.MountShareAtBestDataportal(backingShare.ExportPath, backingDir, hsVol.ClientMountOptions, hsVol.FQDN)
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
		log.Errorf("unable to get share while checking UnmountBackingShareIfUnused. Err %v", err)
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

// Check to select the IP for mount point
// 1. Check if FQDN is provided and its resolvable. If FQDN is there we use that IP only.
// 2. Check if GetPortalFloatingIp have flaoting IPS to be used.
// If we have the IP's in list we use that IP only. We select the IP which response first rpcinfo command.
// 3. If all above check is null of err use anvil IP.

func (d *CSIDriver) MountShareAtBestDataportal(shareExportPath, targetPath string, mountFlags []string, fqdn string) error {
	var err error
	var fipaddr string = ""

	log.Infof("Finding best host exporting %s", shareExportPath)

	portals, err := d.hsclient.GetDataPortals(d.NodeID)
	if err != nil {
		log.Errorf("Could not create list of data-portals, %v", err)
	}

	extracted_endpoint, err := common.ResolveFQDN(fqdn)
	if err != nil {
		log.Errorf("Not able to resolve FQDN=%s checking floating IP's. Error %v", fqdn, err)
	}
	if extracted_endpoint != "" && err == nil { // if fqdn is provided use that ip
		// check if rpcinfo gives a response
		ok, err := common.CheckNFSExports(extracted_endpoint)
		if err != nil {
			log.Warnf("Could not get exports for fqdn %s ip %s. Error: %v", fqdn, extracted_endpoint, err)
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

	// Helper function to check if mountFlags contains nfsvers
	containsNfsvers := func(flags []string) bool {
		for _, flag := range flags {
			if strings.HasPrefix(flag, "nfsvers=") {
				return true
			}
		}
		return false
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
				log.Infof("Could not find any matching export on data-portal address - %s uuid - %s.", portal.Node.MgmtIpAddress.Address, portal.Uoid["uuid"])
				return false
			}
		}
		err = common.MountShare(export, targetPath, mount_options)
		if err != nil {
			log.Infof("Could not mount via data-portal, %s. Error: %v", portal.Uoid["uuid"], err)
		} else {
			log.Infof("Mounted via data-portal, %s.", portal.Uoid["uuid"])
			return true
		}
		return false
	}

	log.Infof("Attempting to mount with provided mount flags.")
	// Attempt to mount with provided mount flags if they contain nfsvers
	if containsNfsvers(mountFlags) {
		for _, p := range portals {
			if MountToDataPortal(p, mountFlags) {
				return nil
			}
		}
		// Remove nfsvers option from mountFlags if mount fails
		var filteredMountFlags []string
		for _, flag := range mountFlags {
			if !strings.HasPrefix(flag, "nfsvers=") {
				filteredMountFlags = append(filteredMountFlags, flag)
			}
		}
		mountFlags = filteredMountFlags
		log.Infof("Mount with provided mount flags failed, removed nfsvers option.")
	}

	// Fallback to NFS 4.2
	log.Infof("Provided mount flags do not contain nfsvers option or failed to mount, using default to NFS 4.2.")
	for _, p := range portals {
		if MountToDataPortal(p, append(mountFlags, "nfsvers=4.2")) {
			return nil
		}
	}

	// Fallback to NFS 3
	log.Infof("Could not mount via NFS 4.2, falling back to NFS 3.")
	for _, p := range portals {
		if MountToDataPortal(p, append(mountFlags, "nfsvers=3,nolock")) {
			return nil
		}
	}

	return fmt.Errorf("could not mount to any data-portals")
}
