package common

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetNFSExports(t *testing.T) {
	// case 1: empty output → should return error
	ExecCommand = func(command string, args ...string) ([]byte, error) {
		return []byte(""), nil
	}
	_, err := GetNFSExports("127.0.0.1")
	if err == nil {
		t.Errorf("Expected error for empty export list, got nil")
	}

	// case 2: whitespace output → should return error
	ExecCommand = func(command string, args ...string) ([]byte, error) {
		return []byte(`


`), nil
	}
	_, err = GetNFSExports("127.0.0.1")
	if err == nil {
		t.Errorf("Expected error for whitespace export list, got nil")
	}

	// case 3: valid exports → should parse correctly
	ExecCommand = func(command string, args ...string) ([]byte, error) {
		return []byte(`/test    *
/mnt/data-portal/test        *
/hs/test				*
`), nil
	}
	expected := []string{"/test", "/mnt/data-portal/test", "/hs/test"}
	actual, err := GetNFSExports("127.0.0.1")
	if err != nil {
		t.Fatalf("Unexpected error, %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("Expected: %v", expected)
		t.Errorf("Actual: %v", actual)
	}
}

func TestDetermineBackingFileFromLoopDevice(t *testing.T) {
	ExecCommand = func(command string, args ...string) ([]byte, error) {
		return []byte(`
/dev/loop0: 0 /tmp/test
/dev/loop1: 0 /tmp/test
/dev/loop2: 0 /tmp//test-csi-block/sanity-node-full-E067A84C-D67CAA8E
`), nil
	}
	expected := "/tmp/test"
	actual, err := determineBackingFileFromLoopDevice("/dev/loop0")
	if err != nil {
		t.Logf("Unexpected error, %v", err)
		t.FailNow()
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

func TestExecCommandHelper(t *testing.T) {
	expected := []byte("test\n")
	actual, err := execCommandHelper("echo", "test")
	if err != nil {
		t.Logf("Unexpected error, %v", err)
		t.FailNow()
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}

	CommandExecTimeout = 1
	_, err = execCommandHelper("sleep", "5")
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}

}

// TestBeginMountDeduplicatesByTarget covers the fix for PR #67 review note #2:
// a mount goroutine wedged on a dead data portal must not be re-forked by every
// retry against the same target. beginMount should attach retries to the single
// in-flight attempt, then start fresh once it completes.
func TestBeginMountDeduplicatesByTarget(t *testing.T) {
	mountInFlightMu.Lock()
	mountInFlight = map[string]*inFlightMount{}
	mountInFlightMu.Unlock()

	const target = "/mnt/dead-portal-vol"
	var starts int32               // how many mount funcs actually began
	release := make(chan struct{}) // gate that keeps the first mount "wedged"

	blockingMount := func() error {
		atomic.AddInt32(&starts, 1)
		<-release // simulate a hard NFS mount stuck in the kernel
		return nil
	}
	shouldNotRun := func() error {
		atomic.AddInt32(&starts, 1)
		return errors.New("second mount fn should not have been invoked")
	}

	// First attempt starts the (blocked) mount goroutine.
	att1 := beginMount(target, blockingMount)

	// Wait until the goroutine has actually entered mountFn.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&starts) == 0 {
		select {
		case <-deadline:
			t.Fatal("first mount goroutine never started")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Retries against the SAME target while the first is still wedged must attach
	// to att1 and must NOT invoke a new mount fn.
	for i := 0; i < 5; i++ {
		if att := beginMount(target, shouldNotRun); att != att1 {
			t.Fatalf("retry %d got a different attempt; expected dedup to the in-flight one", i)
		}
	}
	if got := atomic.LoadInt32(&starts); got != 1 {
		t.Fatalf("mount fn ran %d times across 6 attempts; want exactly 1 (dedup failed)", got)
	}

	// Let the wedged mount finish; att1.done must close and carry its result.
	close(release)
	select {
	case <-att1.done:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight mount never completed after release")
	}
	if att1.err != nil {
		t.Fatalf("unexpected mount error: %v", att1.err)
	}

	// Entry must be cleared so a later mount of the same target starts fresh.
	mountInFlightMu.Lock()
	_, stillThere := mountInFlight[target]
	mountInFlightMu.Unlock()
	if stillThere {
		t.Fatal("completed mount left a stale in-flight entry")
	}

	// A fresh attempt after completion spawns a new goroutine.
	att2 := beginMount(target, func() error { atomic.AddInt32(&starts, 1); return nil })
	<-att2.done
	if att2 == att1 {
		t.Fatal("post-completion mount reused the old attempt")
	}
	if got := atomic.LoadInt32(&starts); got != 2 {
		t.Fatalf("post-completion mount fn didn't run fresh; starts=%d want 2", got)
	}
}

// TestExpandDeviceFileSizeOrdering is a regression test for issue #71: the backing
// file must be grown (qemu-img resize) BEFORE the loop device size is refreshed
// (losetup -c). losetup -c (LOOP_SET_CAPACITY) snapshots the backing file's size at
// the moment it runs, so refreshing before the resize leaves the loop device at the
// old size — making the first NodeExpandVolume's resize2fs/xfs_growfs a no-op (ext4/
// xfs fail; block has no filesystem check to catch it).
func TestExpandDeviceFileSizeOrdering(t *testing.T) {
	orig := ExecCommand
	defer func() { ExecCommand = orig }()

	const backing = "/mnt/backing/csi-file-pvc-abc.img"
	const loopdev = "/dev/loop7"

	var calls [][]string
	ExecCommand = func(command string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{command}, args...))
		// determineLoopDeviceFromBackingFile parses `losetup -a` lines of the form
		// "<loop>: [maj:min]:inode (<backing>)" and matches field[2] to the backing file.
		if command == "losetup" && len(args) == 1 && args[0] == "-a" {
			return []byte(loopdev + ": [2049]:999 (" + backing + ")\n"), nil
		}
		return []byte(""), nil
	}

	if err := ExpandDeviceFileSize(backing, 2147483648); err != nil {
		t.Fatalf("ExpandDeviceFileSize returned error: %v", err)
	}

	idxResize, idxRefresh := -1, -1
	for i, c := range calls {
		switch {
		case c[0] == "qemu-img" && len(c) >= 2 && c[1] == "resize":
			idxResize = i
		case c[0] == "losetup" && len(c) >= 2 && c[1] == "-c":
			idxRefresh = i
		}
	}
	if idxResize == -1 {
		t.Fatalf("qemu-img resize was never called; calls=%v", calls)
	}
	if idxRefresh == -1 {
		t.Fatalf("losetup -c was never called; calls=%v", calls)
	}
	if idxResize > idxRefresh {
		t.Fatalf("issue #71 ordering bug: backing file grown (qemu-img resize @%d) AFTER loop refresh (losetup -c @%d); calls=%v",
			idxResize, idxRefresh, calls)
	}
	// the resize must target the requested size, and the refresh the discovered loop dev
	if got := calls[idxResize][len(calls[idxResize])-1]; got != "2147483648" {
		t.Errorf("qemu-img resize size = %q, want 2147483648", got)
	}
	if got := calls[idxRefresh][len(calls[idxRefresh])-1]; got != loopdev {
		t.Errorf("losetup -c target = %q, want %s", got, loopdev)
	}
}
