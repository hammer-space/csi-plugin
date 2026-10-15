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
	"context"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/hammer-space/csi-plugin/pkg/common"
	"golang.org/x/sync/semaphore"

	log "github.com/sirupsen/logrus"

	"github.com/container-storage-interface/spec/lib/go/csi"
	client "github.com/hammer-space/csi-plugin/pkg/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type CSIDriver struct {
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer
	csi.UnimplementedIdentityServer
	listener      net.Listener
	server        *grpc.Server
	wg            sync.WaitGroup
	running       bool
	locksMu       sync.Mutex
	volumeLocks   map[string]*keyLock
	snapshotLocks map[string]*keyLock
	// mountRefs counts in-flight file operations per backing-share mount. It lets
	// many file-backed CreateVolume calls share a single backing-share mount and run
	// their per-file mkfs concurrently: the share is mounted on the first reference
	// and unmounted only when the last in-flight file operation completes. This
	// replaces holding the coarse per-backing-share lock across the whole per-file
	// create (which serialized all file creation on a share to ~1 at a time).
	mountRefsMu sync.Mutex
	mountRefs   map[string]int
	// mountLocks holds one lock per backing-share staging directory. It serializes
	// the actual mount/unmount of a given backing share so the slow (up to ~5 min)
	// NFS mount never runs under the global mountRefsMu. mountRefsMu then guards only
	// the two maps below and is always held for microseconds; a slow mount on one
	// share can no longer wedge refcount reads (backingMountInUse) or releases on
	// every other in-flight file-backed operation. See acquireBackingMount.
	mountLocks map[string]*sync.Mutex
	// mountSlots caps how many NFS mounts this process runs at once. A node
	// reboot, drain or scale-up stages every NFS volume on the node together;
	// the cap turns that burst into a queue so the node, the data portals and
	// the Anvil REST API see a steady load. Nil means no cap.
	mountSlots *semaphore.Weighted
	hsclient   *client.HammerspaceClient
	NodeID     string
	// freezer runs fsfreeze inside the pod(s) holding a source volume
	// during CreateSnapshot, so XFS reaches a quiesce point before Anvil
	// captures the file bytes. Nil when the driver is not running
	// in-cluster (local dev) — in that case snapshots are still taken but
	// consistency is not enforced.
	freezer *Freezer
}

func NewCSIDriver(endpoint, username, password, tlsVerifyStr string) *CSIDriver {
	tlsVerify := false
	if os.Getenv("HS_TLS_VERIFY") != "" {
		tlsVerify, _ = strconv.ParseBool(tlsVerifyStr)
	} else {
		tlsVerify = false
	}
	client, err := client.NewHammerspaceClient(endpoint, username, password, tlsVerify)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	// We now require mounting through a DSX server
	common.UseAnvil = false

	return &CSIDriver{
		hsclient:      client,
		volumeLocks:   make(map[string]*keyLock),
		snapshotLocks: make(map[string]*keyLock),
		mountRefs:     make(map[string]int),
		mountLocks:    make(map[string]*sync.Mutex),
		mountSlots:    semaphore.NewWeighted(maxConcurrentNFSMounts()),
		NodeID:        os.Getenv("CSI_NODE_NAME"),
		freezer:       NewFreezer(),
	}

}

type keyLock struct {
	sem *semaphore.Weighted // weight=1 → acts like a mutex
}

func newKeyLock() *keyLock {
	return &keyLock{sem: semaphore.NewWeighted(1)}
}

func (kl *keyLock) lock(ctx context.Context) error {
	return kl.sem.Acquire(ctx, 1)
}

func (kl *keyLock) unlock() {
	kl.sem.Release(1)
}

// acquire helpers with timeout + unlock func return
func (c *CSIDriver) acquireVolumeLock(ctx context.Context, volID string) (func(), error) {
	log.Debug("acquireVolumeLock: ", volID)
	c.locksMu.Lock()
	lk, ok := c.volumeLocks[volID]
	if !ok {
		lk = newKeyLock()
		c.volumeLocks[volID] = lk
	}
	c.locksMu.Unlock()

	probe := common.StartLockProbe(ctx, "volume")
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := lk.lock(lctx); err != nil {
		probe.Failed()
		log.WithError(err).Errorf("Error acquiring volume lock for %s", volID)
		return nil, status.Errorf(codes.Aborted, "could not acquire volume lock for %s: %v", volID, err)
	}
	release := probe.Acquired()
	return func() { lk.unlock(); release() }, nil
}

// acquireRootMountLock serializes cleanup of the node-wide root NFS export at
// common.BaseBackingShareMountPath, which driver versions before the
// staged-NFS design mounted at stage time. NodeUnstageVolume holds it while it
// drops an old volume's marker and unmounts the export after the last one, so
// two concurrent unstages cannot both miscount the remaining markers.
//
// It reuses the volume lock map, keyed by the mount path. That key cannot
// collide with a CSI volume ID, which is always a Hammerspace share or file
// path. The lock carries the same 30s timeout as every other lock here, so
// contention surfaces to kubelet as a retryable Aborted instead of a hang.
func (c *CSIDriver) acquireRootMountLock(ctx context.Context) (func(), error) {
	return c.acquireVolumeLock(ctx, common.BaseBackingShareMountPath)
}

// defaultMaxConcurrentNFSMounts is the default for MAX_CONCURRENT_NFS_MOUNTS.
const defaultMaxConcurrentNFSMounts = 10

// maxConcurrentNFSMounts reads MAX_CONCURRENT_NFS_MOUNTS, the number of NFS
// mounts one driver process may run at once.
func maxConcurrentNFSMounts() int64 {
	value := os.Getenv("MAX_CONCURRENT_NFS_MOUNTS")
	if value == "" {
		return defaultMaxConcurrentNFSMounts
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 {
		log.Warnf("Invalid MAX_CONCURRENT_NFS_MOUNTS=%q; using default %d", value, defaultMaxConcurrentNFSMounts)
		return defaultMaxConcurrentNFSMounts
	}
	return n
}

// acquireMountSlot waits for one of the node's NFS mount slots. It waits as
// long as the caller's context allows, which for kubelet is the RPC deadline,
// and then returns Aborted so kubelet retries with its own backoff instead of
// the driver adding a second retry loop.
func (c *CSIDriver) acquireMountSlot(ctx context.Context) (func(), error) {
	if c.mountSlots == nil {
		return func() {}, nil
	}
	probe := common.StartLockProbe(ctx, "mount_slot")
	if err := c.mountSlots.Acquire(ctx, 1); err != nil {
		probe.Failed()
		return nil, status.Errorf(codes.Aborted, "timed out waiting for an NFS mount slot: %v", err)
	}
	release := probe.Acquired()
	return func() { c.mountSlots.Release(1); release() }, nil
}

func (c *CSIDriver) acquireSnapshotLock(ctx context.Context, snapID string) (func(), error) {
	log.Debug("acquireSnapshotLock: ", snapID)
	c.locksMu.Lock()
	lk, ok := c.snapshotLocks[snapID]
	if !ok {
		lk = newKeyLock()
		c.snapshotLocks[snapID] = lk
	}
	c.locksMu.Unlock()

	probe := common.StartLockProbe(ctx, "snapshot")
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := lk.lock(lctx); err != nil {
		probe.Failed()
		log.WithError(err).Errorf("Error acquiring snapshot lock for %s", snapID)
		return nil, status.Errorf(codes.Aborted, "could not acquire snapshot lock for %s: %v", snapID, err)
	}
	release := probe.Acquired()
	return func() { lk.unlock(); release() }, nil
}

func (c *CSIDriver) goServe(started chan<- bool) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		started <- true
		err := c.server.Serve(c.listener)
		if err != nil {
			panic(err.Error())
		}
	}()
}

func (c *CSIDriver) Address() string {
	return c.listener.Addr().String()
}

func (c *CSIDriver) Start(l net.Listener) error {
	c.locksMu.Lock()
	defer c.locksMu.Unlock()

	// Set listener
	c.listener = l

	// Create a new grpc server
	c.server = grpc.NewServer(
		grpc.UnaryInterceptor(c.callInterceptor),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time: 5 * time.Minute,
		}),
	)

	csi.RegisterControllerServer(c.server, c)
	csi.RegisterIdentityServer(c.server, c)
	csi.RegisterNodeServer(c.server, c)
	reflection.Register(c.server)

	// Start listening for requests
	waitForServer := make(chan bool)
	c.goServe(waitForServer)
	<-waitForServer
	c.running = true
	return nil
}

func (c *CSIDriver) Stop() {
	c.locksMu.Lock()
	defer c.locksMu.Unlock()

	if !c.running {
		return
	}

	c.server.Stop()
	c.wg.Wait()
}

func (c *CSIDriver) Close() {
	c.server.Stop()
}

func (c *CSIDriver) GetHammerspaceClient() *client.HammerspaceClient {
	return c.hsclient
}

func (c *CSIDriver) callInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (interface{}, error) {
	rsp, err := handler(ctx, req)
	logGRPC(info.FullMethod, req, rsp, err)
	return rsp, err
}

func logGRPC(method string, request, reply interface{}, err error) {
	fields := log.Fields{
		"grpc_method": method,
		"request":     request,
		"response":    reply,
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	log.WithFields(fields).Debug("gRPC call")
}
