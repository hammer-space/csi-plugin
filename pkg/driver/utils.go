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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	for _, token := range tokens[2:] {
		if strings.HasPrefix(token, "share:") {
			return strings.TrimPrefix(token, "share:"), nil
		}
		if strings.HasPrefix(token, "files:") {
			return "", nil
		}
	}
	return path.Base(tokens[1]), nil
}

func GetFileSnapshotPathsFromSnapshotId(snapshotId string) ([]string, error) {
	tokens := strings.Split(snapshotId, "|")
	if len(tokens) < 2 {
		return nil, fmt.Errorf(common.ImproperlyFormattedSnapshotId, snapshotId)
	}
	for _, token := range tokens[2:] {
		if strings.HasPrefix(token, "files:") {
			encoded := strings.TrimPrefix(token, "files:")
			payload, err := base64.RawURLEncoding.DecodeString(encoded)
			if err != nil {
				return nil, err
			}
			var fileSnapshotPaths []string
			err = json.Unmarshal(payload, &fileSnapshotPaths)
			return fileSnapshotPaths, err
		}
	}
	return nil, nil
}

// GetFileSnapshotsIDFromSnapshotNames is the inverse of
// GetFileSnapshotPathsFromSnapshotId: it encodes the per-file snapshot paths
// returned by SnapshotFiles into a single snapshot ID, using the first
// snapshot path as the ID's representative name (mirroring SnapshotFile's
// single-file convention) and carrying the full list in a "files:" token.
func GetFileSnapshotsIDFromSnapshotNames(hsSnapNames []string, sourceVolumeID string) (string, error) {
	if len(hsSnapNames) == 0 {
		return "", fmt.Errorf("no file snapshots to encode into a snapshot ID")
	}
	payload, err := json.Marshal(hsSnapNames)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s|%s|files:%s", hsSnapNames[0], sourceVolumeID, encoded), nil
}

func GetBackingShareNameFromPath(volumePath string) string {
	trimmed := strings.Trim(path.Clean(volumePath), "/")
	if trimmed == "" || trimmed == "." {
		return ""
	}
	return strings.Split(trimmed, "/")[0]
}

// generate snapshot ID to be stored by the CO
// <created snapshot name>|<sharepath or filepath>
func GetSnapshotIDFromSnapshotName(hsSnapName, sourceVolumeID string) string {
	return fmt.Sprintf("%s|%s", hsSnapName, sourceVolumeID)
}

// formatCreateVolumeName applies volumeNameFormat (validated upstream to
// contain exactly one "%s") to requestName. For snapshot-restore requests it
// marks the name with restoreVolumeNameSuffix (unless requestName is already
// marked, e.g. a retried restore), and truncates as needed to stay within
// Hammerspace's volume name length limit while preserving that marker.
func formatCreateVolumeName(requestName, volumeNameFormat string, fromSnapshot bool) (string, error) {
	name := requestName
	if fromSnapshot && !strings.Contains(name, "restore") {
		name += restoreVolumeNameSuffix
	}

	if formatted := fmt.Sprintf(volumeNameFormat, name); len(formatted) <= MaxHammerspaceVolumeNameLength {
		return formatted, nil
	}

	overhead := len(fmt.Sprintf(volumeNameFormat, ""))
	maxNameLen := MaxHammerspaceVolumeNameLength - overhead
	if maxNameLen <= 0 {
		return "", fmt.Errorf("volumeNameFormat leaves no room for a volume name within the %d character Hammerspace name limit", MaxHammerspaceVolumeNameLength)
	}

	if fromSnapshot {
		if maxNameLen <= len(restoreVolumeNameSuffix) {
			return "", fmt.Errorf("volumeNameFormat leaves no room for the %q suffix within the %d character Hammerspace name limit", restoreVolumeNameSuffix, MaxHammerspaceVolumeNameLength)
		}
		base := strings.TrimSuffix(name, restoreVolumeNameSuffix)
		maxBaseLen := maxNameLen - len(restoreVolumeNameSuffix)
		if len(base) > maxBaseLen {
			base = base[:maxBaseLen]
		}
		name = base + restoreVolumeNameSuffix
	} else if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}

	return fmt.Sprintf(volumeNameFormat, name), nil
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
	backingShare, err := d.hsclient.GetShare(ctx, backingShareName)
	if err != nil {
		return status.Errorf(codes.NotFound, "%s", err.Error())
	}
	if backingShare != nil {
		backingDir := common.ShareStagingDir + backingShare.ExportPath
		// Classify the existing mount. We only force-unmount when it is CONFIRMED
		// stale (mountStaleProbes consecutive mount-check timeouts) — never on a
		// single timeout, which can be a slow-but-healthy stat under load and
		// would otherwise `umount -f -l` a live shared backing mount out from
		// under other in-flight file-backed pods (the same outage this path is
		// meant to prevent, via a false positive).
		state := classifyMount(backingDir, mountStaleProbes, common.SafeIsMountPoint)
		log.Infof("Checked mount for %s: state=%d", backingDir, state)
		switch state {
		case mountHealthy:
			log.Infof("backing share already mounted, %s", backingDir)
			return nil
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
		if err := d.MountShareAtBestDataportal(ctx, backingShare.ExportPath, backingDir, hsVol.MountFlags, hsVol.FQDN); err != nil {
			log.Errorf("failed to mount backing share, %v", err)
			return err
		}
		log.Infof("mounted backing share, %s", backingDir)
		return nil
	}
	return nil
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
		if err := d.EnsureBackingShareMounted(ctx, backingShare.Name, hsVol); err != nil {
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
	d.mountRefsMu.Lock()
	defer d.mountRefsMu.Unlock()
	return d.mountRefs[mountPath] > 0
}

func (d *CSIDriver) UnmountBackingShareIfUnused(ctx context.Context, backingShareName string) (bool, error) {
	ctx, span := tracer.Start(ctx, "UnmountBackingShareIfUnused", trace.WithAttributes(
		attribute.String("backing_share", backingShareName),
	))
	defer span.End()
	defer common.MeasureOp(ctx, "UnmountBackingShareIfUnused")(nil)
	log.Infof("UnmountBackingShareIfUnused is called with backing share name %s", backingShareName)
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
	// If any loopback devices are using the mount
	output, err := common.ExecCommand("losetup", "-a")
	if err != nil {
		return false, status.Errorf(codes.Internal,
			"could not list backing files for loop devices, %v", err)
	}
	devices := strings.Split(string(output), "\n")
	for _, d := range devices {
		if d != "" {
			device := strings.Split(d, " ")
			backingFile := strings.Trim(device[len(device)-1], ":()")
			if strings.Index(backingFile, mountPath) == 0 {
				log.Infof("backing share, %s, still in use by, %s", mountPath, devices[0])
				return false, nil
			}
		}
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
	var err error
	var fipaddr string = ""

	log.Infof("Finding best host exporting %s", shareExportPath)

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
		addr := ""
		if len(fipaddr) > 0 {
			addr = fipaddr
			log.Infof("Floating IP address detected: %s", fipaddr)
		} else {
			addr = portal.Node.MgmtIpAddress.Address
		}
		export := ""
		// Use configured prefix if specified
		if common.DataPortalMountPrefix != "" {
			export = fmt.Sprintf("%s:%s%s", addr, common.DataPortalMountPrefix, shareExportPath)
		} else {
			// grab exports with showmount
			exports, err := common.GetNFSExports(addr)
			common.SetCacheData("NFS_EXPORTS", exports, 60*60) // keep the exports for an our before auto expire
			if err != nil {
				log.Infof("Could not get exports for data-portal at %s, %s. Error: %v", addr, portal.Uoid["uuid"], err)
				return false
			}
			log.Infof("Found exports for data-portal %s, %v", addr, exports)

			// Check configured prefix
			// Check the default prefixes
			for _, mountPrefix := range common.DefaultDataPortalMountPrefixes {
				for _, e := range exports {
					if e == fmt.Sprintf("%s%s", mountPrefix, shareExportPath) {
						export = fmt.Sprintf("%s:%s%s", addr, mountPrefix, shareExportPath)
						log.Infof("Found export %s", export)
						break
					}
				}
				if export != "" {
					break
				}
			}
			if export == "" {
				log.Infof("Could not find any matching export on data-portal address - %s uuid - %s.", portal.Node.MgmtIpAddress.Address, portal.Uoid["uuid"])
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

	log.Infof("Attempting to mount with provided mount flags.")
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

	// Fallback to NFS 3
	log.Infof("Could not mount via NFS 4.2, falling back to NFS 3.")
	for _, p := range portals {
		if MountToDataPortal(p, append(mountFlags, "nfsvers=3", "nolock")) {
			return nil
		}
	}

	return fmt.Errorf("could not mount to any data-portals")
}

func (d *CSIDriver) EnsureRootExportMounted(ctx context.Context, baseRootDirPath string, mountFlags []string, fqdn string) error {
	var err error

	log.Debugf("Check if %s is already mounted", baseRootDirPath)
	if common.IsShareMounted(baseRootDirPath) {
		log.Debugf("Root dir mount is already mounted at this node on path %s", baseRootDirPath)
		return nil
	}
	log.Debugf("Create dir if %s is not already there.", baseRootDirPath)
	if err := os.MkdirAll(baseRootDirPath, 0755); err != nil {
		return err
	}
	effectiveMountFlags := append([]string{}, mountFlags...)
	hasNfsvers := false
	for _, option := range effectiveMountFlags {
		if strings.HasPrefix(option, "nfsvers=") || strings.HasPrefix(option, "vers=") {
			hasNfsvers = true
			break
		}
	}
	if !hasNfsvers {
		effectiveMountFlags = append(effectiveMountFlags, "nfsvers=4.2")
	}
	// Step 1 - If FQDN is provided try to use that to mount the root share
	if fqdn != "" {
		fqdnEndpointIP, resolveErr := common.ResolveFQDN(fqdn)
		if resolveErr != nil {
			log.Errorf("Unable to resolve FQDN %s for root share mount. %v", fqdn, resolveErr)
		} else {
			log.Debugf("Calling mount via nfs v4.2 using FQDN %s resolved to IP %s to mount (/) on %s", fqdn, fqdnEndpointIP, baseRootDirPath)
			err = common.MountShare(ctx, fqdn+":/", baseRootDirPath, effectiveMountFlags)
			if err == nil {
				log.Debugf("Successfully mounted root share using FQDN %s resolved to IP %s", fqdn, fqdnEndpointIP)
				return nil
			}
			log.Errorf("Unable to mount root share via FQDN %s resolved to IP %s. %v", fqdn, fqdnEndpointIP, err)
		}
	}
	// Step 2 - Get Anvil IP and try to mount with that IP with 4.2, if it fails we will do a fallback to try to mount with other data portals with 4.2 and fallback to 3 if 4.2 fails.
	anvilEndpointIP, err := d.hsclient.GetAnvilPortal()
	if err != nil {
		log.Errorf("Not able to extract anvil endpoint. Err %v", err)
	}
	// Step 3 - Use export ip and path to mount root with 4.2 only.
	log.Debugf("Calling mount via nfs v4.2 using anvil IP %s to mount (/) on %s", anvilEndpointIP, baseRootDirPath)
	err = common.MountShare(ctx, anvilEndpointIP+":/", baseRootDirPath, effectiveMountFlags)
	if err != nil {
		log.Errorf("Unable to mount root share via 4.2 using anvil IP. %v", err)

		// Step 3 - Use fallback
		log.Debugf("Call for mount root share with anvil IP and 4.2 FAILED, now will do a fallback try with other data portals, with fallback to 4.2 and v3")
		err = d.MountShareAtBestDataportal(ctx, "/", baseRootDirPath, mountFlags, fqdn)
		if err != nil {
			log.Errorf("Not able to mount root share to mount point %s. Error %v", baseRootDirPath, err)
			return err
		}
	}

	log.Debugf("Successfully mounted base (/) share at best data portal to mount point %s", baseRootDirPath)
	return err
}

// waitForPathReady waits until the given path exists and is a directory,
// or until the context is done (e.g., timeout or cancellation).
func (d *CSIDriver) WaitForPathReady(ctx context.Context, path string, pollInterval time.Duration) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for path %s to be ready: %v", path, ctx.Err())
		case <-ticker.C:
			stat, err := os.Stat(path)
			if err == nil && stat.IsDir() {
				return nil // Path is ready
			}

			// If path does not exist, continue polling
			if os.IsNotExist(err) {
				continue
			}

			// Unexpected error — return immediately
			if err != nil {
				return fmt.Errorf("error checking path %s: %v", path, err)
			}
		}
	}
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
