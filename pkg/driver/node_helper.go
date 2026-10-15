package driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"context"

	"github.com/hammer-space/csi-plugin/pkg/common"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mount operations are variables so the private-bind sequence can be tested
// without requiring CAP_SYS_ADMIN.
var (
	bindMountDevice       = common.BindMountDevice
	remountBindReadOnly   = common.RemountBindReadOnly
	unmountFilesystem     = common.UnmountFilesystem
	mountFilesystem       = common.MountFilesystem
	reconcileFilesystem   = common.ReconcileFilesystemToBackingFile
	filesystemNeedsGrowth = common.FilesystemNeedsGrowth
	isMountPoint          = common.SafeIsMountPoint
	getNFSExports         = common.GetNFSExports
	stageNFS              = (*CSIDriver).stageNFSVolume
)

// growFilesystemPrivatelyIfNeeded gives the driver a private writable
// mount on which it can finish growing a restored filesystem before exposing
// that filesystem to any workload.
//
// The writable mount is taken only when the filesystem is actually smaller
// than its backing file. A ReadOnlyMany volume may already be mounted
// read-only by other pods, and mounting ext4/xfs writable beside those mounts
// would write to a filesystem they are reading.
func growFilesystemPrivatelyIfNeeded(ctx context.Context, backingFile, fsType string) error {
	grow, err := filesystemNeedsGrowth(backingFile, fsType)
	if err != nil {
		return fmt.Errorf("check filesystem size: %w", err)
	}
	if !grow {
		return nil
	}
	log.Infof("growing restored filesystem in %s through a private provisioning mount", backingFile)
	temporary, err := os.MkdirTemp(common.ShareStagingDir, ".hscsi-grow-")
	if err != nil {
		return fmt.Errorf("create temporary filesystem mount: %w", err)
	}
	mounted := false
	defer func() {
		if mounted {
			if cleanupErr := unmountFilesystem(ctx, temporary); cleanupErr != nil {
				log.Errorf("failed to clean temporary filesystem mount %s: %v", temporary, cleanupErr)
			}
		}
		if removeErr := os.Remove(temporary); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Errorf("failed to remove temporary filesystem mount %s: %v", temporary, removeErr)
		}
	}()
	if err := mountFilesystem(backingFile, temporary, fsType, nil); err != nil {
		return fmt.Errorf("mount filesystem temporarily for growth: %w", err)
	}
	mounted = true
	if err := reconcileFilesystem(temporary, backingFile, fsType); err != nil {
		return fmt.Errorf("reconcile restored filesystem during provisioning: %w", err)
	}
	if err := unmountFilesystem(ctx, temporary); err != nil {
		return fmt.Errorf("unmount temporary filesystem after growth: %w", err)
	}
	mounted = false
	return nil
}

// bindMountReadOnly binds source to target read-only without changing the
// mount that contains source, which a staged volume shares with every other pod
// publishing it.
//
// The read-only flag is set on a private bind that then becomes the source of
// the final bind, because a flag-only remount of an already propagated pod
// target does not cross the container/host mount-namespace boundary. The final
// bind is a new topology event and carries the private source's flags with it.
func bindMountReadOnly(ctx context.Context, source, target string) error {
	private, err := os.MkdirTemp(common.ShareStagingDir, ".hscsi-bind-")
	if err != nil {
		return fmt.Errorf("create private bind mount: %w", err)
	}
	privateMounted := false
	defer func() {
		if privateMounted {
			if cleanupErr := unmountFilesystem(ctx, private); cleanupErr != nil {
				log.Errorf("failed to clean private bind mount %s: %v", private, cleanupErr)
			}
		}
		if removeErr := os.Remove(private); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Errorf("failed to remove private bind directory %s: %v", private, removeErr)
		}
	}()

	if err := bindMountDevice(source, private); err != nil {
		return fmt.Errorf("create private bind from %s: %w", source, err)
	}
	privateMounted = true
	if err := remountBindReadOnly(private); err != nil {
		return fmt.Errorf("make private bind read-only: %w", err)
	}
	if err := bindMountDevice(private, target); err != nil {
		return err
	}
	if err := unmountFilesystem(ctx, private); err != nil {
		// Do not report a successful publish while leaking a propagated helper
		// mount. Roll back the final bind so kubelet can retry the whole sequence.
		if rollbackErr := unmountFilesystem(ctx, target); rollbackErr != nil {
			return fmt.Errorf("clean private bind mount: %v (rollback target failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("clean private bind mount: %w", err)
	}
	privateMounted = false
	return nil
}

// nestedNFSSubPath returns where a nested NFS volume lives inside its backing
// share. CreateVolume names such a volume "<backing share export path>/<name>",
// a plain directory inside the share rather than an export of its own.
func nestedNFSSubPath(volumeID, backingExportPath string) (string, error) {
	subPath := strings.TrimPrefix(volumeID, backingExportPath)
	if subPath == volumeID || !strings.HasPrefix(subPath, "/") || len(subPath) < 2 {
		return "", fmt.Errorf("volume %s is not a directory inside backing share %s", volumeID, backingExportPath)
	}
	return subPath, nil
}

// stageNFSVolume mounts a native or nested NFS volume at its CSI staging path.
func (d *CSIDriver) stageNFSVolume(ctx context.Context, volumeID, stagingTarget string, mountFlags []string, volumeContext map[string]string) error {
	if backingShareName := volumeContext["mountBackingShareName"]; backingShareName != "" {
		return d.stageNestedNFSVolume(ctx, backingShareName, volumeID, stagingTarget, mountFlags, volumeContext["fqdn"])
	}
	return d.MountShareAtBestDataportal(ctx, volumeID, stagingTarget, mountFlags, volumeContext["fqdn"])
}

// stageNestedNFSVolume mounts a nested NFS volume's own directory at the CSI
// staging path, with the volume's mount options, exactly as a native NFS volume
// is staged. It deliberately does not bind out of the node's shared backing
// share mount: per-volume options there would apply to every file-backed and
// block volume on the share.
func (d *CSIDriver) stageNestedNFSVolume(ctx context.Context, backingShareName, volumeID, stagingTarget string, mountFlags []string, fqdn string) error {
	backingShare, err := d.hsclient.GetShare(ctx, backingShareName)
	if err != nil {
		return status.Errorf(codes.Internal, "look up backing share %s: %v", backingShareName, err)
	}
	if backingShare == nil {
		return status.Errorf(codes.NotFound, "backing share %s not found", backingShareName)
	}
	subPath, err := nestedNFSSubPath(volumeID, backingShare.ExportPath)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return d.mountShareSubPathAtBestDataportal(ctx, backingShare.ExportPath, subPath, stagingTarget, nestedNFSMountFlags(mountFlags), fqdn)
}

// nestedNFSMountFlags adds nosharecache to a nested NFS volume's mount options.
//
// By default the kernel NFS client gives every mount of the same export the
// same superblock, so a nested volume's staging mount would share one with the
// backing share's own mount and with every other nested volume on that share.
// mountinfo then shows them all with the same device and root, which kubelet's
// GetDeviceMountRefs check reads as other references to the staging path: it
// refuses to unstage the volume for as long as any of them stays mounted.
// nosharecache gives each nested volume a superblock of its own.
func nestedNFSMountFlags(mountFlags []string) []string {
	flags := append([]string{}, mountFlags...)
	for _, flag := range flags {
		for _, option := range strings.Split(flag, ",") {
			if option == "nosharecache" || option == "sharecache" {
				return flags
			}
		}
	}
	return append(flags, "nosharecache")
}

// publishShareBackedVolume publishes a native or nested NFS volume. With a
// staging path it binds the volume's staged mount into the pod, read-only when
// the publish asks for it.
func (d *CSIDriver) publishShareBackedVolume(ctx context.Context, volumeId, stagingTarget, targetPath string, mountFlags []string, readOnly bool, volumeContext map[string]string) error {
	mounted, err := isMountPoint(targetPath)
	if err == nil && mounted {
		log.Debugf("Volume (%s) already published at %s; nothing to do", volumeId, targetPath)
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		if errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESTALE) {
			log.Warnf("native NFS target %s is stale; force-detaching before republish", targetPath)
			if unmountErr := forceUnmountTarget(targetPath); unmountErr != nil {
				return status.Errorf(codes.Internal, "clean stale NFS target %s: %v", targetPath, unmountErr)
			}
		} else {
			return status.Errorf(codes.Internal, "check NFS target %s: %v", targetPath, err)
		}
	}

	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return status.Errorf(codes.Internal, "create NFS target %s: %v", targetPath, err)
	}

	if stagingTarget != "" {
		// The share is mounted once at the CSI staging path. Publishing is a local
		// bind per pod, while kubelet creates any subPath binds beneath that.
		//
		// Never bind a staging path with nothing mounted on it. Binding it
		// anyway hands the pod an empty directory on the node's own disk: every
		// write succeeds, nothing reaches Hammerspace, and the data is lost when
		// the pod moves.
		//
		// An empty staging path is expected after an upgrade from a driver that
		// staged through the node-wide root export: kubelet treats the volume as
		// staged for as long as any pod on the node uses it and won't call
		// NodeStageVolume again, so a new pod would fail until every old pod had
		// left the node. Stage the volume here instead, as NodeStageVolume now
		// would. Old pods keep their binds from the root export, and
		// NodeUnstageVolume releases that once the volume leaves the node.
		staged, checkErr := isMountPoint(stagingTarget)
		if checkErr != nil {
			return status.Errorf(codes.Internal, "check staged NFS volume %s at %s: %v", volumeId, stagingTarget, checkErr)
		}
		if !staged {
			log.Warnf("NFS volume %s is not mounted at staging path %s; staging it before publishing", volumeId, stagingTarget)
			if err := stageNFS(d, ctx, volumeId, stagingTarget, mountFlags, volumeContext); err != nil {
				return err
			}
			if staged, checkErr = isMountPoint(stagingTarget); checkErr != nil || !staged {
				return status.Errorf(codes.FailedPrecondition, "NFS volume %s is not mounted at staging path %s; refusing to publish an unbacked directory", volumeId, stagingTarget)
			}
		}
		bind := func() error { return bindMountDevice(stagingTarget, targetPath) }
		if readOnly {
			bind = func() error { return bindMountReadOnly(ctx, stagingTarget, targetPath) }
		}
		if err := bind(); err != nil {
			return status.Errorf(codes.Internal, "bind staged NFS volume %s to %s: %v", stagingTarget, targetPath, err)
		}
		return nil
	}

	// Controller-side metadata operations do not have a CSI staging path.
	if readOnly {
		mountFlags = append(append([]string{}, mountFlags...), "ro")
	}
	return d.MountShareAtBestDataportal(ctx, volumeId, targetPath, mountFlags, volumeContext["fqdn"])
}

func (d *CSIDriver) publishFileBackedVolume(ctx context.Context, backingShareName, volumePath, targetPath, fsType string, mountFlags []string, readOnly bool, fqdn string) error {
	unlock, err := d.acquireVolumeLock(ctx, backingShareName)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return err
	}
	defer unlock()

	log.Debug("Received publish file-backed volume request")
	mounted, err := common.SafeIsMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			// A missing kubelet target is expected on the first publish. Create it
			// with the shape required by the requested volume type.
			log.Debugf("Publish target %s does not exist; creating it", targetPath)
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
		} else if errors.Is(err, syscall.EIO) {
			// A file-backed filesystem can remain mounted after XFS shuts itself
			// down. Do not report it as healthy or inherit it on a retry.
			log.Warnf("publish target %s returned EIO; force-detaching shut-down filesystem", targetPath)
			if unmountErr := forceUnmountTarget(targetPath); unmountErr != nil {
				return status.Errorf(codes.Internal,
					"failed to clean up publish target after I/O error: %v", unmountErr)
			}
			if fsType != "" {
				if mkdirErr := os.MkdirAll(targetPath, 0755); mkdirErr != nil {
					return status.Errorf(codes.Internal, "failed to recreate publish target after I/O error: %v", mkdirErr)
				}
			} else {
				parentDir := filepath.Dir(targetPath)
				if mkdirErr := os.MkdirAll(parentDir, 0755); mkdirErr != nil {
					return status.Error(codes.Internal, mkdirErr.Error())
				}
				f, createErr := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL, 0644)
				if createErr != nil {
					return status.Errorf(codes.Internal, "failed to recreate block publish target after I/O error: %v", createErr)
				}
				if closeErr := f.Close(); closeErr != nil {
					return status.Error(codes.Internal, closeErr.Error())
				}
			}
			mounted = false
		} else {
			// Any other error (for example permission denied) is actionable.
			log.Errorf("Failed to check whether publish target %s is a mount point: %v", targetPath, err)
			return status.Errorf(codes.Internal, "failed to inspect publish target %s: %v", targetPath, err)
		}
	}

	filePath := common.ShareStagingDir + volumePath

	if mounted {
		log.Debugf("Volume already published at %s", targetPath)
	} else {
		hsVolume := &common.HSVolume{
			FQDN:       fqdn,
			FSType:     fsType,
			MountFlags: mountFlags,
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

		if fsType == "" {
			deviceStr, err := AttachLoopDeviceWithRetry(filePath, readOnly)
			if err != nil {
				log.Errorf("failed to attach loop device: %v", err)
				CleanupLoopDevice(ctx, deviceStr)
				d.UnmountBackingShareIfUnused(ctx, backingShareName)
				return status.Errorf(codes.Internal, common.LoopDeviceAttachFailed, deviceStr, filePath)
			}
			log.Infof("File %s attached to %s", filePath, deviceStr)

			if err := common.BindMountDevice(deviceStr, targetPath); err != nil {
				log.Errorf("bind mount failed for %s: %v", deviceStr, err)
				CleanupLoopDevice(ctx, deviceStr)
				d.UnmountBackingShareIfUnused(ctx, backingShareName)
				return err
			}
		} else {
			// StorageClass mountOptions are used for the backing NFS share mount.
			// Do not pass them to the local ext4/xfs mount of the backing file.
			var filesystemMountFlags []string
			if readOnly {
				// Provisioning grows restored filesystems before CreateVolume
				// returns. Never introduce a writable mount from a read-only
				// publish: another node may already be reading this filesystem.
				needsGrowth, growthErr := filesystemNeedsGrowth(filePath, fsType)
				if growthErr != nil {
					d.UnmountBackingShareIfUnused(ctx, backingShareName)
					return status.Errorf(codes.Internal, "verify filesystem before read-only publish: %v", growthErr)
				}
				if needsGrowth {
					d.UnmountBackingShareIfUnused(ctx, backingShareName)
					return status.Errorf(codes.FailedPrecondition, "filesystem in %s has not been grown to its provisioned capacity; publish it writable once or retry CreateVolume reconciliation", filePath)
				}
				filesystemMountFlags = append(filesystemMountFlags, "ro")
			}
			if err := mountFilesystem(filePath, targetPath, fsType, filesystemMountFlags); err != nil {
				d.UnmountBackingShareIfUnused(ctx, backingShareName)
				return err
			}
		}
	}

	// Writable publish retains an idempotent repair path for older volumes or
	// provisioning attempts interrupted before filesystem reconciliation.
	if fsType != "" && !readOnly {
		if err := reconcilePublishedFilesystem(ctx, targetPath, filePath, fsType, !mounted); err != nil {
			d.UnmountBackingShareIfUnused(ctx, backingShareName)
			return err
		}
	}
	return nil
}

// reconcilePublishedFilesystem grows the filesystem mounted at targetPath to its
// backing file. freshMount says whether this publish just mounted it.
func reconcilePublishedFilesystem(ctx context.Context, targetPath, filePath, fsType string, freshMount bool) error {
	err := reconcileFilesystem(targetPath, filePath, fsType)
	if err == nil {
		return nil
	}
	if !freshMount {
		// Already mounted, i.e. a republish of a healthy volume
		// (requiresRepublish drives these periodically). The reconcile is a
		// no-op in the steady state, so a transient loop-device lookup
		// failure here must not tear down a working volume -- warn and let
		// the next republish retry.
		log.Warnf("could not reconcile filesystem size at %s on republish: %v", targetPath, err)
		return nil
	}
	// Fresh mount: this is where a restored volume gets the capacity its PVC
	// asked for, so a failure here must surface. Undo the mount, or kubelet's
	// retry finds targetPath mounted, takes the republish branch above and
	// reports success on an undersized filesystem.
	log.Errorf("failed to reconcile filesystem size at %s: %v", targetPath, err)
	if unmountErr := unmountFilesystem(ctx, targetPath); unmountErr != nil {
		log.Errorf("failed to unmount %s after reconcile failure: %v", targetPath, unmountErr)
	}
	return status.Errorf(codes.Internal, "failed to reconcile filesystem size: %v", err)
}

// NodeUnpublishVolume
func (d *CSIDriver) unpublishFileBackedVolume(ctx context.Context, volumePath, targetPath string) error {
	ctx, span := tracer.Start(ctx, "unpublishFileBackedVolume", trace.WithAttributes(
		attribute.String("volume.path", volumePath),
		attribute.String("target.path", targetPath),
	))
	defer span.End()

	//determine backing share
	backingShareName := filepath.Dir(volumePath)

	unlock, err := d.acquireVolumeLock(ctx, backingShareName)
	if err != nil {
		// surfaces to kubelet instead of hanging forever
		return err
	}
	defer unlock()

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
