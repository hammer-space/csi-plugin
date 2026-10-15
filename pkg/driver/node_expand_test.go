package driver

import (
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeExpansionFilesystemTypeWithoutCapability(t *testing.T) {
	original := filesystemType
	defer func() { filesystemType = original }()

	filesystemType = func(backingFile string) (string, error) {
		if backingFile != "/tmp/share/volume" {
			t.Fatalf("backing file = %q", backingFile)
		}
		return "xfs", nil
	}

	got, err := nodeExpansionFilesystemType(&csi.NodeExpandVolumeRequest{
		VolumePath: "/var/lib/kubelet/plugins/kubernetes.io/csi/volume",
	}, "/tmp/share/volume")
	if err != nil {
		t.Fatalf("nodeExpansionFilesystemType returned error: %v", err)
	}
	if got != "xfs" {
		t.Fatalf("filesystem type = %q, want xfs", got)
	}
}

func TestNodeExpansionFilesystemTypeFromCapability(t *testing.T) {
	got, err := nodeExpansionFilesystemType(&csi.NodeExpandVolumeRequest{
		VolumePath: "/mnt/volume",
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{FsType: "xfs"},
		}},
	}, "/tmp/share/volume")
	if err != nil {
		t.Fatalf("nodeExpansionFilesystemType returned error: %v", err)
	}
	if got != "xfs" {
		t.Fatalf("filesystem type = %q, want xfs", got)
	}
}

func TestNodeExpansionFilesystemTypeRawBlock(t *testing.T) {
	got, err := nodeExpansionFilesystemType(&csi.NodeExpandVolumeRequest{
		VolumePath: "/dev/loop2",
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{
			Block: &csi.VolumeCapability_BlockVolume{},
		}},
	}, "/tmp/share/volume")
	if err != nil {
		t.Fatalf("nodeExpansionFilesystemType returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("filesystem type = %q, want empty for raw block", got)
	}
}

func TestNodeExpansionFilesystemTypeLookupError(t *testing.T) {
	original := filesystemType
	defer func() { filesystemType = original }()
	filesystemType = func(string) (string, error) { return "", errors.New("filesystem unavailable") }

	_, err := nodeExpansionFilesystemType(&csi.NodeExpandVolumeRequest{VolumePath: "/mnt/volume"}, "/tmp/share/volume")
	if status.Code(err) != codes.Internal {
		t.Fatalf("error code = %s, want %s (err=%v)", status.Code(err), codes.Internal, err)
	}
}
