package driver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/client"
)

// Deleting a share-backed volume whose share is already gone must succeed, even
// while the Anvil still has the share's directory. Before, the ID was handed to
// the file-backed path, which derived a backing share of "/" and failed on every
// retry.
func TestDeleteVolumeSucceedsWhenShareAlreadyGone(t *testing.T) {
	const share = "hscsi-pvc-gone"
	mux := http.NewServeMux()
	mux.HandleFunc(client.BasePath+"/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc(client.BasePath+"/shares/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != client.BasePath+"/shares/"+share {
			t.Errorf("unexpected share request: %s", r.URL.Path)
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The leftover directory would be reported as existing; nothing past
		// the share lookup should need to ask.
		t.Errorf("unexpected Anvil request after the share was found missing: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(200)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	hs, err := client.NewHammerspaceClient(server.URL, "test", "test", true)
	if err != nil {
		t.Fatal(err)
	}
	d := &CSIDriver{hsclient: hs, volumeLocks: make(map[string]*keyLock)}

	if _, err := d.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "/" + share}); err != nil {
		t.Fatalf("DeleteVolume of an already-deleted share returned %v, want success", err)
	}
}
