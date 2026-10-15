package driver

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/common"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeStagingMounts replaces the node's mount checks with a set of paths that
// count as mounted, and records what gets staged, bound and unmounted.
type fakeStagingMounts struct {
	mounted   map[string]bool
	staged    []string
	binds     [][2]string
	unmounted []string
}

func useFakeStagingMounts(t *testing.T, stageErr error, stageMounts bool) *fakeStagingMounts {
	t.Helper()
	f := &fakeStagingMounts{mounted: map[string]bool{}}
	origIsMountPoint, origStage, origBind, origUnmount := isMountPoint, stageNFS, bindMountDevice, unmountFilesystem
	t.Cleanup(func() {
		isMountPoint, stageNFS, bindMountDevice, unmountFilesystem = origIsMountPoint, origStage, origBind, origUnmount
	})
	isMountPoint = func(path string) (bool, error) { return f.mounted[path], nil }
	stageNFS = func(_ *CSIDriver, _ context.Context, volumeID, stagingTarget string, _ []string, _ map[string]string) error {
		f.staged = append(f.staged, volumeID)
		if stageErr != nil {
			return stageErr
		}
		f.mounted[stagingTarget] = stageMounts
		return nil
	}
	bindMountDevice = func(source, target string) error {
		f.binds = append(f.binds, [2]string{source, target})
		return nil
	}
	unmountFilesystem = func(_ context.Context, path string) error {
		f.unmounted = append(f.unmounted, path)
		delete(f.mounted, path)
		return nil
	}
	return f
}

// After an upgrade from the root-export layout, kubelet still treats a volume
// in use on the node as staged and publishes a new pod without calling
// NodeStageVolume. Publish must stage the volume itself and bind from it.
func TestPublishStagesVolumeLeftUnstagedByOlderDriver(t *testing.T) {
	f := useFakeStagingMounts(t, nil, true)
	d := &CSIDriver{}
	staging, target := t.TempDir(), t.TempDir()+"/pod"

	if err := d.publishShareBackedVolume(context.Background(), "/share", staging, target, nil, false, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(f.staged) != 1 || f.staged[0] != "/share" {
		t.Fatalf("publish should have staged the volume once, staged %v", f.staged)
	}
	if len(f.binds) != 1 || f.binds[0] != [2]string{staging, target} {
		t.Fatalf("publish should bind the staging mount into the pod, got %v", f.binds)
	}
}

func TestPublishDoesNotRestageMountedStagingPath(t *testing.T) {
	f := useFakeStagingMounts(t, nil, true)
	d := &CSIDriver{}
	staging, target := t.TempDir(), t.TempDir()+"/pod"
	f.mounted[staging] = true

	if err := d.publishShareBackedVolume(context.Background(), "/share", staging, target, nil, false, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(f.staged) != 0 {
		t.Fatalf("a staged volume must not be staged again, staged %v", f.staged)
	}
}

// The data-loss guard stays: if staging fails, or leaves nothing mounted,
// publish must not bind the empty local directory into the pod.
func TestPublishRefusesUnbackedStagingPath(t *testing.T) {
	for name, tc := range map[string]struct {
		stageErr    error
		stageMounts bool
		code        codes.Code
	}{
		"stage fails":          {status.Error(codes.Internal, "mount failed"), false, codes.Internal},
		"stage mounts nothing": {nil, false, codes.FailedPrecondition},
	} {
		t.Run(name, func(t *testing.T) {
			f := useFakeStagingMounts(t, tc.stageErr, tc.stageMounts)
			d := &CSIDriver{}
			err := d.publishShareBackedVolume(context.Background(), "/share", t.TempDir(), t.TempDir()+"/pod", nil, false, nil)
			if status.Code(err) != tc.code {
				t.Fatalf("want %v, got %v", tc.code, err)
			}
			if len(f.binds) != 0 {
				t.Fatalf("must not bind an unbacked staging path, bound %v", f.binds)
			}
		})
	}
}

// A volume staged by an older driver and staged again by publish has both a
// staging mount and a root-export marker. Unstage must release both, and
// unmount the root export only once no marker is left.
func TestUnstageReleasesRootExportAfterRestagedVolume(t *testing.T) {
	origMarkers := common.BaseVolumeMarkerSourcePath
	common.BaseVolumeMarkerSourcePath = t.TempDir()
	defer func() { common.BaseVolumeMarkerSourcePath = origMarkers }()

	f := useFakeStagingMounts(t, nil, true)
	d := &CSIDriver{volumeLocks: make(map[string]*keyLock)}
	for _, volumeID := range []string{"/share-a", "/share-b"} {
		if err := os.WriteFile(GetHashedMarkerPath(common.BaseVolumeMarkerSourcePath, volumeID), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	stagingA := t.TempDir()
	f.mounted[stagingA] = true
	if _, err := d.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: "/share-a", StagingTargetPath: stagingA}); err != nil {
		t.Fatalf("unstage a: %v", err)
	}
	if _, err := os.Stat(GetHashedMarkerPath(common.BaseVolumeMarkerSourcePath, "/share-a")); !os.IsNotExist(err) {
		t.Fatalf("unstage must remove the marker of a restaged volume: %v", err)
	}
	if len(f.unmounted) != 1 || f.unmounted[0] != stagingA {
		t.Fatalf("root export must stay while another legacy volume uses it, unmounted %v", f.unmounted)
	}

	// share-b was never restaged: its staging path is empty.
	if _, err := d.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{VolumeId: "/share-b", StagingTargetPath: t.TempDir()}); err != nil {
		t.Fatalf("unstage b: %v", err)
	}
	if len(f.unmounted) != 2 || f.unmounted[1] != common.BaseBackingShareMountPath {
		t.Fatalf("the last legacy volume must unmount the root export, unmounted %v", f.unmounted)
	}
}

// A stat that never returns, as on a hard NFS mount whose server is gone,
// must not hang NodeGetVolumeStats, and repeated calls must share one stuck
// probe rather than start another each time.
func TestVolumeStatsBoundedOnHungMount(t *testing.T) {
	origStat, origTimeout := statVolumePath, volumeStatsTimeout
	release := make(chan struct{})
	var calls int32
	statVolumePath = func(string) (os.FileInfo, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil, os.ErrNotExist
	}
	volumeStatsTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		close(release)
		statVolumePath, volumeStatsTimeout = origStat, origTimeout
	})

	d := &CSIDriver{}
	req := &csi.NodeGetVolumeStatsRequest{VolumeId: "/share", VolumePath: "/hung/volume"}
	for i := 0; i < 3; i++ {
		if _, err := d.NodeGetVolumeStats(context.Background(), req); status.Code(err) != codes.Unavailable {
			t.Fatalf("call %d: want Unavailable, got %v", i, err)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("repeated stats calls should share one stuck probe, started %d", n)
	}
}

func TestMountSlotTimesOutWhenAllSlotsBusy(t *testing.T) {
	d := &CSIDriver{mountSlots: semaphore.NewWeighted(1)}
	release, err := d.acquireMountSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := d.acquireMountSlot(ctx); status.Code(err) != codes.Aborted {
		t.Fatalf("want Aborted while the only slot is held, got %v", err)
	}
	release()
	again, err := d.acquireMountSlot(context.Background())
	if err != nil {
		t.Fatalf("slot should be free after release: %v", err)
	}
	again()
}

func TestNodeEnvLimits(t *testing.T) {
	for value, want := range map[string]int64{"": defaultMaxConcurrentNFSMounts, "4": 4, "0": defaultMaxConcurrentNFSMounts, "x": defaultMaxConcurrentNFSMounts} {
		t.Setenv("MAX_CONCURRENT_NFS_MOUNTS", value)
		if got := maxConcurrentNFSMounts(); got != want {
			t.Errorf("MAX_CONCURRENT_NFS_MOUNTS=%q: want %d, got %d", value, want, got)
		}
	}
	for value, want := range map[string]int64{"": 0, "300": 300, "-1": 0, "x": 0} {
		t.Setenv("MAX_VOLUMES_PER_NODE", value)
		if got := maxVolumesPerNode(); got != want {
			t.Errorf("MAX_VOLUMES_PER_NODE=%q: want %d, got %d", value, want, got)
		}
	}
}

// A cached export list is reused, but a share newer than the cached list must
// still be found by refetching it.
func TestCachedNFSExportsRefetchesForNewShare(t *testing.T) {
	orig := getNFSExports
	t.Cleanup(func() { getNFSExports = orig })
	fetches := 0
	exports := []string{"/old"}
	getNFSExports = func(string) ([]string, error) {
		fetches++
		return append([]string{}, exports...), nil
	}
	addr := "exports-cache-test-" + t.Name()

	if _, cached, _ := cachedNFSExports(addr, false); cached {
		t.Fatal("first fetch cannot come from the cache")
	}
	list, cached, _ := cachedNFSExports(addr, false)
	if !cached || fetches != 1 {
		t.Fatalf("second fetch should be cached, cached=%v fetches=%d", cached, fetches)
	}
	if matchNFSExport(list, addr, "/new", "") != "" {
		t.Fatal("the cached list predates /new")
	}
	exports = append(exports, "/new")
	list, _, _ = cachedNFSExports(addr, true)
	if got := matchNFSExport(list, addr, "/new", "/dir"); got != addr+":/new/dir" {
		t.Fatalf("fresh fetch should find /new, got %q", got)
	}

	getNFSExports = func(string) ([]string, error) { return nil, errors.New("down") }
	if _, _, err := cachedNFSExports(addr, true); err == nil {
		t.Fatal("a failed fetch must be reported")
	}
}
