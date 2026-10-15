package driver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/client"
	"github.com/hammer-space/csi-plugin/pkg/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetCapacityUsesStorageClassParameters(t *testing.T) {
	for _, tc := range []struct {
		name        string
		params      map[string]string
		share       string
		shareStatus int
		want        int64
		wantCode    codes.Code
	}{
		{name: "NFS default", want: 8192},
		{name: "NFS explicit", params: map[string]string{"fsType": "nfs"}, want: 8192},
		{name: "raw block", params: map[string]string{"blockBackingShareName": "block-share"}, share: "block-share", shareStatus: 200, want: 4096},
		{name: "ext4", params: map[string]string{"fsType": "ext4", "mountBackingShareName": "fs-share"}, share: "fs-share", shareStatus: 200, want: 4096},
		{name: "XFS new backing share", params: map[string]string{"fsType": "xfs", "mountBackingShareName": "fs-share"}, share: "fs-share", shareStatus: 404, want: 8192},
		{name: "backing API failure", params: map[string]string{"blockBackingShareName": "block-share"}, share: "block-share", shareStatus: 500, wantCode: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Subtests reuse share names; start each without a cached figure.
			common.SetCacheData("BACKING_SHARE_FREE:"+tc.share, nil, 60)
			shareCalls := 0
			mux := http.NewServeMux()
			mux.HandleFunc(client.BasePath+"/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			mux.HandleFunc(client.BasePath+"/cntl/state", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"capacity":{"free":8192}}`) })
			mux.HandleFunc(client.BasePath+"/shares/", func(w http.ResponseWriter, r *http.Request) {
				shareCalls++
				if tc.share == "" || r.URL.Path != client.BasePath+"/shares/"+tc.share {
					t.Errorf("unexpected backing share request: %s", r.URL.Path)
					w.WriteHeader(400)
					return
				}
				w.WriteHeader(tc.shareStatus)
				if tc.shareStatus == 200 {
					fmt.Fprint(w, `{"space":{"available":4096}}`)
				}
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			hs, err := client.NewHammerspaceClient(server.URL, "test", "test", true)
			if err != nil {
				t.Fatal(err)
			}
			driver := &CSIDriver{hsclient: hs}
			// External provisioner capacity polling supplies StorageClass
			// parameters without per-PVC volume capabilities.
			got, err := driver.GetCapacity(context.Background(), &csi.GetCapacityRequest{Parameters: tc.params})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("error = %v, want code %v", err, tc.wantCode)
			}
			if err == nil && got.AvailableCapacity != tc.want {
				t.Fatalf("capacity = %d, want %d", got.AvailableCapacity, tc.want)
			}
			if tc.share != "" && shareCalls != 1 {
				t.Fatalf("backing share queries = %d, want 1", shareCalls)
			}
		})
	}
}

// The provisioner refreshes capacity for every StorageClass in turn, so a
// backing share lookup stuck on a slow Anvil held up every other class. Its
// free space is reused for a while; a missing share is not cached.
func TestGetCapacityReusesBackingShareFreeSpace(t *testing.T) {
	common.SetCacheData("BACKING_SHARE_FREE:cached-share", nil, 60)
	common.SetCacheData("BACKING_SHARE_FREE:new-share", nil, 60)
	calls := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc(client.BasePath+"/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc(client.BasePath+"/cntl/state", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"capacity":{"free":8192}}`) })
	mux.HandleFunc(client.BasePath+"/shares/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[len(client.BasePath+"/shares/"):]
		calls[name]++
		if name == "new-share" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"space":{"available":4096}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	hs, err := client.NewHammerspaceClient(server.URL, "test", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	driver := &CSIDriver{hsclient: hs}

	for i := 0; i < 3; i++ {
		for _, share := range []string{"cached-share", "new-share"} {
			req := &csi.GetCapacityRequest{Parameters: map[string]string{"blockBackingShareName": share}}
			if _, err := driver.GetCapacity(context.Background(), req); err != nil {
				t.Fatalf("GetCapacity(%s): %v", share, err)
			}
		}
	}
	if calls["cached-share"] != 1 {
		t.Errorf("existing backing share queried %d times, want 1", calls["cached-share"])
	}
	if calls["new-share"] != 3 {
		t.Errorf("missing backing share queried %d times, want 3 so its creation is noticed", calls["new-share"])
	}
}
