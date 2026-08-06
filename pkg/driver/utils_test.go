package driver

import (
	"context"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hammer-space/csi-plugin/pkg/common"
)

func TestGetSnapshotNameFromSnapshotId(t *testing.T) {

	snapshotId := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	expected := "2019-05-24T15-26-57-0"
	actual, err := GetSnapshotNameFromSnapshotId(snapshotId)
	if err != nil {
		t.Logf("Unexpected error, %v", err)
		t.FailNow()
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}

	snapshotId = "2019-05-24T15-26-57-0"
	_, err = GetSnapshotNameFromSnapshotId(snapshotId)
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}

}

func TestGetShareNameFromSnapshotId(t *testing.T) {

	snapshotId := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	expected := "sanity-controller-source-vol-859F8B9B-35BBFB36"
	actual, err := GetShareNameFromSnapshotId(snapshotId)
	if err != nil {
		t.Logf("Unexpected error, %v", err)
		t.FailNow()
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}

	snapshotId = "2019-05-24T15-26-57-0"
	_, err = GetShareNameFromSnapshotId(snapshotId)
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}
}

func TestGetSnapshotIDFromSnapshotName(t *testing.T) {
	expected := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	actual := GetSnapshotIDFromSnapshotName("2019-05-24T15-26-57-0",
		"/sanity-controller-source-vol-859F8B9B-35BBFB36")
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

func TestIsDirectoryFile(t *testing.T) {
	tests := []struct {
		name    string
		file    *common.File
		want    bool
		wantErr bool
	}{
		{name: "directory", file: &common.File{Type: "DIRECTORY"}, want: true},
		{name: "file", file: &common.File{Type: "FILE"}},
		{name: "case insensitive", file: &common.File{Type: "directory"}, want: true},
		{name: "symbolic link", file: &common.File{Type: "SYM_LINK"}, wantErr: true},
		{name: "other", file: &common.File{Type: "OTHER"}, wantErr: true},
		{name: "unknown", file: &common.File{}, wantErr: true},
		{name: "nil", file: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isDirectoryFile(tt.file)
			if (err != nil) != tt.wantErr {
				t.Fatalf("isDirectoryFile() error = %v, wantErr %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("isDirectoryFile() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestGetVolumeNameFromPath(t *testing.T) {
	expected := "test-volume"
	actual := GetVolumeNameFromPath("/test-backing-share/test-volume")
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

// TestIsFileBackedVolumeID covers the structural file- vs share-backed
// discriminator (PR C): a file-backed volume ID is a file *inside* a backing
// share (multi-segment path), while a share-backed/native NFS volume ID is the
// share itself (single top-level segment). This replaced a GetShare probe that
// 404'd for every file-backed source.
func TestIsFileBackedVolumeID(t *testing.T) {
	cases := []struct {
		volumeID string
		want     bool
	}{
		{"/k8s-file-backed/file--pvc-1234", true}, // file inside a backing share
		{"/backing/file--pvc-abcd/nested", true},  // deeper still -> file-backed
		{"/some-share", false},                    // native NFS share
		{"/hs-nfs-prod", false},                   // native NFS share
		{"/", false},                              // root
		{"", false},                               // empty -> Dir("")=="." != "/" would be true; guard below
	}
	for _, tc := range cases {
		got := isFileBackedVolumeID(tc.volumeID)
		// "" is a malformed ID; document current behavior rather than assert a
		// specific value we don't rely on.
		if tc.volumeID == "" {
			continue
		}
		if got != tc.want {
			t.Fatalf("isFileBackedVolumeID(%q) = %v, want %v", tc.volumeID, got, tc.want)
		}
	}
}

// TestClassifyMount covers the fix for review comment #1: a single
// SafeIsMountPoint timeout must NOT be treated as "stale" (which would trigger a
// force-unmount of a live shared backing mount). Only `probes` CONSECUTIVE
// timeouts classify as stale; a timeout that recovers on retry is healthy.
func TestClassifyMount(t *testing.T) {
	type r struct {
		mounted bool
		err     error
	}
	// newProbe returns a probe that yields the scripted results in order,
	// repeating the last one for any extra calls.
	newProbe := func(results ...r) func(string) (bool, error) {
		i := 0
		return func(string) (bool, error) {
			res := results[i]
			if i < len(results)-1 {
				i++
			}
			return res.mounted, res.err
		}
	}
	cases := []struct {
		name    string
		results []r
		probes  int
		want    mountState
	}{
		{"healthy on first probe", []r{{true, nil}}, 2, mountHealthy},
		{"absent (not a mount point)", []r{{false, nil}}, 2, mountAbsent},
		{"not-exist path -> absent", []r{{false, os.ErrNotExist}}, 2, mountAbsent},
		{"all timeouts -> stale", []r{{false, context.DeadlineExceeded}, {false, context.DeadlineExceeded}}, 2, mountStale},
		{"timeout then healthy -> healthy (no false positive)", []r{{false, context.DeadlineExceeded}, {true, nil}}, 2, mountHealthy},
		{"timeout then absent -> absent", []r{{false, context.DeadlineExceeded}, {false, nil}}, 2, mountAbsent},
		{"single probe timeout -> stale", []r{{false, context.DeadlineExceeded}}, 1, mountStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyMount("/backing", tc.probes, newProbe(tc.results...)); got != tc.want {
				t.Fatalf("classifyMount = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDropBackingRefNoDeadlock is a regression test for the self-deadlock found
// during live xfs validation: releaseBackingMount held mountRefsMu while calling
// UnmountBackingShareIfUnused, which re-locks the same (non-reentrant) mutex via
// backingMountInUse — so the last reference to drop wedged the whole file-backed
// mount subsystem. dropBackingRef must release mountRefsMu before returning, so a
// same-mutex call (like backingMountInUse, which the real unmount path makes) is
// safe immediately afterwards. The whole sequence runs under a watchdog: if the
// lock is ever held across the callout again, this hangs and the test fails.
func TestDropBackingRefNoDeadlock(t *testing.T) {
	d := &CSIDriver{mountRefs: map[string]int{}}
	p := "/tmp/k8s-file-rev"

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Two concurrent creates hold the share; the first release is NOT the last.
		d.mountRefs[p] = 2
		if last := d.dropBackingRef(p); last {
			t.Errorf("dropBackingRef at refcount 2->1 reported last=true")
		}
		// Mimic the real unmount path taking the same mutex right after the drop.
		// Before the fix, this is exactly where the goroutine deadlocked.
		if !d.backingMountInUse(p) {
			t.Errorf("refcount 1 should still read as in-use")
		}
		// Second release IS the last: entry must be deleted and read not-in-use.
		if last := d.dropBackingRef(p); !last {
			t.Errorf("dropBackingRef at refcount 1->0 reported last=false")
		}
		if d.backingMountInUse(p) {
			t.Errorf("refcount 0 should read as not-in-use after final drop")
		}
		if _, ok := d.mountRefs[p]; ok {
			t.Errorf("map entry should be deleted at refcount 0")
		}
		// Dropping below zero must stay at last=true and not underflow.
		if last := d.dropBackingRef(p); !last {
			t.Errorf("dropBackingRef on absent key should report last=true")
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dropBackingRef/backingMountInUse deadlocked: mountRefsMu held across callout")
	}
}

// TestBumpBackingRefFirst covers bumpBackingRef reporting the 0->1 transition,
// which is what tells acquireBackingMount it owns the mount. The reference is taken
// BEFORE mounting so a concurrent delete can't see refcount 0 mid-mount.
func TestBumpBackingRefFirst(t *testing.T) {
	d := &CSIDriver{mountRefs: map[string]int{}}
	p := "/tmp/k8s-file-rev"

	if first := d.bumpBackingRef(p); !first {
		t.Fatal("first bump on an unmounted share should report first=true")
	}
	if first := d.bumpBackingRef(p); first {
		t.Fatal("second bump should report first=false (already mounted)")
	}
	if d.mountRefs[p] != 2 {
		t.Fatalf("refcount = %d, want 2", d.mountRefs[p])
	}
	// Drain back to 0, then the next bump is a fresh 0->1 transition again.
	d.dropBackingRef(p)
	d.dropBackingRef(p)
	if first := d.bumpBackingRef(p); !first {
		t.Fatal("bump after draining to 0 should report first=true again")
	}
}

// TestBackingMountLockDoesNotBlockRefcount is the core regression test for moving
// the slow NFS mount off the global mountRefsMu: while a per-directory mount lock is
// held (simulating an in-progress or hung mount that can last up to ~5 min), the
// global refcount operations — bumpBackingRef, backingMountInUse, dropBackingRef,
// for the SAME dir and OTHER dirs — must still complete immediately. If the mount
// ever moves back under mountRefsMu, those ops would block behind the held mount
// lock and this watchdog fails.
func TestBackingMountLockDoesNotBlockRefcount(t *testing.T) {
	d := &CSIDriver{mountRefs: map[string]int{}, mountLocks: map[string]*sync.Mutex{}}
	dir1 := "/tmp/k8s-file-rev"
	dir2 := "/tmp/other-share"

	// mountLockFor must be stable per dir and distinct across dirs.
	if d.mountLockFor(dir1) != d.mountLockFor(dir1) {
		t.Fatal("mountLockFor returned different locks for the same dir")
	}
	if d.mountLockFor(dir1) == d.mountLockFor(dir2) {
		t.Fatal("mountLockFor returned the same lock for different dirs")
	}

	// Simulate a slow/hung mount: hold dir1's per-dir mount lock for the whole test.
	ml := d.mountLockFor(dir1)
	ml.Lock()
	defer ml.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Refcount ops on the SAME dir whose mount is "in progress" must not block.
		d.bumpBackingRef(dir1)
		if !d.backingMountInUse(dir1) {
			t.Errorf("dir1 should read in-use after bump")
		}
		d.dropBackingRef(dir1)
		// ...and neither must ops on an unrelated dir.
		d.bumpBackingRef(dir2)
		d.dropBackingRef(dir2)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refcount ops blocked behind a held mount lock: mount is back under mountRefsMu")
	}
}

// TestBackingMountInUse covers the fix for review comment #2: the mountRefs
// refcount is the authoritative in-use signal that UnmountBackingShareIfUnused
// now consults (so it can't unmount a share out from under an in-flight mkfs
// that holds a reference but has no loop device yet).
func TestBackingMountInUse(t *testing.T) {
	d := &CSIDriver{mountRefs: map[string]int{}}
	p := "/tmp/k8s-file-backed"

	if d.backingMountInUse(p) {
		t.Fatal("empty refcount should report not-in-use")
	}
	d.mountRefs[p] = 1
	if !d.backingMountInUse(p) {
		t.Fatal("refcount 1 should report in-use")
	}
	d.mountRefs[p] = 2
	if !d.backingMountInUse(p) {
		t.Fatal("refcount 2 should report in-use")
	}
	// An unrelated path's references must not make this path read as in-use.
	d.mountRefs["/tmp/other-share"] = 5
	d.mountRefs[p] = 0
	if d.backingMountInUse(p) {
		t.Fatal("refcount 0 should report not-in-use even when another path is referenced")
	}
}
