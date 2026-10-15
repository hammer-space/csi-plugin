package driver

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestGrowFilesystemPrivately(t *testing.T) {
	originalMount := mountFilesystem
	originalReconcile := reconcileFilesystem
	originalUnmount := unmountFilesystem
	originalNeedsGrowth := filesystemNeedsGrowth
	defer func() {
		mountFilesystem = originalMount
		reconcileFilesystem = originalReconcile
		unmountFilesystem = originalUnmount
		filesystemNeedsGrowth = originalNeedsGrowth
	}()
	filesystemNeedsGrowth = func(string, string) (bool, error) { return true, nil }

	var temporary string
	var calls []string
	mountFilesystem = func(source, target, fsType string, flags []string) error {
		temporary = target
		calls = append(calls, "mount:"+source+":"+fsType)
		if len(flags) != 0 {
			t.Fatalf("temporary mount flags = %v, want writable mount", flags)
		}
		return nil
	}
	reconcileFilesystem = func(target, backing, fsType string) error {
		if target != temporary || backing != "/tmp/share/volume" || fsType != "ext4" {
			t.Fatalf("unexpected reconcile arguments: %q %q %q", target, backing, fsType)
		}
		calls = append(calls, "reconcile")
		return nil
	}
	unmountFilesystem = func(_ context.Context, target string) error {
		if target != temporary {
			t.Fatalf("unmount target = %q, want %q", target, temporary)
		}
		calls = append(calls, "unmount")
		return nil
	}

	if err := growFilesystemPrivatelyIfNeeded(context.Background(), "/tmp/share/volume", "ext4"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"mount:/tmp/share/volume:ext4", "reconcile", "unmount"}) {
		t.Fatalf("calls = %v", calls)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary directory was not removed: %v", err)
	}
}

// A failed reconcile on a fresh publish must undo the mount: kubelet's retry
// would otherwise find targetPath mounted, treat it as a republish and report
// success on an undersized filesystem. A republish keeps its working mount.
func TestReconcilePublishedFilesystemUnmountsFreshMountOnFailure(t *testing.T) {
	originalReconcile := reconcileFilesystem
	originalUnmount := unmountFilesystem
	defer func() {
		reconcileFilesystem = originalReconcile
		unmountFilesystem = originalUnmount
	}()
	reconcileFilesystem = func(string, string, string) error { return fmt.Errorf("resize2fs failed") }

	for _, tc := range []struct {
		fresh       bool
		wantErr     bool
		wantUnmount bool
	}{
		{fresh: true, wantErr: true, wantUnmount: true},
		{fresh: false, wantErr: false, wantUnmount: false},
	} {
		var unmounted []string
		unmountFilesystem = func(_ context.Context, target string) error {
			unmounted = append(unmounted, target)
			return nil
		}
		err := reconcilePublishedFilesystem(context.Background(), "/target", "/tmp/share/volume", "ext4", tc.fresh)
		if (err != nil) != tc.wantErr {
			t.Errorf("fresh=%v: err = %v, want error %v", tc.fresh, err, tc.wantErr)
		}
		if got := len(unmounted) == 1 && unmounted[0] == "/target"; got != tc.wantUnmount {
			t.Errorf("fresh=%v: unmounted = %v, want unmount of /target %v", tc.fresh, unmounted, tc.wantUnmount)
		}
	}
}

// A filesystem that is already full size must not be mounted writable: a
// ReadOnlyMany volume may be mounted read-only by other pods at the same time.
func TestGrowFilesystemPrivatelySkipsFullSizeFilesystem(t *testing.T) {
	originalMount := mountFilesystem
	originalNeedsGrowth := filesystemNeedsGrowth
	defer func() {
		mountFilesystem = originalMount
		filesystemNeedsGrowth = originalNeedsGrowth
	}()
	filesystemNeedsGrowth = func(string, string) (bool, error) { return false, nil }
	mountFilesystem = func(source, target, fsType string, flags []string) error {
		t.Fatalf("unexpected writable mount of %s at %s", source, target)
		return nil
	}

	if err := growFilesystemPrivatelyIfNeeded(context.Background(), "/tmp/share/volume", "xfs"); err != nil {
		t.Fatal(err)
	}
}

// A read-only publish must not remount the staged mount it binds from, which
// every other pod publishing the same volume shares. The read-only flag goes on
// a private bind that is removed once the pod target is bound from it.
func TestBindMountReadOnlyUsesPrivateMount(t *testing.T) {
	originalBind := bindMountDevice
	originalRemount := remountBindReadOnly
	originalUnmount := unmountFilesystem
	defer func() {
		bindMountDevice = originalBind
		remountBindReadOnly = originalRemount
		unmountFilesystem = originalUnmount
	}()

	const source = "/var/lib/kubelet/plugins/kubernetes.io/csi/com.hammerspace.csi/x/globalmount"
	const target = "/var/lib/kubelet/pods/test/volume"
	var calls []string
	var private string

	bindMountDevice = func(from, to string) error {
		calls = append(calls, fmt.Sprintf("bind:%s:%s", from, to))
		if private == "" {
			private = to
			if from != source {
				t.Fatalf("first bind source = %q, want %q", from, source)
			}
			return nil
		}
		if from != private || to != target {
			t.Fatalf("final bind = %q -> %q, want %q -> %q", from, to, private, target)
		}
		return nil
	}
	remountBindReadOnly = func(path string) error {
		calls = append(calls, "remount-ro:"+path)
		if path != private {
			t.Fatalf("remounted %q read-only, want only the private bind %q", path, private)
		}
		return nil
	}
	unmountFilesystem = func(_ context.Context, path string) error {
		calls = append(calls, "unmount:"+path)
		if path != private {
			t.Fatalf("unmount target = %q, want private bind %q", path, private)
		}
		return os.Remove(path)
	}

	if err := bindMountReadOnly(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	if private == "" {
		t.Fatal("private bind path was not created")
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatalf("private bind directory was not cleaned up: %v", err)
	}
	if len(calls) != 4 || calls[3] != "unmount:"+private {
		t.Fatalf("mount sequence = %v, want bind, remount-ro, bind, then unmount of the private bind", calls)
	}
}

// A nested NFS volume is a directory inside its backing share, and is staged by
// mounting that directory itself. Anything else must be rejected rather than
// mounting the wrong path.
func TestNestedNFSSubPath(t *testing.T) {
	for _, tc := range []struct {
		volumeID, exportPath, want string
		wantErr                    bool
	}{
		{volumeID: "/backing/pvc-1", exportPath: "/backing", want: "/pvc-1"},
		{volumeID: "/parent/backing/pvc-1", exportPath: "/parent/backing", want: "/pvc-1"},
		{volumeID: "/backing", exportPath: "/backing", wantErr: true},
		{volumeID: "/backing/", exportPath: "/backing", wantErr: true},
		{volumeID: "/backingother/pvc-1", exportPath: "/backing", wantErr: true},
		{volumeID: "/elsewhere/pvc-1", exportPath: "/backing", wantErr: true},
	} {
		got, err := nestedNFSSubPath(tc.volumeID, tc.exportPath)
		if tc.wantErr {
			if err == nil {
				t.Errorf("nestedNFSSubPath(%q, %q) = %q, want an error", tc.volumeID, tc.exportPath, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("nestedNFSSubPath(%q, %q) = %q, %v; want %q", tc.volumeID, tc.exportPath, got, err, tc.want)
		}
	}
}

// Nested NFS volumes need a superblock of their own, or kubelet counts the
// backing share's mount as a reference to the volume and never unstages it.
// An explicit choice in the StorageClass is left alone.
func TestNestedNFSMountFlagsAddsNoShareCache(t *testing.T) {
	for _, tc := range []struct {
		in, want []string
	}{
		{in: nil, want: []string{"nosharecache"}},
		{in: []string{"ro", "vers=3,nolock"}, want: []string{"ro", "vers=3,nolock", "nosharecache"}},
		{in: []string{"hard,nosharecache"}, want: []string{"hard,nosharecache"}},
		{in: []string{"sharecache"}, want: []string{"sharecache"}},
	} {
		in := append([]string{}, tc.in...)
		got := nestedNFSMountFlags(in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("nestedNFSMountFlags(%v) = %v, want %v", tc.in, got, tc.want)
		}
		if len(in) != len(tc.in) || (len(in) > 0 && !reflect.DeepEqual(in, tc.in)) {
			t.Errorf("nestedNFSMountFlags modified its input: %v", in)
		}
	}
}
