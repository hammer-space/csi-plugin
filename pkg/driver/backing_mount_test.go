package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hammer-space/csi-plugin/pkg/client"
	"github.com/hammer-space/csi-plugin/pkg/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeBackingMount serves one backing share from a fake Anvil and replaces the
// mount table, the Anvil's server list and losetup.
type fakeBackingMount struct {
	server    string   // server of the NFS mount at the backing dir; "" for none
	current   []string // the Anvil's data portals and floating IPs
	listErr   error
	inUse     bool
	unmounted []string
}

func useFakeBackingMount(t *testing.T, f *fakeBackingMount) *CSIDriver {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(client.BasePath+"/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc(client.BasePath+"/shares/backing", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"backing","path":"/backing"}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	hs, err := client.NewHammerspaceClient(server.URL, "test", "test", true)
	if err != nil {
		t.Fatal(err)
	}

	origIsMountPoint, origServer, origAddresses, origInUse, origUnmount := isMountPoint, backingMountServer, nfsServerAddresses, backingFilesInUse, unmountFilesystem
	t.Cleanup(func() {
		isMountPoint, backingMountServer, nfsServerAddresses, backingFilesInUse, unmountFilesystem = origIsMountPoint, origServer, origAddresses, origInUse, origUnmount
	})
	isMountPoint = func(string) (bool, error) { return f.server != "", nil }
	backingMountServer = func(string) (string, error) { return f.server, nil }
	nfsServerAddresses = func(*CSIDriver, context.Context) ([]string, error) { return f.current, f.listErr }
	backingFilesInUse = func(string) (bool, error) { return f.inUse, nil }
	unmountFilesystem = func(_ context.Context, path string) error {
		f.unmounted = append(f.unmounted, path)
		f.server = ""
		return nil
	}
	return &CSIDriver{hsclient: hs, mountRefs: map[string]int{}, mountLocks: map[string]*sync.Mutex{}}
}

func TestBackingMountFromCurrentAnvilIsReused(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.0.5", current: []string{"10.0.0.4", "10.0.0.5"}}
	d := useFakeBackingMount(t, f)

	if err := d.EnsureBackingShareMounted(context.Background(), "backing", &common.HSVolume{}); err != nil {
		t.Fatalf("EnsureBackingShareMounted: %v", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("a mount from a current data portal must be kept, unmounted %v", f.unmounted)
	}
}

// After the driver is pointed at a new Anvil, a backing mount left on the host
// from the old one still answers. Volume files must not be created on it.
func TestBackingMountFromPreviousAnvilIsReplaced(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.9.9", current: []string{"10.0.0.5"}}
	d := useFakeBackingMount(t, f)

	// The remount then fails because the fake Anvil has no data portals; what
	// matters is that the old mount was taken down first.
	_ = d.EnsureBackingShareMounted(context.Background(), "backing", &common.HSVolume{})
	want := common.ShareStagingDir + "/backing"
	if len(f.unmounted) != 1 || f.unmounted[0] != want {
		t.Fatalf("the old Anvil's mount should have been unmounted once at %s, unmounted %v", want, f.unmounted)
	}
}

func TestBackingMountFromPreviousAnvilInUseIsRefused(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.9.9", current: []string{"10.0.0.5"}, inUse: true}
	d := useFakeBackingMount(t, f)

	err := d.EnsureBackingShareMounted(context.Background(), "backing", &common.HSVolume{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("a mount loop devices still use must not be unmounted, unmounted %v", f.unmounted)
	}
}

// An in-flight create holds a reference but has no loop device until its mkfs
// is done; replacing the mount under it would fail the create or send its file
// to the node's local disk.
func TestBackingMountFromPreviousAnvilWithInFlightCreateIsRefused(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.9.9", current: []string{"10.0.0.5"}}
	d := useFakeBackingMount(t, f)
	d.bumpBackingRef(common.ShareStagingDir + "/backing")

	err := d.EnsureBackingShareMounted(context.Background(), "backing", &common.HSVolume{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("a mount an in-flight create references must not be unmounted, unmounted %v", f.unmounted)
	}
}

// The caller's own reference (acquireBackingMount) doesn't count as another user.
func TestBackingMountFromPreviousAnvilOwnReferenceIsReplaced(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.9.9", current: []string{"10.0.0.5"}}
	d := useFakeBackingMount(t, f)
	backingDir := common.ShareStagingDir + "/backing"
	d.bumpBackingRef(backingDir)

	_ = d.ensureBackingShareMounted(context.Background(), "backing", &common.HSVolume{}, true)
	if len(f.unmounted) != 1 || f.unmounted[0] != backingDir {
		t.Fatalf("the old Anvil's mount should have been unmounted once at %s, unmounted %v", backingDir, f.unmounted)
	}
}

// If the Anvil can't be asked, an outage must not take down a working mount.
func TestBackingMountKeptWhenAnvilCannotBeAsked(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.9.9", listErr: errors.New("anvil down")}
	d := useFakeBackingMount(t, f)

	if err := d.EnsureBackingShareMounted(context.Background(), "backing", &common.HSVolume{}); err != nil {
		t.Fatalf("EnsureBackingShareMounted: %v", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("unmounted %v with no server list to compare against", f.unmounted)
	}
}

func TestBackingMountFromStorageClassFQDNIsReused(t *testing.T) {
	f := &fakeBackingMount{server: "10.0.7.7", current: []string{"10.0.0.5"}}
	d := useFakeBackingMount(t, f)
	origLookup := lookupIP
	t.Cleanup(func() { lookupIP = origLookup })
	lookupIP = func(host string) ([]net.IP, error) {
		if host == "nfs.example.com" {
			return []net.IP{net.ParseIP("10.0.7.7")}, nil
		}
		return nil, errors.New("unknown host")
	}

	if err := d.EnsureBackingShareMounted(context.Background(), "backing", &common.HSVolume{FQDN: "nfs.example.com"}); err != nil {
		t.Fatalf("EnsureBackingShareMounted: %v", err)
	}
	if len(f.unmounted) != 0 {
		t.Fatalf("a mount from the StorageClass FQDN must be kept, unmounted %v", f.unmounted)
	}
}

func TestNFSSourceHost(t *testing.T) {
	for source, want := range map[string]string{
		"10.200.107.185:/hscsi-cert-20260918-ext4": "10.200.107.185",
		"[fd00::1]:/share":                         "fd00::1",
		"nfs.example.com:/share/dir":               "nfs.example.com",
		"/dev/loop0":                               "",
		"tmpfs":                                    "",
	} {
		if got := nfsSourceHost(source); got != want {
			t.Errorf("nfsSourceHost(%q) = %q, want %q", source, got, want)
		}
	}
}
