package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"context"

	"github.com/hammer-space/csi-plugin/pkg/common"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mount share and attach it
func (d *CSIDriver) publishShareBackedVolume(ctx context.Context, volumeId, targetPath string) error {
	// Step 1 create a targetpath
	log.Debugf("Check if target path exist. %s", targetPath)
	if _, err := os.Stat(targetPath); err != nil {
		log.Debugf("Target path does not exist creating it. %s", targetPath)
		if os.IsNotExist(err) {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed to create target path: %w", err)
			}
		} else {
			return fmt.Errorf("failed to stat target path: %w", err)
		}
	}

	// Step 2 check if this is already a mount point
	log.Debugf("Target path exist check if it already a mount point")
	mounted, err := common.SafeIsMountPoint(targetPath)
	log.Debugf("Checking if target is a already a mount point %s", targetPath)
	if err != nil {
		log.Warnf("Error while checking target path is a mount point %s %v", targetPath, err)
		return status.Error(codes.Internal, err.Error())
	}

	// Step 3 check is mounted return
	if mounted {
		log.Debugf("Volume (%s) already published at %s", volumeId, targetPath)
		return nil
	}
	// Step 4 if not mounted created a mount point

	// Bind mount from staging to target
	/** eg: The belwo should be:
	/usr/bin/mount --bind
	/mnt/hammerspace_root/share1/
	/var/lib/kubelet/pods/bbab7dff-b679-4315-9de0-cf1484b4d11d/volumes/kubernetes.io~csi/share1-base-pv/mount.

	* This is the same thing as with "autofs": if you do "/net/foo" as opposed to "/net/foo/" you won't trigger the automounter.
	This is by design, so that readdir() and "ls -l" won't trigger an automtic automount of everything in the directory.
	**/
	sourcePath := filepath.Join(common.BaseBackingShareMountPath, volumeId)
	if !strings.HasSuffix(sourcePath, "/") {
		sourcePath += "/"
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := d.WaitForPathReady(ctx, sourcePath, 500*time.Millisecond); err != nil {
		log.Errorf("Volume path %s not ready: %v", sourcePath, err)
		return status.Errorf(codes.Internal, "volume path %s not ready: %v", sourcePath, err)
	}

	if err := common.BindMountDevice(sourcePath, targetPath); err != nil {
		log.Errorf("bind mount failed for %s: %v", targetPath, err)
		return err
	}
	log.Debugf("Bind mount is success, from source (%s) to target (%s)", sourcePath, targetPath)

	mounted, statErr := common.SafeIsMountPoint(targetPath)
	log.Debugf("Checking mount is point target (%s).", targetPath)
	if statErr != nil {
		log.Warnf("Could not determine mount status of %s: %v", targetPath, statErr)
	} else if !mounted {
		log.Warnf("Bind mount from %s to %s appears to have failed (target is not a mount point)", sourcePath, targetPath)
	} else {
		log.Infof("Bind mount succeeded from %s to %s.", sourcePath, targetPath)
		return nil
	}

	return err

}

// Check base pv exist as backingShareName and create path with backingShareName/exportPath attach to target path
func (d *CSIDriver) publishShareBackedDirBasedVolume(ctx context.Context, backingShareName, exportPath, targetPath, fsType string, mountFlags []string, fqdn string) error {
	defer d.releaseVolumeLock(backingShareName)
	d.getVolumeLock(backingShareName)

	mounted, err := common.SafeIsMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			mounted = false
		} else {
			// Any other error (e.g. permission denied)
			return status.Error(codes.Internal, err.Error())
		}
	}

	if mounted {
		log.Debugf("Volume already published at %s", targetPath)
		return nil
	}

	hsVolume := &common.HSVolume{
		FQDN:               fqdn,
		FSType:             fsType,
		ClientMountOptions: mountFlags,
	}
	log.Infof("check nfs backed volume %v", hsVolume)

	// Ensure the backing share is mounted
	if err := d.EnsureBackingShareMounted(ctx, backingShareName, hsVolume); err != nil {
		return err
	}

	// Mount the file
	log.Infof("Mounting NFS-backed volume at %s", targetPath)

	// Compute full source path inside mounted backing share
	sourceMountPoint := filepath.Join(common.ShareStagingDir, exportPath)

	// Validate that the source exists
	if _, err := os.Stat(sourceMountPoint); err != nil {
		if os.IsNotExist(err) {
			return status.Errorf(codes.NotFound, "export path %s does not exist inside share %s", exportPath, backingShareName)
		}
		return status.Errorf(codes.Internal, "error accessing source path %s: %v", sourceMountPoint, err)
	}

	if err := common.BindMountDevice(sourceMountPoint, targetPath); err != nil {
		log.Errorf("bind mount failed for %s: %v", targetPath, err)
		CleanupLoopDevice(targetPath)
		d.UnmountBackingShareIfUnused(ctx, backingShareName)
		return err
	}

	log.Infof("Successfully mounted %s -> %s", sourceMountPoint, targetPath)
	return nil
}

func (d *CSIDriver) publishFileBackedVolume(ctx context.Context, backingShareName, volumePath, targetPath, fsType string, mountFlags []string, readOnly bool, fqdn string) error {
	defer d.releaseVolumeLock(backingShareName)
	d.getVolumeLock(backingShareName)

	log.Debugf("Recived publish file backed volume request.")
	mounted, err := common.SafeIsMountPoint(targetPath)
	if err != nil {
		log.Errorf("Some error while checking valid mount point")
		if os.IsNotExist(err) {
			// Path does not exist
			if fsType != "" {
				// fsType specified => assume directory mount
				if err := os.MkdirAll(targetPath, 0755); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
			} else {
				// Block volume mount => create file
				parentDir := filepath.Dir(targetPath)
				if err := os.MkdirAll(parentDir, 0755); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
				f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL, 0644)
				if err != nil {
					return status.Error(codes.Internal, err.Error())
				}
				f.Close()
			}
			mounted = false
		} else {
			// Any other error (e.g. permission denied)
			return status.Error(codes.Internal, err.Error())
		}
	}

	if mounted {
		log.Debugf("Volume already published at %s", targetPath)
		return nil
	}

	hsVolume := &common.HSVolume{
		FQDN:               fqdn,
		FSType:             fsType,
		ClientMountOptions: mountFlags,
	}

	log.WithFields(log.Fields{
		"fqdn":             hsVolume.FQDN,
		"FSType":           hsVolume.FSType,
		"backingShareName": backingShareName,
	}).Info("Publish file backed volume.")

	// Ensure the backing share is mounted
	if err := d.EnsureBackingShareMounted(ctx, backingShareName, hsVolume); err != nil {
		return err
	}

	// Mount the file
	log.Infof("Mounting file-backed volume at %s", targetPath)
	filePath := common.ShareStagingDir + volumePath

	if fsType == "" {
		deviceStr, err := AttachLoopDeviceWithRetry(filePath, readOnly)
		if err != nil {
			log.Errorf("failed to attach loop device: %v", err)
			CleanupLoopDevice(deviceStr)
			d.UnmountBackingShareIfUnused(ctx, backingShareName)
			return status.Errorf(codes.Internal, common.LoopDeviceAttachFailed, deviceStr, filePath)
		}
		log.Infof("File %s attached to %s", filePath, deviceStr)

		if err := common.BindMountDevice(deviceStr, targetPath); err != nil {
			log.Errorf("bind mount failed for %s: %v", deviceStr, err)
			CleanupLoopDevice(deviceStr)
			d.UnmountBackingShareIfUnused(ctx, backingShareName)
			return err
		}
	} else {
		if readOnly {
			mountFlags = append(mountFlags, "ro")
		}
		if err := common.MountFilesystem(filePath, targetPath, fsType, mountFlags); err != nil {
			d.UnmountBackingShareIfUnused(ctx, backingShareName)
			return err
		}
	}
	return nil
}

// NodeUnpublishVolume
func (d *CSIDriver) unpublishFileBackedVolume(ctx context.Context, volumePath, targetPath string) error {

	//determine backing share
	backingShareName := filepath.Dir(volumePath)

	defer d.releaseVolumeLock(backingShareName)
	d.getVolumeLock(backingShareName)

	deviceMinor, err := common.GetDeviceMinorNumber(targetPath)
	if err != nil {
		log.Errorf("could not determine corresponding device path for target path, %s, %v", targetPath, err)
		return status.Error(codes.Internal, err.Error())
	}
	lodevice := fmt.Sprintf("/dev/loop%d", deviceMinor)
	log.Infof("found device %s for mount %s", lodevice, targetPath)

	// Remove bind mount
	output, err := common.ExecCommand("umount", targetPath)
	if err != nil {
		log.Errorf("could not remove bind mount, %s", err)
		return status.Error(codes.Internal, err.Error())
	}
	log.Infof("unmounted the targetPath %s. Command output %v ", targetPath, output)
	// delete target path
	err = os.Remove(targetPath)
	if err != nil {
		log.Errorf("could not remove target path, %v", err)
		return status.Error(codes.Internal, err.Error())
	}

	// detach from loopback device
	log.Infof("detaching loop device, %s", lodevice)
	output, err = common.ExecCommand("losetup", "-d", lodevice)
	if err != nil {
		log.Errorf("%s, %v", output, err.Error())
		return status.Error(codes.Internal, err.Error())
	}

	// Unmount backing share if appropriate
	unmounted, err := d.UnmountBackingShareIfUnused(ctx, backingShareName)
	if unmounted {
		log.Infof("unmounted backing share, %s", backingShareName)
	}
	if err != nil {
		log.Errorf("unmounted backing share, %s, failed: %v", backingShareName, err)
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}
