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
