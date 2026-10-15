/*
Copyright 2019 Hammerspace

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"context"

	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"

	common "github.com/hammer-space/csi-plugin/pkg/common"
)

var (
	maxRetries    int           = 5
	retryInterval time.Duration = 1 * time.Second
)

func init() {
	retryCountStr := os.Getenv("UNMOUNT_RETRY_COUNT")
	if retryCountStr != "" {
		if count, err := strconv.Atoi(retryCountStr); err == nil && count >= 0 {
			maxRetries = count
		} else {
			log.Warnf("Invalid UNMOUNT_RETRY_COUNT=%s; using default %d", retryCountStr, maxRetries)
		}
	}

	retryIntervalStr := os.Getenv("UNMOUNT_RETRY_INTERVAL")
	if retryIntervalStr != "" {
		if interval, err := time.ParseDuration(retryIntervalStr); err == nil && interval >= 0 {
			retryInterval = interval
		} else {
			log.Warnf("Invalid UNMOUNT_RETRY_INTERVAL=%s; using default %s", retryIntervalStr, retryInterval)
		}
	}

	log.Infof("Unmount retry config: maxRetries=%d, retryInterval=%s", maxRetries, retryInterval)
}

func IsBlockDevice(fileInfo os.FileInfo) bool {
	mode := fileInfo.Mode()
	return mode&os.ModeDevice != 0 && mode&os.ModeCharDevice == 0
}

func GetFreeLoopDevice() (string, error) {
	output, err := common.ExecCommand("losetup", "-f")
	if err != nil {
		return "", fmt.Errorf("failed to get free loop device: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func AttachLoopDevice(filePath string, readOnly bool) (string, error) {
	deviceStr, err := GetFreeLoopDevice()
	if err != nil {
		return "", err
	}

	flags := []string{}
	if readOnly {
		flags = append(flags, "-r")
	}
	flags = append(flags, deviceStr, filePath)

	output, err := common.ExecCommand("losetup", flags...)

	if err != nil {
		return "", fmt.Errorf("losetup failed: %s, %w", string(output), err)
	}

	return deviceStr, nil
}

// AttachLoopDeviceWithRetry binds a loop device to a filePath with retry support for EBUSY
func AttachLoopDeviceWithRetry(filePath string, readOnly bool) (string, error) {
	log.Debugf("Recived request to AttachLoopDeviceWithRetry for filepath %s", filePath)
	// Step 1: Check if already attached
	output, err := common.ExecCommand("losetup", "-j", filePath)
	if err == nil && strings.TrimSpace(string(output)) != "" {
		// Example output: "/dev/loop3: [12345]:123 (/path/to/file)"
		fields := strings.Split(string(output), ":")
		if len(fields) > 0 {
			device := strings.TrimSpace(fields[0])
			log.Infof("Backing file %s already attached to loop device %s", filePath, device)
			return device, nil
		}
	}

	// 3. Create loop device if missing
	deviceStr, err := GetFreeLoopDevice()
	if err != nil {
		log.Errorf("Will not retry [GetFreeLoopDevice] recived an error. %v", err)
		return "", err
	}
	if _, err := os.Stat(deviceStr); os.IsNotExist(err) {
		major := 7
		minor, err := common.GetDeviceMinorNumber(deviceStr)
		if err != nil {
			log.Debugf("Unable to parse lopp device minor number from %s", deviceStr)
		}
		_, err = common.ExecCommand("mknod", "-m660", deviceStr, "b", strconv.Itoa(major), strconv.Itoa(int(minor)))
		if err != nil {
			return "", fmt.Errorf("failed to create loop device: %v", err)
		}
	}

	// Step 2: Attach using losetup
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		deviceStr, err := AttachLoopDevice(filePath, readOnly)
		if err != nil {
			log.Errorf("Not able to attach the loop device, Err %v", err)
			// retry if device is busy
			if strings.Contains(err.Error(), "busy") {
				log.Warnf("losetup attempt %d failed: %v", i+1, err)
				lastErr = fmt.Errorf("device busy on attempt %d: %w", i+1, err)
				time.Sleep(retryInterval)
				continue
			}
			// Other error → return immediately
			return "", err
		}
		return deviceStr, nil
	}

	return "", fmt.Errorf("failed to attach loop device for %s after %d retries: %w", filePath, maxRetries, lastErr)
}

// CleanupLoopDevice detaches a loop device if it exists
func CleanupLoopDevice(ctx context.Context, dev string) {
	_, span := tracer.Start(ctx, "CleanupLoopDevice", trace.WithAttributes(
		attribute.String("device", dev),
	))
	defer span.End()
	if _, err := os.Stat(dev); os.IsNotExist(err) {
		log.Warnf("Loop device %s does not exist, skipping cleanup", dev)
		return
	}

	for i := 0; i < maxRetries; i++ {
		out, err := common.ExecCommand("losetup", "-d", dev)
		if err == nil {
			log.Infof("Loop device %s detached successfully", dev)
			return
		}
		log.Warnf("Attempt %d: Failed to detach loop device %s: %v. Output: %s", i+1, dev, err, string(out))
		time.Sleep(retryInterval)
	}

	log.Errorf("Failed to detach loop device %s after %d retries", dev, maxRetries)
}

func IsValueInList(value string, list []string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func GetVolumeNameFromPath(path string) string {
	return filepath.Base(path)
}

// shareBackedSnapshotRestoreSupported gates RESTORE of a snapshot into a
// SHARE-BACKED (native NFS) volume. It deliberately does NOT gate snapshot
// CREATION: taking a share snapshot works, and snapshots of NFS volumes remain
// available for backup and for deletion.
//
// Only the restore direction is broken. CreateShareFromSnapshot clones the
// snapshot INTO the source share, creating no share of its own, so a restored
// NFS volume has no quota and silently reports the SOURCE share's capacity
// rather than the size the PVC asked for. Shipping that would hand users a
// volume whose size is quietly wrong, so CreateVolume declines a share-backed
// snapshot source up front with Unimplemented.
//
// File-backed and block volumes are unaffected in both directions -- their
// snapshots are file snapshots and restore correctly, including to a larger
// size (see growRestoredDeviceFile).
//
// The certification manifests set snapshotDataSource: false for the NFS profile
// to match: that capability specifically means "can create a volume from a
// snapshot", i.e. restore.
//
// The restore implementation (CreateShareFromSnapshot and the restore branch of
// ensureShareBackedVolumeExists) is kept intact, so flipping this to true is all
// that is needed once the backend can restore a share snapshot into a share of
// its own.
const shareBackedSnapshotRestoreSupported = false

// isFileBackedVolumeID reports, with no REST call, whether a volume ID refers to
// a file-backed volume. CreateVolume builds file-backed IDs as
// "<SharePathPrefix><backingShare>/<file>" - an extra path segment living inside
// the backing share - and share-backed IDs as "<SharePathPrefix><share>", a
// single segment directly under the prefix. So a volume is file-backed exactly
// when its ID sits one level below the share-path prefix. Deciding this from the
// ID lets callers skip the GetShare probe that, for file-backed volumes, always
// returns 404.
func isFileBackedVolumeID(volumeID string) bool {
	return filepath.Dir(volumeID) != common.SharePathPrefix
}

func GetSnapshotNameFromSnapshotId(snapshotId string) (string, error) {
	tokens := strings.Split(snapshotId, "|")
	if len(tokens) < 2 {
		return "", fmt.Errorf(common.ImproperlyFormattedSnapshotId, snapshotId)
	}
	return tokens[0], nil
}

func GetSnapshotSourceVolumeId(snapshotId string) (string, error) {
	tokens := strings.Split(snapshotId, "|")
	if len(tokens) < 2 {
		return "", fmt.Errorf(common.ImproperlyFormattedSnapshotId, snapshotId)
	}
	return tokens[1], nil
}

func GetShareNameFromSnapshotId(snapshotId string) (string, error) {
	tokens := strings.Split(snapshotId, "|")
	if len(tokens) < 2 {
		return "", fmt.Errorf(common.ImproperlyFormattedSnapshotId, snapshotId)
	}
	return path.Base(tokens[1]), nil
}

// generate snapshot ID to be stored by the CO
// <created snapshot name>|<sharepath or filepath>
func GetSnapshotIDFromSnapshotName(hsSnapName, sourceVolumeID string) string {
	return fmt.Sprintf("%s|%s", hsSnapName, sourceVolumeID)
}

// mountState classifies an existing backing-share staging mount.
type mountState int

const (
	mountHealthy mountState = iota // cleanly mounted; reuse it
	mountAbsent                    // nothing mounted; just mount
	mountStale                     // confirmed unreachable; force-clear then mount
)

// mountStaleProbes is how many CONSECUTIVE SafeIsMountPoint timeouts we require
// before treating a backing mount as stale and force-unmounting it. A single
// timeout is more likely a slow-but-healthy NFS stat under the concurrency this
// driver now allows than a dead server; only a run of timeouts indicates the
// server is actually gone. Kept small so a genuinely dead mount is still cleared
// promptly.
const mountStaleProbes = 2

// classifyMount probes path up to `probes` times to decide whether a backing
// mount is healthy, absent, or stale. `probe` is common.SafeIsMountPoint in
// production and is injectable for tests; it returns (mounted, nil) when it can
// answer, an os.ErrNotExist error when the path does not exist, and
// context.DeadlineExceeded on timeout. "stale" is concluded only after `probes`
// consecutive timeouts, so a live-but-slow shared mount is never force-unmounted
// out from under in-flight pods on a false positive.
func classifyMount(path string, probes int, probe func(string) (bool, error)) mountState {
	for i := 0; i < probes; i++ {
		mounted, err := probe(path)
		if err == nil {
			if mounted {
				return mountHealthy
			}
			return mountAbsent
		}
		if os.IsNotExist(err) {
			return mountAbsent
		}
		// timeout / other transient error — retry
	}
	return mountStale
}

func (d *CSIDriver) EnsureBackingShareMounted(ctx context.Context, backingShareName string, hsVol *common.HSVolume) error {
	return d.ensureBackingShareMounted(ctx, backingShareName, hsVol, false)
}

// ensureBackingShareMounted mounts the backing share if needed. refHeld means
// the caller (acquireBackingMount) already holds the share's mount lock and one
// mountRefs reference; every other caller is serialized here on that lock, so
// two callers can't both replace a foreign mount and unmount each other's.
func (d *CSIDriver) ensureBackingShareMounted(ctx context.Context, backingShareName string, hsVol *common.HSVolume, refHeld bool) error {
	backingShare, err := d.hsclient.GetShare(ctx, backingShareName)
	if err != nil {
		return status.Errorf(codes.NotFound, "%s", err.Error())
	}
	if backingShare != nil {
		backingDir := common.ShareStagingDir + backingShare.ExportPath
		if !refHeld {
			ml := d.mountLockFor(backingDir)
			ml.Lock()
			defer ml.Unlock()
		}
		// Classify the existing mount. We only force-unmount when it is CONFIRMED
		// stale (mountStaleProbes consecutive mount-check timeouts) — never on a
		// single timeout, which can be a slow-but-healthy stat under load and
		// would otherwise `umount -f -l` a live shared backing mount out from
		// under other in-flight file-backed pods (the same outage this path is
		// meant to prevent, via a false positive).
		state := classifyMount(backingDir, mountStaleProbes, isMountPoint)
		log.Debugf("Checked mount for %s: state=%d", backingDir, state)
		switch state {
		case mountHealthy:
			server := d.foreignBackingMountServer(ctx, backingDir, hsVol.FQDN)
			if server == "" {
				log.Infof("backing share already mounted, %s", backingDir)
				return nil
			}
			// Reusing this mount would create and read volume files on another
			// Anvil's share. Replace it, unless volumes on it are still attached
			// or another operation holds a reference: an in-flight create has no
			// loop device until its mkfs is done.
			others := d.backingMountRefs(backingDir)
			if refHeld {
				others--
			}
			inUse := others > 0
			if !inUse {
				if inUse, err = backingFilesInUse(backingDir); err != nil {
					return err
				}
			}
			if inUse {
				return status.Errorf(codes.FailedPrecondition,
					"backing share %s is mounted at %s from %s, which is not an NFS server of the current Anvil, and loop devices still use it; unmount it once no pod uses volumes from that server",
					backingShare.ExportPath, backingDir, server)
			}
			log.Warnf("backing share %s is mounted from %s, which is not an NFS server of the current Anvil; remounting", backingDir, server)
			if err := unmountFilesystem(ctx, backingDir); err != nil {
				return err
			}
		case mountStale:
			// A hung/stale NFS mount is lingering (server unreachable). Force-clear
			// it best-effort so the mount below re-establishes against the CURRENT
			// data portal rather than reusing a dead mount — this is what makes
			// file-backed provisioning survive an Anvil swap.
			log.Warnf("backing share %s appears stale after %d consecutive mount-check timeouts; force-clearing before remount", backingDir, mountStaleProbes)
			common.ForceUnmountStale(backingDir)
		case mountAbsent:
			// nothing mounted here; fall through to mount
		}
		// This mount is shared by every file-backed and block volume on the share,
		// whose StorageClass mountOptions are, by design, the backing share's NFS
		// options. Nested NFS volumes never publish from it: they get their own
		// mount (stageNestedNFSVolume), and creating their directory mounts this
		// share with no volume options (ensureNFSDirectoryExists).
		if err := d.MountShareAtBestDataportal(ctx, backingShare.ExportPath, backingDir, hsVol.MountFlags, hsVol.FQDN); err != nil {
			log.Errorf("failed to mount backing share, %v", err)
			return err
		}
		log.Infof("mounted backing share, %s", backingDir)
		return nil
	}
	return nil
}

// Backing-mount inspection, as variables so tests can replace the mount
// table, the Anvil and losetup.
var (
	backingMountServer = mountedNFSServer
	nfsServerAddresses = func(d *CSIDriver, ctx context.Context) ([]string, error) {
		return d.hsclient.NFSServerAddresses(ctx)
	}
	backingFilesInUse = loopDevicesUnder
	lookupIP          = net.LookupIP
)

// foreignBackingMountServer returns the server of the NFS mount at path when
// the driver would not mount from it now: it is not an up data portal, a
// floating IP, or the StorageClass FQDN. This happens when the driver is
// pointed at a new Anvil while the old one is still reachable, so the mount
// still looks healthy. Mounts made on the host reach the driver through its
// /tmp host path, so a new driver pod can inherit one.
//
// It returns "" when the mount is current, and also when the Anvil can't be
// asked, so an Anvil outage never takes down a working mount.
func (d *CSIDriver) foreignBackingMountServer(ctx context.Context, path, fqdn string) string {
	server, err := backingMountServer(path)
	if err != nil || server == "" {
		return ""
	}
	addresses, err := nfsServerAddresses(d, ctx)
	if err != nil {
		log.Warnf("could not list the Anvil's NFS servers, keeping the mount at %s: %v", path, err)
		return ""
	}
	if fqdn != "" {
		if ips, err := lookupIP(fqdn); err == nil {
			for _, ip := range ips {
				addresses = append(addresses, ip.String())
			}
		}
	}
	if len(addresses) == 0 {
		// Nothing to compare against; the mount below couldn't pick a server either.
		return ""
	}
	if serverIn(server, addresses) {
		return ""
	}
	return server
}

// serverIn reports whether server, an IP or host name, is one of addresses.
func serverIn(server string, addresses []string) bool {
	candidates := []net.IP{net.ParseIP(server)}
	if candidates[0] == nil {
		ips, err := lookupIP(server)
		if err != nil {
			// Can't tell; treat it as current rather than unmount it.
			return true
		}
		candidates = ips
	}
	for _, a := range addresses {
		ip := net.ParseIP(a)
		for _, c := range candidates {
			if ip != nil && ip.Equal(c) {
				return true
			}
		}
	}
	return false
}

// mountedNFSServer returns the server of the topmost NFS mount at path, or ""
// when no NFS filesystem is mounted there. It reads the mount table and never
// touches the mount, so a dead server can't hang it.
func mountedNFSServer(path string) (string, error) {
	mounts, err := mount.New("").List()
	if err != nil {
		return "", err
	}
	server := ""
	for _, m := range mounts {
		// Later entries are mounted on top of earlier ones.
		if m.Path == path && strings.HasPrefix(m.Type, "nfs") {
			server = nfsSourceHost(m.Device)
		}
	}
	return server, nil
}

// nfsSourceHost returns the host part of an NFS mount source such as
// "10.0.0.1:/share" or "[fd00::1]:/share".
func nfsSourceHost(source string) string {
	i := strings.Index(source, ":/")
	if i <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(source[:i], "["), "]")
}

// loopDevicesUnder reports whether any loop device is backed by a file under
// mountPath.
func loopDevicesUnder(mountPath string) (bool, error) {
	output, err := common.ExecCommand("losetup", "-a")
	if err != nil {
		return false, status.Errorf(codes.Internal,
			"could not list backing files for loop devices, %v", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, " ")
		backingFile := strings.Trim(fields[len(fields)-1], ":()")
		if strings.HasPrefix(backingFile, mountPath+"/") {
			log.Infof("backing share, %s, still in use by, %s", mountPath, line)
			return true, nil
		}
	}
	return false, nil
}

// mountLockFor returns the per-backing-directory lock that serializes the actual
// mount/unmount of that share. Using a distinct lock per directory (instead of the
// global mountRefsMu) is what lets the slow NFS mount run without freezing refcount
// operations on other shares. The lock is created on first use and never deleted —
// there are only a handful of distinct backing shares, so the map stays tiny, and
// keeping entries avoids a delete-vs-lookup race. The global mountRefsMu is held
// only to look up/insert the entry (microseconds).
func (d *CSIDriver) mountLockFor(backingDir string) *sync.Mutex {
	d.mountRefsMu.Lock()
	defer d.mountRefsMu.Unlock()
	ml, ok := d.mountLocks[backingDir]
	if !ok {
		ml = &sync.Mutex{}
		d.mountLocks[backingDir] = ml
	}
	return ml
}

// acquireBackingMount guarantees the backing share is mounted and takes one
// in-flight reference on it. It must be called WITHOUT the per-backing-share volume
// lock held, so the caller's subsequent per-file work (mkfs) runs concurrently with
// other creates on the same share.
//
// The mount is serialized by a PER-DIRECTORY lock (mountLockFor), never by the
// global mountRefsMu. EnsureBackingShareMounted performs a real NFS mount that can
// block for up to the ~5 min command-exec timeout against a slow or dead portal.
// The previous code held mountRefsMu across that mount, so a single slow mount froze
// EVERY other file-backed operation — including refcount reads (backingMountInUse)
// and releases on unrelated shares — for the whole mount window. With a per-dir
// lock, a slow mount on one share only blocks new mounts of THAT share; mountRefsMu
// is taken only for the microsecond map updates below.
//
// The reference is reserved BEFORE mounting (bumpBackingRef, so the count is >=1
// throughout the mount). That closes a window the old global-lock design covered
// incidentally: a concurrent delete's UnmountBackingShareIfUnused must not observe
// refcount 0 mid-mount and unmount the share out from under us. The per-dir lock
// additionally guarantees two concurrent first-creates can't both mount the target.
func (d *CSIDriver) acquireBackingMount(ctx context.Context, backingShare *common.ShareResponse, hsVol *common.HSVolume) error {
	backingDir := common.ShareStagingDir + backingShare.ExportPath

	ml := d.mountLockFor(backingDir)
	ml.Lock()
	defer ml.Unlock()

	// Reserve the reference first; `first` is true only on the 0->1 transition.
	if first := d.bumpBackingRef(backingDir); first {
		if err := d.ensureBackingShareMounted(ctx, backingShare.Name, hsVol, true); err != nil {
			// Roll back the reservation so a failed mount doesn't leak a reference
			// that would keep the (unmounted) share pinned as "in use" forever.
			d.dropBackingRef(backingDir)
			return err
		}
	}
	return nil
}

// releaseBackingMount drops one in-flight reference taken by acquireBackingMount
// and unmounts the backing share once the last concurrent file operation using it
// has finished (refcount reaches 0). UnmountBackingShareIfUnused still applies its
// own loopback-device safety check before actually unmounting.
func (d *CSIDriver) releaseBackingMount(ctx context.Context, backingShare *common.ShareResponse) {
	backingDir := common.ShareStagingDir + backingShare.ExportPath

	// Take the SAME per-dir lock as acquireBackingMount so this unmount can't race a
	// concurrent (re)mount of the same target, and — as in acquire — so the slow
	// unmount runs under the per-dir lock rather than the global mountRefsMu.
	ml := d.mountLockFor(backingDir)
	ml.Lock()
	defer ml.Unlock()

	// Decide-then-act: drop the refcount under the global mutex (dropBackingRef holds
	// it only for the decrement), then run the unmount with the global mutex RELEASED.
	// UnmountBackingShareIfUnused re-acquires the global mutex via backingMountInUse,
	// and sync.Mutex is not reentrant, so holding it across the call self-deadlocks
	// the goroutine — the bug found during live xfs validation, which then never
	// released mountRefsMu (or the volume lock above it) and wedged all file-backed
	// creates. It only triggers for whichever volume drops the LAST reference.
	if !d.dropBackingRef(backingDir) {
		return
	}
	// Because we hold ml, no acquireBackingMount for this dir can re-mount until we
	// return; UnmountBackingShareIfUnused still re-checks backingMountInUse (belt and
	// suspenders, and to stay correct against the direct, non-ml unmount callers).
	if _, err := d.UnmountBackingShareIfUnused(ctx, backingShare.Name); err != nil {
		log.Warnf("releaseBackingMount: unmount of %s failed: %v", backingDir, err)
	}
}

// bumpBackingRef adds one in-flight reference for backingDir under mountRefsMu and
// reports whether this was the 0->1 transition (i.e. the caller is responsible for
// mounting). It holds mountRefsMu only for the increment.
func (d *CSIDriver) bumpBackingRef(backingDir string) (first bool) {
	d.mountRefsMu.Lock()
	defer d.mountRefsMu.Unlock()
	first = d.mountRefs[backingDir] == 0
	d.mountRefs[backingDir]++
	return first
}

// dropBackingRef decrements the in-flight refcount for backingDir under
// mountRefsMu and reports whether this was the final reference (the count
// reached 0, and the map entry was removed). It performs NO unmount and holds
// mountRefsMu only for the decrement itself, so the caller can run the
// same-mutex-taking unmount decision afterwards without self-deadlocking.
func (d *CSIDriver) dropBackingRef(backingDir string) (last bool) {
	d.mountRefsMu.Lock()
	defer d.mountRefsMu.Unlock()
	if d.mountRefs[backingDir] > 0 {
		d.mountRefs[backingDir]--
	}
	if d.mountRefs[backingDir] == 0 {
		delete(d.mountRefs, backingDir)
		return true
	}
	return false
}

// backingMountInUse reports whether any in-flight file-backed operation still
// holds a reference to the backing-share staging mount at mountPath (see
// acquire/releaseBackingMount). It is the authoritative "in use" signal shared
// with UnmountBackingShareIfUnused so the two mechanisms can't disagree and
// unmount a share out from under an in-flight mkfs.
func (d *CSIDriver) backingMountInUse(mountPath string) bool {
	return d.backingMountRefs(mountPath) > 0
}

// backingMountRefs returns the number of in-flight references on the
// backing-share staging mount at mountPath.
func (d *CSIDriver) backingMountRefs(mountPath string) int {
	d.mountRefsMu.Lock()
	defer d.mountRefsMu.Unlock()
	return d.mountRefs[mountPath]
}

func (d *CSIDriver) UnmountBackingShareIfUnused(ctx context.Context, backingShareName string) (bool, error) {
	ctx, span := tracer.Start(ctx, "UnmountBackingShareIfUnused", trace.WithAttributes(
		attribute.String("backing_share", backingShareName),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "UnmountBackingShareIfUnused")(nil)
	log.Debugf("UnmountBackingShareIfUnused is called with backing share name %s", backingShareName)
	backingShare, err := d.hsclient.GetShare(ctx, backingShareName)
	if err != nil || backingShare == nil {
		log.Errorf("unable to get share while checking UnmountBackingShareIfUnused. Err %v", err)
		return false, err
	}
	mountPath := common.ShareStagingDir + backingShare.ExportPath
	// Honor the mountRefs refcount FIRST. A file-backed CreateVolume holds a
	// reference for its whole mkfs/format window (which unit G runs without the
	// per-backing-share lock), and during that window there is no loop device
	// backing the file yet — so the losetup check below would wrongly report
	// "unused" and unmount the share out from under an in-flight mkfs. The
	// refcount is the authoritative in-use signal shared with acquireBackingMount.
	if d.backingMountInUse(mountPath) {
		log.Infof("backing share %s still has in-flight reference(s); not unmounting", mountPath)
		return false, nil
	}
	if isMounted := common.IsShareMounted(mountPath); !isMounted {
		return true, nil
	}
	if inUse, err := backingFilesInUse(mountPath); err != nil || inUse {
		return false, err
	}

	log.Infof("unmounting backing share %s", mountPath)
	err = common.UnmountFilesystem(ctx, mountPath)
	if err != nil {
		log.Errorf("failed to unmount backing share %s", mountPath)
		return false, err
	}

	return true, err
}

// Check to select the IP for mount point
// 1. Check if FQDN is provided and its resolvable. If FQDN is there we use that IP only.
// 2. Check if GetPortalFloatingIp have floating IPS to be used.
// If we have the IP's in list we use that IP only. We select the IP which response first rpcinfo command.
// 3. If all above check is null of err use anvil IP.

func (d *CSIDriver) MountShareAtBestDataportal(ctx context.Context, shareExportPath, targetPath string, mountFlags []string, fqdn string) error {
	return d.mountShareSubPathAtBestDataportal(ctx, shareExportPath, "", targetPath, mountFlags, fqdn)
}

// nfsExportsCacheSeconds is how long a data portal's export list is reused.
const nfsExportsCacheSeconds = 30

// cachedNFSExports returns the exports of the data portal at addr, reusing a
// list fetched in the last nfsExportsCacheSeconds, and reports whether it came
// from the cache. Staging many volumes at once would otherwise run showmount
// against the same portal once per volume. fresh skips the cache.
func cachedNFSExports(addr string, fresh bool) ([]string, bool, error) {
	key := "NFS_EXPORTS:" + addr
	if !fresh {
		if cached, _ := common.GetCacheData(key); cached != nil {
			if exports, ok := cached.([]string); ok {
				return exports, true, nil
			}
		}
	}
	exports, err := getNFSExports(addr)
	if err != nil {
		return nil, false, err
	}
	common.SetCacheData(key, exports, nfsExportsCacheSeconds)
	// DeleteFile refuses to remove a path in this list.
	common.SetCacheData("NFS_EXPORTS", exports, 60*60)
	return exports, false, nil
}

// matchNFSExport returns the mount source for subPath inside the share at
// shareExportPath, or "" when no export under a known prefix matches.
func matchNFSExport(exports []string, addr, shareExportPath, subPath string) string {
	for _, mountPrefix := range common.DefaultDataPortalMountPrefixes {
		for _, e := range exports {
			if e == fmt.Sprintf("%s%s", mountPrefix, shareExportPath) {
				export := fmt.Sprintf("%s:%s%s%s", addr, mountPrefix, shareExportPath, subPath)
				log.Debugf("Found export %s", export)
				return export
			}
		}
	}
	return ""
}

// mountShareSubPathAtBestDataportal mounts subPath, a directory inside the share
// exported at shareExportPath, rather than the share's root. The portal is
// selected by matching shareExportPath against its export list, since a
// directory inside a share is not an export of its own; NFSv3 and v4.1 both
// mount it directly. An empty subPath mounts the whole share.
func (d *CSIDriver) mountShareSubPathAtBestDataportal(ctx context.Context, shareExportPath, subPath, targetPath string, mountFlags []string, fqdn string) error {
	var err error
	var fipaddr string = ""

	log.Debugf("Finding best host exporting %s (mounting %q inside it)", shareExportPath, subPath)

	// Hold a mount slot across portal selection as well as the mount itself:
	// the Anvil and showmount calls are most of the load a burst of mounts puts
	// on the cluster.
	releaseSlot, err := d.acquireMountSlot(ctx)
	if err != nil {
		return err
	}
	defer releaseSlot()

	portals, err := d.hsclient.GetDataPortals(ctx, d.NodeID)
	if err != nil {
		log.WithFields(log.Fields{
			"share":   shareExportPath,
			"target":  targetPath,
			"Node_id": d.NodeID,
		}).Errorf("Could not create list of data-portals")
		return status.Errorf(codes.Internal, "could not create list of data-portals, %v", err)
	}

	extracted_endpoint, err := common.ResolveFQDN(fqdn)
	if err != nil {
		log.Errorf("Not able to resolve FQDN=%s checking floating IP's. Error %v", fqdn, err)
	}
	if extracted_endpoint != "" && err == nil {
		// if fqdn is provided use that ip, no need to check for rpcinfo response time as we are already using fqdn which is expected to be resolved to the right IP by DNS.
		fipaddr = extracted_endpoint
	} else {
		// Always look for floating data portal IPs
		fipaddr, err = d.hsclient.GetPortalFloatingIp(ctx)
		if err != nil {
			log.Errorf("Could not contact Anvil for floating IPs, %v", err)
		}
	}

	// In a no-DSX deployment (e.g. Anvil-only) GetDataPortals returns an empty
	// list, so the portal-bounded mount loops below would never execute and the
	// resolved fipaddr (from the FQDN or floating IP) would be silently thrown
	// away. Synthesize a single portal from fipaddr so the existing mount logic
	// (export discovery via showmount, NFS 4.2 -> 3 fallback) still runs.
	if len(portals) == 0 && fipaddr != "" {
		log.Infof("No data-portals returned by Anvil; using resolved address %s as the mount target", fipaddr)
		portals = []common.DataPortal{
			{
				Node: common.DataPortalNode{
					Name: fqdn,
					MgmtIpAddress: common.DataPortalNodeAddress{
						Address: fipaddr,
					},
				},
				Uoid: map[string]string{"uuid": fipaddr},
			},
		}
	}

	MountToDataPortal := func(portal common.DataPortal, mount_options []string) bool {
		// Kubelet has given up on this RPC and will retry it. Stop walking the
		// portal list so the retry doesn't queue behind this attempt's slot.
		if ctx.Err() != nil {
			return false
		}
		addr := ""
		if len(fipaddr) > 0 {
			addr = fipaddr
			log.Debugf("Floating IP address detected: %s", fipaddr)
		} else {
			addr = portal.Node.MgmtIpAddress.Address
		}
		export := ""
		// Use configured prefix if specified
		if common.DataPortalMountPrefix != "" {
			export = fmt.Sprintf("%s:%s%s%s", addr, common.DataPortalMountPrefix, shareExportPath, subPath)
		} else {
			// grab exports with showmount
			exports, cached, err := cachedNFSExports(addr, false)
			if err != nil {
				log.Debugf("Could not get exports for data-portal at %s, %s. Error: %v", addr, portal.Uoid["uuid"], err)
				return false
			}
			log.Debugf("Found exports for data-portal %s, %v", addr, exports)

			export = matchNFSExport(exports, addr, shareExportPath, subPath)
			if export == "" && cached {
				// The share may be newer than the cached list.
				if exports, _, err = cachedNFSExports(addr, true); err == nil {
					export = matchNFSExport(exports, addr, shareExportPath, subPath)
				}
			}
			if export == "" {
				log.Debugf("Could not find any matching export on data-portal address - %s uuid - %s.", portal.Node.MgmtIpAddress.Address, portal.Uoid["uuid"])
				return false
			}
		}
		err = common.MountShare(ctx, export, targetPath, mount_options)
		if err != nil {
			log.WithFields(log.Fields{
				"share":         shareExportPath,
				"target":        targetPath,
				"portal_name":   portal.Node.Name,
				"portal_ip":     portal.Node.MgmtIpAddress.Address,
				"portal":        portal.Uoid["uuid"],
				"mount_options": mount_options,
			}).Errorf("Could NOT mount share %s to %s ERR %v", shareExportPath, targetPath, err)
		} else {
			log.WithFields(log.Fields{
				"share":         shareExportPath,
				"target":        targetPath,
				"portal_name":   portal.Node.Name,
				"portal_ip":     portal.Node.MgmtIpAddress.Address,
				"portal":        portal.Uoid["uuid"],
				"mount_options": mount_options,
			}).Debugf("Mounted share %s to %s via data-portal %s", shareExportPath, targetPath, portal.Node.Name)
			// If mount is successful, return true
			return true
		}
		return false
	}

	log.Debugf("Attempting to mount with provided mount flags.")
	// Attempt to mount with provided mount flags if they contain nfsvers
	containsNfsvers := false
	for _, flag := range mountFlags {
		if strings.HasPrefix(flag, "nfsvers=") || strings.HasPrefix(flag, "vers=") {
			containsNfsvers = true
			break
		}
	}
	if containsNfsvers {
		for _, p := range portals {
			if MountToDataPortal(p, mountFlags) {
				return nil
			}
		}
		// Remove nfsvers/vers option from mountFlags if mount fails
		var filteredMountFlags []string
		for _, flag := range mountFlags {
			if !strings.HasPrefix(flag, "nfsvers=") && !strings.HasPrefix(flag, "vers=") {
				filteredMountFlags = append(filteredMountFlags, flag)
			}
		}
		mountFlags = filteredMountFlags
		log.Infof("Mount with provided mount flags failed, removed nfsvers/vers option.")
	}

	// Fallback to NFS 4.2
	log.Infof("Provided mount flags do not contain nfsvers option or failed to mount, using default to NFS 4.2.")
	for _, p := range portals {
		if MountToDataPortal(p, append(mountFlags, "nfsvers=4.2")) {
			return nil
		}
	}

	// DELIBERATELY no NFS 4.1 step here, even though a data portal advertised as
	// NFS_V4_1 will happily accept nfsvers=4.1 while rejecting 4.2 with
	// "Protocol not supported".
	//
	// Backing shares mounted here hold the raw files behind file-backed and block
	// volumes, and those are created with `qemu-img create`, which takes an OFD
	// byte-range lock on the file. This backend's NFSv4.1 export does not support
	// byte-range locking, so on a 4.1 mount every create fails with:
	//
	//	qemu-img: Failed to lock byte 101: Unknown error 524   (ENOTSUPP)
	//
	// The v3 fallback below pairs with "nolock", which keeps locking local to the
	// client and lets qemu-img work. Verified 2026-09-23 by running qemu-img
	// against the same share over both versions: 4.1 fails, v3 succeeds. Adding a
	// 4.1 step here therefore breaks ALL file-backed provisioning -- do not
	// reintroduce it without first removing the driver's dependence on
	// qemu-img's locking (for example by creating the raw file with truncate).

	// Fallback to NFS 3
	log.Infof("Could not mount via NFS 4.2, falling back to NFS 3.")
	for _, p := range portals {
		if MountToDataPortal(p, append(mountFlags, "nfsvers=3", "nolock")) {
			return nil
		}
	}

	return fmt.Errorf("could not mount to any data-portals")
}

func IsAnyVolumeStillMounted(baseMarkerDir string) bool {
	files, err := os.ReadDir(baseMarkerDir)
	if err != nil {
		return false // Fail safe
	}

	for _, f := range files {
		log.Debugf("volume marker still present at %s", f.Name())
		if strings.HasSuffix(f.Name(), ".marker") {
			return true
		}
	}

	return false
}

func GetHashedMarkerPath(baseDir, volmeID string) string {
	h := sha256.New()
	h.Write([]byte(volmeID))
	hashStr := hex.EncodeToString(h.Sum(nil))

	// Instead of putting marker as a file named ".marker" inside hash directory,
	// create a file named "<hash>.marker" directly inside baseDir
	markerFile := filepath.Join(baseDir, hashStr+".marker")
	return markerFile
}
