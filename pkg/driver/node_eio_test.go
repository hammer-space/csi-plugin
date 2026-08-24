package driver

import (
	"context"
	"os"
	"syscall"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeGetVolumeStatsReportsEIOAsUnavailable(t *testing.T) {
	originalStat := statVolumePath
	t.Cleanup(func() { statVolumePath = originalStat })
	statVolumePath = func(path string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: path, Err: syscall.EIO}
	}

	d := &CSIDriver{}
	_, err := d.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{
		VolumeId:   "/csi/xfs-pvc-test",
		VolumePath: "/var/lib/kubelet/pods/test/mount",
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("NodeGetVolumeStats EIO code = %s, want %s (err: %v)", status.Code(err), codes.Unavailable, err)
	}
}

func TestNodeGetVolumeStatsStillReportsMissingPathAsNotFound(t *testing.T) {
	originalStat := statVolumePath
	t.Cleanup(func() { statVolumePath = originalStat })
	statVolumePath = func(path string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: path, Err: syscall.ENOENT}
	}

	d := &CSIDriver{}
	_, err := d.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{
		VolumeId:   "/csi/xfs-pvc-test",
		VolumePath: "/var/lib/kubelet/pods/test/mount",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("NodeGetVolumeStats missing-path code = %s, want %s (err: %v)", status.Code(err), codes.NotFound, err)
	}
}

func TestNodeUnpublishVolumeForceUnmountsEIOPath(t *testing.T) {
	originalLstat := lstatTargetPath
	originalForceUnmount := forceUnmountTarget
	t.Cleanup(func() {
		lstatTargetPath = originalLstat
		forceUnmountTarget = originalForceUnmount
	})

	targetPath := "/var/lib/kubelet/pods/test/mount"
	lstatTargetPath = func(path string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: syscall.EIO}
	}
	forceUnmountCalls := 0
	forceUnmountTarget = func(path string) error {
		forceUnmountCalls++
		if path != targetPath {
			t.Fatalf("force-unmount path = %q, want %q", path, targetPath)
		}
		return nil
	}

	d := &CSIDriver{volumeLocks: make(map[string]*keyLock)}
	_, err := d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{
		VolumeId:   "/csi/xfs-pvc-test",
		TargetPath: targetPath,
	})
	if err != nil {
		t.Fatalf("NodeUnpublishVolume returned error for recoverable EIO: %v", err)
	}
	if forceUnmountCalls != 1 {
		t.Fatalf("force-unmount calls = %d, want 1", forceUnmountCalls)
	}
}
