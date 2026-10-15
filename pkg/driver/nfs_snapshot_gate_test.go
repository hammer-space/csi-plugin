package driver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newSnapshotGateDriver serves one share-backed volume ("/pvc-share") and one
// file-backed volume ("/backing/pvc-file"), so behaviour can be checked on both
// sides of the share-vs-file distinction the restore gate keys on.
func newSnapshotGateDriver(t *testing.T) (*CSIDriver, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(client.BasePath+"/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc(client.BasePath+"/shares/pvc-share", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"pvc-share","path":"/pvc-share"}`)
	})
	mux.HandleFunc(client.BasePath+"/shares/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	// Taking a SHARE snapshot is supported and must work.
	mux.HandleFunc(client.BasePath+"/share-snapshots/snapshot-create/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"2026-09-23T08-25-16-0644-0"`)
	})
	// A FILE snapshot is supported too.
	mux.HandleFunc(client.BasePath+"/file-snapshots/create", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `["/backing/.fsnapshot/pvc-file/2026-09-23T08-25-16-0644-0"]`)
	})
	server := httptest.NewServer(mux)
	hs, err := client.NewHammerspaceClient(server.URL, "test", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	return &CSIDriver{
		hsclient:      hs,
		volumeLocks:   make(map[string]*keyLock),
		snapshotLocks: make(map[string]*keyLock),
	}, server.Close
}

// Snapshotting a share-backed (native NFS) volume IS supported. Only restoring
// one is not, so CreateSnapshot must not be gated -- the snapshot is still
// useful for backup, and it must remain creatable so it can also be listed and
// deleted.
func TestCreateSnapshotAllowsShareBackedVolumes(t *testing.T) {
	driver, closeServer := newSnapshotGateDriver(t)
	defer closeServer()

	resp, err := driver.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name:           "snap-of-nfs",
		SourceVolumeId: "/pvc-share",
	})
	if err != nil {
		t.Fatalf("snapshotting a share-backed volume must be supported, got %v", err)
	}
	expected := "2026-09-23T08-25-16-0644-0|/pvc-share"
	if resp.Snapshot.SnapshotId != expected {
		t.Fatalf("snapshot ID = %q, want %q", resp.Snapshot.SnapshotId, expected)
	}
	if !resp.Snapshot.ReadyToUse {
		t.Fatal("a created snapshot must be reported ready")
	}
}

// File-backed snapshots are supported in both directions.
func TestCreateSnapshotAllowsFileBackedVolumes(t *testing.T) {
	driver, closeServer := newSnapshotGateDriver(t)
	defer closeServer()

	resp, err := driver.CreateSnapshot(context.Background(), &csi.CreateSnapshotRequest{
		Name:           "snap-of-file",
		SourceVolumeId: "/backing/pvc-file",
	})
	if err != nil {
		t.Fatalf("file-backed snapshot must be supported, got %v", err)
	}
	expected := "/backing/.fsnapshot/pvc-file/2026-09-23T08-25-16-0644-0|/backing/pvc-file"
	if resp.Snapshot.SnapshotId != expected {
		t.Fatalf("snapshot ID = %q, want %q", resp.Snapshot.SnapshotId, expected)
	}
}

// RESTORING into a share-backed NFS volume is the unsupported direction: the
// clone lands inside the source share, so the restored volume would silently
// report the source's capacity instead of the requested size. It must be
// declined with Unimplemented rather than half-served.
func TestCreateVolumeDeclinesShareBackedSnapshotRestore(t *testing.T) {
	if shareBackedSnapshotRestoreSupported {
		t.Skip("share-backed restore enabled; gate no longer applies")
	}
	driver, closeServer := newSnapshotGateDriver(t)
	defer closeServer()

	_, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:          "restored-nfs",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1073741824},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs"}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		}},
		Parameters: map[string]string{"fsType": "nfs"},
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: "snap-1|/pvc-share"},
			},
		},
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented for a share-backed restore, got %v", err)
	}
}
