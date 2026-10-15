package driver

import (
	"context"
	"os"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Only volumes staged by a driver version that still mounted the node-wide
// root export leave a marker behind, so only their unstage may take the root
// mount lock. Everything else must stage and unstage without contending on it.
// The lock is held externally throughout; an RPC that tried to take it would
// fail with Aborted on the already-cancelled context.
func TestRootMountLockTakenOnlyForLegacyMarkers(t *testing.T) {
	origMarkers := common.BaseVolumeMarkerSourcePath
	common.BaseVolumeMarkerSourcePath = t.TempDir()
	defer func() { common.BaseVolumeMarkerSourcePath = origMarkers }()

	d := &CSIDriver{volumeLocks: make(map[string]*keyLock)}
	unlock, err := d.acquireRootMountLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	staging := t.TempDir()

	// A file-backed volume has nothing to stage: its backing share is mounted
	// at publish time.
	capability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"}},
	}
	if _, err := d.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
		VolumeId: "/backing/file", StagingTargetPath: staging, VolumeCapability: capability,
		VolumeContext: map[string]string{"mountBackingShareName": "backing", "fsType": "ext4"},
	}); err != nil {
		t.Fatalf("staging a file-backed volume should be a no-op: %v", err)
	}
	if entries, _ := os.ReadDir(common.BaseVolumeMarkerSourcePath); len(entries) != 0 {
		t.Fatalf("staging must not write root-export markers, found %d", len(entries))
	}

	if _, err := d.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{
		VolumeId: "/backing/file", StagingTargetPath: staging,
	}); err != nil {
		t.Fatalf("unstaging a volume with no legacy marker should not need the root lock: %v", err)
	}

	legacy := "/legacy-share"
	if err := os.WriteFile(GetHashedMarkerPath(common.BaseVolumeMarkerSourcePath, legacy), nil, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = d.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{
		VolumeId: legacy, StagingTargetPath: staging,
	})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("unstaging a legacy-staged volume must take the root mount lock: %v", err)
	}
}

// The root mount lock is keyed by a host path, so it must not collide with a
// CSI volume ID, which is always a Hammerspace share or file path.
func TestRootMountLockKeyIsNotAVolumeID(t *testing.T) {
	d := &CSIDriver{volumeLocks: make(map[string]*keyLock)}
	unlock, err := d.acquireRootMountLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// A real volume must still be lockable while the root lock is held.
	volumeUnlock, err := d.acquireVolumeLock(context.Background(), "/"+common.SharePathPrefix+"pvc-test")
	if err != nil {
		t.Fatalf("a volume lock should not contend with the root mount lock: %v", err)
	}
	volumeUnlock()
}
