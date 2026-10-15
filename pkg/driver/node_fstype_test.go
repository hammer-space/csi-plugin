package driver

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

// NodeStageVolume and NodePublishVolume both resolve fsType through this one
// function. When they resolved it differently, a pre-provisioned PV with no
// fsType was staged without mounting anything but published as native NFS, and
// the pod silently wrote to the node's local disk.
func TestNodeVolumeFsType(t *testing.T) {
	mount := func(fs string) *csi.VolumeCapability {
		return &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fs}}}
	}
	block := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}

	for _, tc := range []struct {
		name string
		cap  *csi.VolumeCapability
		ctx  map[string]string
		want string
	}{
		{"pre-provisioned PV, no fsType anywhere -> nfs", mount(""), nil, "nfs"},
		{"StorageClass fsType from volume context", mount(""), map[string]string{"fsType": "ext4"}, "ext4"},
		{"capability fsType wins", mount("xfs"), map[string]string{"fsType": "ext4"}, "xfs"},
		{"raw block has no filesystem", block, map[string]string{"fsType": "ext4"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeVolumeFsType(tc.cap, tc.ctx); got != tc.want {
				t.Fatalf("nodeVolumeFsType = %q, want %q", got, tc.want)
			}
		})
	}
}
