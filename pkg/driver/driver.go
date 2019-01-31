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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/container-storage-interface/spec/lib/go/csi"
	client "github.com/hammer-space/hammerspace-csi-plugin/pkg/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type CSIDriver struct {
	listener      net.Listener
	server        *grpc.Server
	wg            sync.WaitGroup
	running       bool
	lock          sync.Mutex
	volumeLocks   map[string]*sync.Mutex //This only grows and may be a memory issue
	snapshotLocks map[string]*sync.Mutex
	hsclient      *client.HammerspaceClient
	NodeID        string
	UseAnvil      bool
}

func NewCSIDriver(endpoint, username, password, tlsVerifyStr, useAnvilStr string) *CSIDriver {
	tlsVerify := false
	if os.Getenv("HS_TLS_VERIFY") != "" {
		tlsVerify, _ = strconv.ParseBool(tlsVerifyStr)
	} else {
		tlsVerify = true
	}
	client, err := client.NewHammerspaceClient(endpoint, username, password, tlsVerify)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	var useAnvil bool
	if os.Getenv("CSI_USE_ANVIL_FOR_DATA") != "" {
		useAnvil, _ = strconv.ParseBool(useAnvilStr)
	} else {
		useAnvil = true
	}

	return &CSIDriver{
		hsclient:      client,
		volumeLocks:   make(map[string]*sync.Mutex),
		snapshotLocks: make(map[string]*sync.Mutex),
		NodeID:        os.Getenv("CSI_NODE_NAME"),
		UseAnvil:      useAnvil,
	}

}

func (c *CSIDriver) getVolumeLock(volName string) {
	if _, exists := c.volumeLocks[volName]; !exists {
		c.volumeLocks[volName] = &sync.Mutex{}
	}
	c.volumeLocks[volName].Lock()
}

func (c *CSIDriver) releaseVolumeLock(volName string) {
	if _, exists := c.volumeLocks[volName]; exists {
		if exists {
			c.volumeLocks[volName].Unlock()
		}
	}
}

func (c *CSIDriver) getSnapshotLock(volName string) {
	if _, exists := c.snapshotLocks[volName]; !exists {
		c.snapshotLocks[volName] = &sync.Mutex{}
	}
	c.snapshotLocks[volName].Lock()
}

func (c *CSIDriver) releaseSnapshotLock(volName string) {
	if _, exists := c.snapshotLocks[volName]; exists {
		if exists {
			c.snapshotLocks[volName].Unlock()
		}
	}
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
	c.lock.Lock()
	defer c.lock.Unlock()

	// Set listener
	c.listener = l

	// Create a new grpc server
	c.server = grpc.NewServer(
		grpc.UnaryInterceptor(c.callInterceptor),
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
	c.lock.Lock()
	defer c.lock.Unlock()

	if !c.running {
		return
	}

	c.server.Stop()
	c.wg.Wait()
}

func (c *CSIDriver) Close() {
	c.server.Stop()
}

func (c *CSIDriver) IsRunning() bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.running
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
	// Log JSON with the request and response for easier parsing
	logMessage := struct {
		Method   string
		Request  interface{}
		Response interface{}
		Error    string
	}{
		Method:   method,
		Request:  request,
		Response: reply,
	}
	if err != nil {
		logMessage.Error = err.Error()
	}
	msg, _ := json.Marshal(logMessage)
	fmt.Printf("gRPCCall: %s\n", msg)
}
