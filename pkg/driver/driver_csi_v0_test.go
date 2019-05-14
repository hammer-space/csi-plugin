package driver

import (
    "fmt"
    csi_v0 "github.com/ameade/spec/lib/go/csi/v0"
    "github.com/container-storage-interface/spec/lib/go/csi"
    "github.com/hammer-space/csi-plugin/pkg/common"
    "reflect"
    "strings"
    "testing"
)

func TestConvertVolumeCapablityfromv0tov1(t *testing.T) {

    // Test basic conversion
    capv0 := &csi_v0.VolumeCapability{
        AccessType: &csi_v0.VolumeCapability_Mount{
            Mount: &csi_v0.VolumeCapability_MountVolume{
                FsType: "NFS",
                MountFlags: []string{"nfsvers=4.2"},
            },
        },
        AccessMode: &csi_v0.VolumeCapability_AccessMode{
            Mode: csi_v0.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
        },
    }

    capv1 := &csi.VolumeCapability{
        AccessType: &csi.VolumeCapability_Mount{
            Mount: &csi.VolumeCapability_MountVolume{
                FsType: "NFS",
                MountFlags: []string{"nfsvers=4.2"},
            },
        },
        AccessMode: &csi.VolumeCapability_AccessMode{
            Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
        },
    }

    actualcpv1, err := ConvertVolumeCapabilityFromv0Tov1(capv0)
    if err != nil {
        t.Logf("unexpected error")
        t.FailNow()
    }

    if !reflect.DeepEqual(actualcpv1, capv1) {
        t.Logf("Expected: %v", capv1)
        t.Logf("Actual: %v", actualcpv1)
        t.FailNow()
    }

    // Test that Raw volumes are not supported
    capv0 = &csi_v0.VolumeCapability{
        AccessType: &csi_v0.VolumeCapability_Block{
            Block: &csi_v0.VolumeCapability_BlockVolume{},
        },
        AccessMode: &csi_v0.VolumeCapability_AccessMode{
            Mode: csi_v0.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
        },
    }

    _, err = ConvertVolumeCapabilityFromv0Tov1(capv0)
    if err == nil {
        t.Logf("expected error")
        t.FailNow()
    } else {
        errString := fmt.Sprintf("%s", err)
        if !strings.Contains(errString, common.BlockVolumesUnsupported) {
            t.Logf("unexpected error, %s", err)
            t.FailNow()
        }
    }
}
