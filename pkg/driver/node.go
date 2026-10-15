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
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/common"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Filesystem operations are variables so error paths that require kernel-level
// failures (notably EIO from a shut-down XFS mount) can be regression tested.
var (
	statVolumePath     = os.Stat
	statfsVolumePath   = syscall.Statfs
	lstatTargetPath    = os.Lstat
	forceUnmountTarget = common.ForceUnmountFilesystem
	filesystemType     = common.GetFilesystemType
	// volumeStatsTimeout bounds NodeGetVolumeStats' stat and statfs of the
	// volume path, which never return on a hard NFS mount whose server is gone.
	volumeStatsTimeout = 10 * time.Second
)

// maxVolumesPerNode reads MAX_VOLUMES_PER_NODE, the most volumes of this
// driver the scheduler may place on one node. Zero or unset means no limit.
func maxVolumesPerNode() int64 {
	value := os.Getenv("MAX_VOLUMES_PER_NODE")
	if value == "" {
		return 0
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		log.Warnf("Invalid MAX_VOLUMES_PER_NODE=%q; not limiting volumes per node", value)
		return 0
	}
	return n
}

// volumePathProbe is one stat, and for a non-block path one statfs, of a
// volume path.
type volumePathProbe struct {
	done      chan struct{}
	info      os.FileInfo
	statErr   error
	statfs    syscall.Statfs_t
	statfsErr error
}

var (
	volumePathProbesMu sync.Mutex
	volumePathProbes   = map[string]*volumePathProbe{}
)

// probeVolumePath stats path, and statfs's it unless it is a block device,
// giving up after timeout. A probe stuck on a dead NFS mount cannot be
// cancelled, so a later call for the same path waits on that probe instead of
// starting another: kubelet asks for stats every minute, and would otherwise
// leave one more stuck goroutine behind each time.
func probeVolumePath(path string, timeout time.Duration) (*volumePathProbe, bool) {
	volumePathProbesMu.Lock()
	probe, ok := volumePathProbes[path]
	if !ok {
		probe = &volumePathProbe{done: make(chan struct{})}
		volumePathProbes[path] = probe
		go func() {
			probe.info, probe.statErr = statVolumePath(path)
			if probe.statErr == nil && !IsBlockDevice(probe.info) {
				probe.statfsErr = statfsVolumePath(path, &probe.statfs)
			}
			volumePathProbesMu.Lock()
			delete(volumePathProbes, path)
			volumePathProbesMu.Unlock()
			close(probe.done)
		}()
	}
	volumePathProbesMu.Unlock()

	select {
	case <-probe.done:
		return probe, true
	case <-time.After(timeout):
		return nil, false
	}
}

func (d *CSIDriver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {

	// Determine if this node is a data portal
	dataPortals, err := d.hsclient.GetDataPortals(ctx, d.NodeID)
	if err != nil {
		log.WithFields(log.Fields{
			"Node ID": d.NodeID,
		}).Errorf("Could not list data-portals, %s", err.Error())
		return nil, err
	}

	log.WithFields(log.Fields{
		"dataPortals": dataPortals,
	}).Debugf("Recived data portal list")
	var isDataPortal bool
	for _, p := range dataPortals {
		if p.Node.Name == d.NodeID {
			isDataPortal = true
		}
	}

	csiNodeResponse := &csi.NodeGetInfoResponse{
		NodeId:            d.NodeID,
		MaxVolumesPerNode: maxVolumesPerNode(),
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				common.TopologyKeyDataPortal: strconv.FormatBool(isDataPortal),
			},
		},
	}
	log.WithFields(log.Fields{
		"csiNodeResponse": csiNodeResponse,
	}).Debugf("NodeGetInfo was successful.")

	return csiNodeResponse, nil
}

func (d *CSIDriver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}

	if req.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumePath)
	}

	// Check if path exists
	probe, answered := probeVolumePath(req.GetVolumePath(), volumeStatsTimeout)
	if !answered {
		log.Errorf("volume path %s did not answer stat within %s", req.GetVolumePath(), volumeStatsTimeout)
		return nil, status.Errorf(codes.Unavailable,
			"volume path %s did not answer within %s; its NFS server may be unreachable",
			req.GetVolumePath(), volumeStatsTimeout)
	}
	info, err := probe.info, probe.statErr
	if err != nil {
		if errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESTALE) {
			log.Errorf("volume path is inaccessible due to an I/O error: %s, err: %v", req.GetVolumePath(), err)
			return nil, status.Errorf(codes.Unavailable,
				"volume path %s is inaccessible due to an I/O error; filesystem teardown and retry are required",
				req.GetVolumePath())
		}
		log.Errorf("volume path not found: %s, err: %v", req.GetVolumePath(), err)
		return nil, status.Error(codes.NotFound, common.VolumeNotFound)
	}

	// If it's a block device, use Stat_t
	if IsBlockDevice(info) {
		log.Infof("Detected block volume: %s", req.GetVolumePath())

		file, err := os.Open(req.GetVolumePath())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to open block device: %v", err)
		}
		defer file.Close()

		// Get size using ioctl
		var size int64
		// BLKGETSIZE64: ioctl to get size in bytes for block devices
		const BLKGETSIZE64 = 0x80081272
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), BLKGETSIZE64, uintptr(unsafe.Pointer(&size)))
		if errno != 0 {
			return nil, status.Errorf(codes.Internal, "failed to get block size: %v", errno)
		}

		// CSI spec: for block volumes, 'used' and 'available' may be omitted.
		// Reporting Available = 0 can be misinterpreted as disk full.
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{
				{
					Unit:  csi.VolumeUsage_BYTES,
					Total: size,
					// Used and Available intentionally omitted per CSI spec
				},
				{
					Unit: csi.VolumeUsage_INODES,
					// All inode fields omitted (optional)
				},
			},
		}, nil
	}

	// Default: File or NFS mount — use Statfs
	st, err := probe.statfs, probe.statfsErr
	if err != nil {
		log.Errorf("statfs failed on %s: %v", req.GetVolumePath(), err)
		return nil, status.Error(codes.Internal, common.FileNotFound)
	}

	total := int64(st.Bsize) * int64(st.Blocks)
	available := int64(st.Bsize) * int64(st.Bavail)
	used := int64(st.Bsize) * int64(st.Blocks-st.Bfree)

	inodestotal := int64(st.Files)
	inodesavail := int64(st.Ffree)
	inodesused := inodestotal - inodesavail

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: available,
				Total:     total,
				Used:      used,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: inodesavail,
				Total:     inodestotal,
				Used:      inodesused,
			},
		},
	}, nil
}

// nodeVolumeFsType resolves a volume's filesystem type. NodeStageVolume and
// NodePublishVolume MUST agree on it: stage decides whether to mount the share
// at the staging path, and publish decides whether to bind from that path. When
// they disagreed, a pre-provisioned PV with no fsType was staged via the old
// root-export path (nothing mounted at the staging path) but published as
// native NFS, so the pod silently got an empty directory on the node's local
// disk instead of the Hammerspace share.
//
// Precedence: the capability's fsType, then the StorageClass-derived volume
// context, then "nfs" -- the driver's default for filesystem volumes. A raw
// block volume has no filesystem and resolves to "".
func nodeVolumeFsType(capability *csi.VolumeCapability, volumeContext map[string]string) string {
	mountCapability := capability.GetMount()
	if mountCapability == nil {
		return ""
	}
	if mountCapability.GetFsType() != "" {
		return mountCapability.GetFsType()
	}
	if fsType := volumeContext["fsType"]; fsType != "" {
		return fsType
	}
	return "nfs"
}

func (d *CSIDriver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (_ *csi.NodeStageVolumeResponse, err error) {
	ctx, span := tracer.Start(ctx, "Node/NodeStageVolume", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
		attribute.String("staging.target", req.GetStagingTargetPath()),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Node/NodeStageVolume")(&err)
	volumeID := req.GetVolumeId()
	volumeContext := req.GetVolumeContext()
	stagingTarget := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}
	if stagingTarget == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing")
	}
	if volumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "VolumeCapability must be provided")
	}

	mountFlags := []string{}
	if volumeCapability.GetMount() != nil {
		mountFlags = append(mountFlags, volumeCapability.GetMount().MountFlags...)
	}
	fsType := nodeVolumeFsType(volumeCapability, volumeContext)

	// An NFS volume is staged as one direct NFS mount per volume per node, with
	// the volume's own mount options. NodePublishVolume binds this mount into
	// each pod. This is the standard CSI topology and avoids both one NFS mount
	// per pod and the Hammerspace root-export junction that returns ESTALE for
	// file subPaths. A nested NFS volume (a directory inside a backing share)
	// gets its own mount of that directory, not a bind out of the node's shared
	// backing-share mount.
	if fsType == "nfs" {
		if err := stageNFS(d, ctx, volumeID, stagingTarget, mountFlags, volumeContext); err != nil {
			return nil, err
		}
		log.Infof("Staged NFS volume %s at %s", volumeID, stagingTarget)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// File-backed and block volumes have nothing to stage: NodePublishVolume
	// mounts their backing share (EnsureBackingShareMounted) and reaches the
	// backing file through it.
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *CSIDriver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (_ *csi.NodeUnstageVolumeResponse, err error) {
	ctx, span := tracer.Start(ctx, "Node/NodeUnstageVolume", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
		attribute.String("staging.target", req.GetStagingTargetPath()),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Node/NodeUnstageVolume")(&err)
	volumeID := req.GetVolumeId()
	stagingTarget := req.GetStagingTargetPath()

	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}
	if stagingTarget == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing")
	}

	// Native NFS volumes have a real mount at the CSI staging path. Other volume
	// types leave it empty.
	mounted, mountErr := isMountPoint(stagingTarget)
	if mountErr == nil && mounted {
		if err := unmountFilesystem(ctx, stagingTarget); err != nil {
			return nil, status.Errorf(codes.Internal, "unmount NFS staging target: %v", err)
		}
	} else if errors.Is(mountErr, syscall.EIO) || errors.Is(mountErr, syscall.ESTALE) {
		if err := forceUnmountTarget(stagingTarget); err != nil {
			return nil, status.Errorf(codes.Internal, "force-unmount stale NFS staging target: %v", err)
		}
	}

	// A volume staged by an older driver may also have been staged again by
	// NodePublishVolume after the upgrade, so release its root-export
	// reference whether or not the staging path was mounted.
	if err := d.releaseLegacyRootExport(ctx, volumeID); err != nil {
		return nil, err
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// releaseLegacyRootExport drops volumeID's reference to the node-wide root
// export. Driver versions before the staged-NFS design mounted that export at
// stage time and wrote a marker per staged volume. This driver no longer
// writes markers, so a marker here belongs to a volume staged by an older
// version: drop it, and unmount the root export once the last such volume is
// gone.
func (d *CSIDriver) releaseLegacyRootExport(ctx context.Context, volumeID string) error {
	marker := GetHashedMarkerPath(common.BaseVolumeMarkerSourcePath, volumeID)
	if _, statErr := os.Stat(marker); os.IsNotExist(statErr) {
		return nil
	}

	unlock, lockErr := d.acquireRootMountLock(ctx)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()

	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return status.Errorf(codes.Internal, "remove volume marker %s: %v", marker, err)
	}
	if !IsAnyVolumeStillMounted(common.BaseVolumeMarkerSourcePath) {
		if err := unmountFilesystem(ctx, common.BaseBackingShareMountPath); err != nil {
			return status.Errorf(codes.Internal, "unmount root export: %v", err)
		}
	}
	return nil
}

func (d *CSIDriver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (_ *csi.NodePublishVolumeResponse, err error) {
	ctx, span := tracer.Start(ctx, "Node/NodePublishVolume", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
		attribute.String("target.path", req.GetTargetPath()),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Node/NodePublishVolume")(&err)

	volume_id := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	volumeCapability := req.GetVolumeCapability()

	if volume_id == "" || targetPath == "" || volumeCapability == nil {
		log.WithFields(log.Fields{
			"volume_id":        volume_id,
			"targetPath":       targetPath,
			"volumeCapability": volumeCapability,
		}).Errorf("Invalid arguments")
		if volume_id == "" {
			return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
		}
		if targetPath == "" {
			return nil, status.Error(codes.InvalidArgument, common.EmptyTargetPath)
		}
		if volumeCapability == nil {
			return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, volume_id)
		}
	}

	unlock, err := d.acquireVolumeLock(ctx, volume_id)
	if err != nil {
		log.Errorf("Failed to acquire volume lock for volume %s: %v", volume_id, err)
		// surfaces to kubelet instead of hanging forever
		return nil, err
	}
	defer unlock()

	log.Infof("Attempting to publish volume %s at target path %s", volume_id, targetPath)

	var volumeContext = req.GetVolumeContext()
	var readOnly bool = req.GetReadonly()
	var mountFlags []string
	var fsType, backingShareName string

	switch volumeCapability.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		backingShareName = volumeContext["blockBackingShareName"]
	case *csi.VolumeCapability_Mount:
		backingShareName = volumeContext["mountBackingShareName"]
		fsType = nodeVolumeFsType(volumeCapability, volumeContext)
		mountFlags = append([]string{}, volumeCapability.GetMount().MountFlags...)
	default:
		return nil, status.Errorf(codes.InvalidArgument, common.NoCapabilitiesSupplied, volume_id)
	}

	// Native and nested NFS volumes are both staged as their own NFS mount, so
	// both publish the same way.
	if fsType == "nfs" {
		log.WithFields(log.Fields{
			"NFS Backing share": backingShareName,
			"Volume_id":         volume_id,
			"Traget Path":       targetPath,
		}).Info("Starting node publish volume for NFS volume.")
		err := d.publishShareBackedVolume(ctx, volume_id, req.GetStagingTargetPath(), targetPath, mountFlags, readOnly, volumeContext)
		if err != nil {
			return nil, err
		}
	} else {
		log.WithFields(log.Fields{
			"Backing share": backingShareName,
			"Volume_id":     volume_id,
			"Traget Path":   targetPath,
		}).Info("Starting node publish volume file backed.")
		err := d.publishFileBackedVolume(ctx, backingShareName, volume_id, targetPath, fsType, mountFlags, readOnly, volumeContext["fqdn"])
		if err != nil {
			log.Errorf("Error while running publishFileBackedVolume.")
			return nil, err
		}
	}
	log.WithFields(log.Fields{
		"volume_id":   volume_id,
		"target_path": targetPath,
	}).Info("Published volume")

	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *CSIDriver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (_ *csi.NodeUnpublishVolumeResponse, err error) {
	ctx, span := tracer.Start(ctx, "Node/NodeUnpublishVolume", trace.WithAttributes(
		attribute.String("volume.id", req.GetVolumeId()),
		attribute.String("target.path", req.GetTargetPath()),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "Node/NodeUnpublishVolume")(&err)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, common.EmptyVolumeId)
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, common.EmptyTargetPath)
	}

	log.Infof("Attempting to unpublish volume %s", req.GetVolumeId())

	unlock, err := d.acquireVolumeLock(ctx, req.VolumeId)
	if err != nil {
		log.Errorf("Failed to acquire volume lock for volume %s: %v", req.VolumeId, err)
		// surfaces to kubelet instead of hanging forever
		return nil, err
	}
	defer unlock()

	targetPath := req.GetTargetPath()
	fi, err := lstatTargetPath(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Infof("target path does not exist on this host: %s", targetPath)
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		if errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESTALE) {
			log.Warnf("target path %s is not stat-able (%v); force-detaching filesystem", targetPath, err)
			if unmountErr := forceUnmountTarget(targetPath); unmountErr != nil {
				return nil, status.Errorf(codes.Internal,
					"failed to clean up target path after I/O error: %v", unmountErr)
			}
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "error stating target path: %v", err)
	}

	// Resolve symlink before checking type
	if fi.Mode()&os.ModeSymlink != 0 {
		resolvedPath, err := filepath.EvalSymlinks(targetPath)
		if err != nil {
			log.Warnf("Broken symlink at %s: %v", targetPath, err)
			// remove the symlink path
			if rmErr := os.Remove(targetPath); rmErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to remove broken symlink: %v", rmErr)
			}
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}

		log.Infof("Resolved symlink targetPath=%s → %s", req.GetTargetPath(), resolvedPath)
		targetPath = resolvedPath
		fi, err = os.Stat(targetPath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to stat resolved path: %v", err)
		}
	}

	switch mode := fi.Mode(); {
	case IsBlockDevice(fi): // block device
		log.Infof("Detected block device at target path %s", targetPath)
		if err := d.unpublishFileBackedVolume(ctx, req.GetVolumeId(), targetPath); err != nil {
			return nil, err
		}
	case mode.IsDir(): // directory for mount volumes
		log.Infof("Detected directory mount at target path %s", targetPath)
		if err := common.UnmountFilesystem(ctx, targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	default:
		// Unknown file type, attempt cleanup
		log.Warnf("Target path %s exists but is not a block device nor directory. Removing...", targetPath)
		if err := os.Remove(targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to remove unexpected target path: %v", err)
		}
	}
	log.WithFields(log.Fields{
		"volume_id":   req.GetVolumeId(),
		"target_path": req.GetTargetPath(),
	}).Info("Unpublished volume")

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *CSIDriver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_UNKNOWN,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
					},
				},
			},
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (d *CSIDriver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {

	var requestedSize int64
	if req.GetCapacityRange().GetLimitBytes() != 0 {
		requestedSize = req.GetCapacityRange().GetLimitBytes()
	} else {
		requestedSize = req.GetCapacityRange().GetRequiredBytes()
	}

	// Find Share
	fileBacked := false

	volumeName := GetVolumeNameFromPath(req.GetVolumeId())
	share, _ := d.hsclient.GetShare(ctx, volumeName)
	if share != nil {
		if isMounted := common.IsShareMounted(share.ExportPath); !isMounted {
			return nil, status.Error(codes.FailedPrecondition, common.ShareNotMounted)
		}
	} else {
		fileBacked = true
	}

	//  Check if the specified backing share or file exists
	if share == nil {
		backingFileExists, err := d.hsclient.DoesFileExist(ctx, req.GetVolumeId())
		if err != nil {
			log.Error(err)
		}
		if !backingFileExists {
			return nil, status.Error(codes.InvalidArgument, common.VolumeNotFound)
		} else {
			fileBacked = true
		}
	}
	if fileBacked {
		// Ensure it's file-backed, otherwise no-op
		// Resize device
		backingFile := common.ShareStagingDir + req.GetVolumeId()
		err := common.ExpandDeviceFileSize(backingFile, requestedSize)
		if err != nil {
			return nil, err
		}
		fsType, err := nodeExpansionFilesystemType(req, backingFile)
		if err != nil {
			return nil, err
		}
		if fsType != "" {
			// xfs_growfs takes the mount point, but resize2fs needs the
			// underlying loop device, not a directory.
			err = common.ExpandMountedFilesystem(req.GetVolumePath(), backingFile, fsType)
			if err != nil {
				return nil, err
			}
		}
		log.WithFields(log.Fields{
			"volume_id":      req.GetVolumeId(),
			"capacity_bytes": requestedSize,
		}).Info("Expanded volume on node")
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: requestedSize,
		}, nil
	} else {
		return nil, nil
	}
}

// nodeExpansionFilesystemType determines whether NodeExpandVolume must grow a
// filesystem and, if so, which one. volume_capability is optional in the CSI
// specification, so kubelet may provide only volume_path. In that case inspect
// the path and loop device rather than silently treating the request as a raw
// block expansion.
func nodeExpansionFilesystemType(req *csi.NodeExpandVolumeRequest, backingFile string) (string, error) {
	if mountCapability := req.GetVolumeCapability().GetMount(); mountCapability != nil {
		if req.GetVolumePath() == "" {
			return "", status.Error(codes.InvalidArgument, common.EmptyVolumePath)
		}
		if mountCapability.GetFsType() != "" {
			return mountCapability.GetFsType(), nil
		}
	}
	if _, isBlock := req.GetVolumeCapability().GetAccessType().(*csi.VolumeCapability_Block); isBlock {
		return "", nil
	}
	if req.GetVolumePath() == "" {
		return "", status.Error(codes.InvalidArgument, common.EmptyVolumePath)
	}

	fsType, err := filesystemType(backingFile)
	if err != nil {
		return "", status.Errorf(codes.Internal, "could not determine filesystem type at %q: %v", req.GetVolumePath(), err)
	}
	return fsType, nil
}
