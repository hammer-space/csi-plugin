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
    "errors"
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

func GetVolumeNameFromPath(path string) string {
    return filepath.Base(path)
}

func GetSnapshotNameFromSnapshotId(snapshotId string) (string, error) {
    tokens := strings.SplitN(snapshotId, "|", 2)
    if len(tokens) != 2 {
        return "", errors.New(fmt.Sprintf(common.ImproperlyFormattedSnapshotId, snapshotId))
    }
    return tokens[0], nil
}

func GetShareNameFromSnapshotId(snapshotId string) (string, error) {
    tokens := strings.SplitN(snapshotId, "|", 2)
    if len(tokens) != 2 {
        return "", errors.New(fmt.Sprintf(common.ImproperlyFormattedSnapshotId, snapshotId))
    }
    return path.Base(tokens[1]), nil
}

// generate snapshot ID to be stored by the CO
// <created snapshot name>|<sharepath or filepath>
func GetSnapshotIDFromSnapshotName(hsSnapName, sourceVolumeID string) (string) {
    return fmt.Sprintf("%s|%s", hsSnapName, sourceVolumeID)
}

func (d *CSIDriver) EnsureBackingShareMounted(backingShareName string) error {
    backingShare, err := d.hsclient.GetShare(backingShareName)
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    if err != nil {
        return status.Errorf(codes.Internal, err.Error())
    }
    backingDir := common.ShareStagingDir + backingShare.ExportPath
    // Mount backing share
    if isMounted, _ := common.IsShareMounted(backingDir); !isMounted {
        mo := []string{"sync"}
        err := d.MountShareAtBestDataportal(backingShare.ExportPath, backingDir, mo)
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

func (d *CSIDriver) UnmountBackingShareIfUnused(backingShareName string) (bool, error) {
    backingShare, err := d.hsclient.GetShare(backingShareName)
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

func (d *CSIDriver) MountShareAtBestDataportal(shareExportPath, targetPath string, mountFlags []string) error {
    var err error

    log.Infof("Finding best host exporting %s", shareExportPath)
    if common.UseAnvil {
        dataPortal, _ := d.hsclient.GetAnvilPortal()
        source := fmt.Sprintf("%s:%s", dataPortal, shareExportPath)
        mo := append(mountFlags, "nfsvers=4.2")
        err = common.MountShare(source, targetPath, mo)
        if err == nil {
            log.Infof("Mounted via Anvil portal")
            return nil
        } else {
            log.Infof("Could not mount via Anvil portal, falling back to data-portals. Error: %v", err)
        }
    }

    //Try data portals
    portals, err := d.hsclient.GetDataPortals(d.NodeID)
    if err != nil {
        log.Errorf("Could not create list of data-portals, %v", err)
    }

    for _, p := range portals {
        addr := p.Node.MgmtIpAddress.Address
        export := ""
        // Use configured prefix if specified
        if common.DataPortalMountPrefix != "" {
            export = fmt.Sprintf("%s:%s%s", addr, common.DataPortalMountPrefix, shareExportPath)
        } else {
            // grab exports with showmount
            exports, err := common.GetNFSExports(addr)
            if err != nil {
                log.Infof("Could not get exports for data-portal, %s. Error: %v", p.Uoid["uuid"], err)
                continue
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
                continue
            }
        }
        mo := append(mountFlags, "nfsvers=3")
        err = common.MountShare(export, targetPath, mo)
        if err != nil {
            log.Infof("Could not mount via data-portal, %s. Error: %v", p.Uoid["uuid"], err)
        } else {
            log.Infof("Mounted via data-portal, %s.", p.Uoid["uuid"])
            return nil
        }
    }

    return errors.New("could not mount to any data-portals")
}
