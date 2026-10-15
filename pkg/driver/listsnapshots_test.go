package driver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/client"
)

// newListSnapshotsDriver wires a CSIDriver to a fake Anvil exposing one
// share-backed volume ("/pvc-share" with snapshot "snap-1") and one
// file-backed volume ("/backing/pvc-file" with a snapshot under .fsnapshot).
func newListSnapshotsDriver(t *testing.T) (*CSIDriver, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(client.BasePath+"/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc(client.BasePath+"/shares/pvc-share", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"pvc-share","path":"/pvc-share"}`)
	})
	mux.HandleFunc(client.BasePath+"/shares/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	// ListShares, used by the enumeration path (no snapshot_id supplied).
	mux.HandleFunc(client.BasePath+"/shares", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"pvc-share","path":"/pvc-share"}]`)
	})
	mux.HandleFunc(client.BasePath+"/share-snapshots/snapshot-list/pvc-share", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `["current","snap-1"]`)
	})
	mux.HandleFunc(client.BasePath+"/files", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("path") {
		// findSnapshotByID asks without a trailing slash; the enumeration path
		// (hsclient.ListSnapshots) asks with one.
		case "/pvc-share/.snapshot", "/pvc-share/.snapshot/":
			fmt.Fprint(w, `{"name":".snapshot","children":[{"name":"snap-1","size":1073741824,"createTime":1700000000}]}`)
		case "/backing/.fsnapshot/2026-09-22-00-00-00":
			fmt.Fprint(w, `{"name":"2026-09-22-00-00-00","children":[{"name":"pvc-file","size":2147483648,"createTime":1700000001}]}`)
		default:
			w.WriteHeader(404)
		}
	})
	server := httptest.NewServer(mux)
	hs, err := client.NewHammerspaceClient(server.URL, "test", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	return &CSIDriver{hsclient: hs}, server.Close
}

// The external snapshotter marks a PRE-PROVISIONED VolumeSnapshotContent ready
// only when ListSnapshots returns its snapshot handle. The handle is the
// composite ID CreateSnapshot issued, so looking it up must not fail just
// because the backend knows the snapshot by its bare name.
func TestListSnapshotsResolvesPreProvisionedHandles(t *testing.T) {
	driver, closeServer := newListSnapshotsDriver(t)
	defer closeServer()

	for _, tc := range []struct {
		name          string
		snapshotID    string
		wantFound     bool
		wantSize      int64
		wantSourceVol string
	}{
		{
			name:          "share-backed",
			snapshotID:    "snap-1|/pvc-share",
			wantFound:     true,
			wantSize:      1073741824,
			wantSourceVol: "/pvc-share",
		},
		{
			name:          "file-backed",
			snapshotID:    "/backing/.fsnapshot/2026-09-22-00-00-00/pvc-file|/backing/pvc-file",
			wantFound:     true,
			wantSize:      2147483648,
			wantSourceVol: "/backing/pvc-file",
		},
		{name: "unknown share snapshot", snapshotID: "no-such-snap|/pvc-share"},
		{name: "unknown source share", snapshotID: "snap-1|/no-such-share"},
		{name: "unknown file snapshot", snapshotID: "/backing/.fsnapshot/1999-01-01-00-00-00/pvc-file|/backing/pvc-file"},
		{name: "malformed handle", snapshotID: "not-a-composite-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := driver.ListSnapshots(context.Background(), &csi.ListSnapshotsRequest{SnapshotId: tc.snapshotID})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantFound {
				if len(resp.Entries) != 0 {
					t.Fatalf("expected no entries for %q, got %v", tc.snapshotID, resp.Entries)
				}
				return
			}
			if len(resp.Entries) != 1 {
				t.Fatalf("expected exactly one entry for %q, got %v", tc.snapshotID, resp.Entries)
			}
			snapshot := resp.Entries[0].Snapshot
			if snapshot.SnapshotId != tc.snapshotID {
				t.Fatalf("snapshot ID = %q, want the handle we asked for, %q", snapshot.SnapshotId, tc.snapshotID)
			}
			if !snapshot.ReadyToUse {
				t.Fatal("an existing snapshot must be reported ready, or the content never binds")
			}
			if snapshot.SourceVolumeId != tc.wantSourceVol {
				t.Fatalf("source volume = %q, want %q", snapshot.SourceVolumeId, tc.wantSourceVol)
			}
			if snapshot.SizeBytes != tc.wantSize {
				t.Fatalf("size = %d, want %d", snapshot.SizeBytes, tc.wantSize)
			}
		})
	}
}

// Filtering by both snapshot_id and source_volume_id must agree: a handle
// belonging to a different volume is not a match.
func TestListSnapshotsFiltersHandleBySourceVolume(t *testing.T) {
	driver, closeServer := newListSnapshotsDriver(t)
	defer closeServer()

	resp, err := driver.ListSnapshots(context.Background(), &csi.ListSnapshotsRequest{
		SnapshotId:     "snap-1|/pvc-share",
		SourceVolumeId: "/some-other-volume",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected no entries, got %v", resp.Entries)
	}
}

// The CO identifies a share-backed volume by its export path ("/pvc-share"),
// while the backend knows it as a bare share name ("pvc-share"). Filtering the
// enumeration on the name while reporting the path made every
// source_volume_id-filtered ListSnapshots return nothing.
func TestListSnapshotsFiltersEnumerationByExportPath(t *testing.T) {
	driver, closeServer := newListSnapshotsDriver(t)
	defer closeServer()

	resp, err := driver.ListSnapshots(context.Background(), &csi.ListSnapshotsRequest{
		SourceVolumeId: "/pvc-share",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("expected the share's snapshot to be listed for its export path, got %d entries", len(resp.Entries))
	}
	got := resp.Entries[0].Snapshot
	if got.SourceVolumeId != "/pvc-share" {
		t.Fatalf("source volume = %q, want %q", got.SourceVolumeId, "/pvc-share")
	}
	if got.SnapshotId != "snap-1|/pvc-share" {
		t.Fatalf("snapshot ID = %q, want the composite handle %q", got.SnapshotId, "snap-1|/pvc-share")
	}

	// A volume that is not this share must not match.
	resp, err = driver.ListSnapshots(context.Background(), &csi.ListSnapshotsRequest{
		SourceVolumeId: "/some-other-volume",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected no entries for an unrelated volume, got %d", len(resp.Entries))
	}
}
