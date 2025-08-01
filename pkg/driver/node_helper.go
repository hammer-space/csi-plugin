package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hammer-space/csi-plugin/pkg/common"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mount share and attach it
func (d *CSIDriver) publishShareBackedVolume(ctx context.Context, volumeId, targetPath string, mountFlags []string, readOnly bool, fqdn string) error {

	notMnt, err := common.SafeIsLikelyNotMountPoint(targetPath)
	log.Debugf("Checking if target is a already a mount point %s", targetPath)
	if err != nil {
		log.Errorf("Error while checking target path is a mount point %s %v", targetPath, err)
		if os.IsNotExist(err) {
			log.Debugf("File dosent exist for target path %s", targetPath)
			if err := os.MkdirAll(targetPath, 0750); err != nil {
				log.Errorf("Error while making target path, %s", targetPath)
				return status.Error(codes.Internal, err.Error())
			}
			notMnt = true
		} else {
			return status.Error(codes.Internal, err.Error())
		}
	}

	// notMnt with not is mounted
	if !notMnt {
		// Run stale mount check
		stale, err := IsMountStale(targetPath)
		if err != nil {
			return status.Errorf(codes.Internal, "failed mount health check: %v", err)
		}
		if stale {
			log.Warnf("Stale/hung mount detected at %s. Attempting lazy unmount...", targetPath)

			output, err := common.ExecCommand("umount", "-l", targetPath)
			if err != nil {
				log.Errorf("Lazy unmount failed at %s: %v, output: %s", targetPath, err, string(output))
				return status.Errorf(codes.Internal, "failed to clean up stale mount at %s", targetPath)
			}

			// Re-check mount state
			notMnt, err = common.SafeIsLikelyNotMountPoint(targetPath)
			if err != nil {
				log.Errorf("Post-unmount check failed at %s: %v", targetPath, err)
				return status.Errorf(codes.Internal, "post-unmount validation failed")
			}
			if !notMnt {
				log.Errorf("Mount point %s still appears mounted after lazy unmount", targetPath)
				return status.Errorf(codes.Internal, "stale mount at %s could not be removed", targetPath)
			}

			log.Infof("Successfully cleaned up stale mount at %s", targetPath)
		}
		log.Debugf("Volume already published at %s", targetPath)
		return nil
	}

	if readOnly {
		mountFlags = append(mountFlags, "ro")
	}

	// Bind mount from staging to target
	sourcePath := filepath.Join("/mnt/hammerspace_root", volumeId)
	if !strings.HasSuffix(sourcePath, "/") {
		sourcePath += "/"
	}

	out, err := common.ExecCommand("mount", "--bind", sourcePath, targetPath)
	if err != nil {

		return err
	}
	log.Debugf("Bind mount is success, from source (%s) to target (%s)", sourcePath, targetPath)

	notMount, statErr := common.SafeIsLikelyNotMountPoint(targetPath)
	log.Debugf("Checking mount is point target (%s).", targetPath)
	if statErr != nil {
		log.Warnf("Could not determine mount status of %s: %v", targetPath, statErr)
	} else if notMount {
		log.Warnf("Bind mount from %s to %s appears to have failed (target is not a mount point)", sourcePath, targetPath)
	} else {
		log.Infof("Bind mount succeeded from %s to %s. Output %v", sourcePath, targetPath, out)
		return nil
	}

	return err

	// Check to add fallback with regular mount
	// log.Infof("Bind mount didnt work for %s -> %s, trying to mount share directly", volumeId, targetPath)
	// err = d.MountShareAtBestDataportal(ctx, volumeId, targetPath, mountFlags, fqdn)
	// return err
}

// Check base pv exist as backingShareName and create path with backingShareName/exportPath attach to target path
func (d *CSIDriver) publishShareBackedDirBasedVolume(ctx context.Context, backingShareName, exportPath, targetPath, stagingTargetPath, fsType string, mountFlags []string, readOnly bool, fqdn string) error {
	defer d.releaseVolumeLock(backingShareName)
	d.getVolumeLock(backingShareName)

	notMnt, err := common.SafeIsLikelyNotMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			notMnt = true
		} else {
			// Any other error (e.g. permission denied)
			return status.Error(codes.Internal, err.Error())
		}
	}

	if !notMnt {
		stale, err := IsMountStale(targetPath)
		if err != nil {
			return status.Errorf(codes.Internal, "failed mount health check: %v", err)
		}
		if stale {
			log.Warnf("Stale/hung mount detected at %s. Attempting lazy unmount...", targetPath)
			unmountCmd := exec.Command("umount", "-l", targetPath)
			output, err := unmountCmd.CombinedOutput()
			if err != nil {
				log.Errorf("Lazy unmount failed at %s: %v, output: %s", targetPath, err, string(output))
				return status.Errorf(codes.Internal, "failed to clean up stale mount at %s", targetPath)
			}

			notMnt, err = common.SafeIsLikelyNotMountPoint(targetPath)
			if err != nil {
				log.Errorf("Post-unmount check failed at %s: %v", targetPath, err)
				return status.Errorf(codes.Internal, "post-unmount validation failed")
			}
			if !notMnt {
				log.Errorf("Mount point %s still appears mounted after lazy unmount", targetPath)
				return status.Errorf(codes.Internal, "stale mount at %s could not be removed", targetPath)
			}
			log.Infof("Successfully cleaned up stale mount at %s", targetPath)
		}
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
	log.Infof("Mounting file-backed volume at %s", targetPath)

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

	notMnt, err := common.SafeIsLikelyNotMountPoint(targetPath)
	if err != nil {
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
			notMnt = true
		} else {
			// Any other error (e.g. permission denied)
			return status.Error(codes.Internal, err.Error())
		}
	}

	if !notMnt {
		stale, err := IsMountStale(targetPath)
		if err != nil {
			return status.Errorf(codes.Internal, "failed mount health check: %v", err)
		}
		if stale {
			log.Warnf("Stale/hung mount detected at %s. Attempting lazy unmount...", targetPath)
			unmountCmd := exec.Command("umount", "-l", targetPath)
			output, err := unmountCmd.CombinedOutput()
			if err != nil {
				log.Errorf("Lazy unmount failed at %s: %v, output: %s", targetPath, err, string(output))
				return status.Errorf(codes.Internal, "failed to clean up stale mount at %s", targetPath)
			}

			notMnt, err = common.SafeIsLikelyNotMountPoint(targetPath)
			if err != nil {
				log.Errorf("Post-unmount check failed at %s: %v", targetPath, err)
				return status.Errorf(codes.Internal, "post-unmount validation failed")
			}
			if !notMnt {
				log.Errorf("Mount point %s still appears mounted after lazy unmount", targetPath)
				return status.Errorf(codes.Internal, "stale mount at %s could not be removed", targetPath)
			}
			log.Infof("Successfully cleaned up stale mount at %s", targetPath)
		}
		log.Debugf("Volume already published at %s", targetPath)
		return nil
	}

	hsVolume := &common.HSVolume{
		FQDN:               fqdn,
		FSType:             fsType,
		ClientMountOptions: mountFlags,
	}
	log.Infof("check publish file backed volume %v", hsVolume)

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
	output, err := common.ExecCommand("umount", "-f", targetPath)
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
	output, err = exec.Command("losetup", "-d", lodevice).CombinedOutput()
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
