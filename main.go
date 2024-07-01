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
package main

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/hammer-space/csi-plugin/pkg/common"

	"github.com/hammer-space/csi-plugin/pkg/driver"
	log "github.com/sirupsen/logrus"
)

func init() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Setup logging
	log.SetFormatter(&log.JSONFormatter{
		PrettyPrint:      true,
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05",
	})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
	log.SetReportCaller(false)
	log.WithContext(ctx)
}

func validateEnvironmentVars() {
	endpoint := os.Getenv("CSI_ENDPOINT")
	if len(endpoint) == 0 {
		log.Error("CSI_ENDPOINT must be defined and must be a path")
		os.Exit(1)
	}
	if strings.Contains(endpoint, ":") {
		log.Error("CSI_ENDPOINT must be a unix path")
		os.Exit(1)
	}

	hsEndpoint := os.Getenv("HS_ENDPOINT")
	if len(hsEndpoint) == 0 {
		log.Error("HS_ENDPOINT must be defined")
		os.Exit(1)
	}

	endpointUrl, err := url.Parse(hsEndpoint)
	if err != nil || endpointUrl.Scheme != "https" || endpointUrl.Host == "" {
		log.Error("HS_ENDPOINT must be a valid HTTPS URL")
		os.Exit(1)
	}

	username := os.Getenv("HS_USERNAME")
	if len(username) == 0 {
		log.Error("HS_USERNAME must be defined")
		os.Exit(1)
	}

	password := os.Getenv("HS_PASSWORD")
	if len(password) == 0 {
		log.Error("HS_PASSWORD must be defined")
		os.Exit(1)
	}

	if os.Getenv("HS_TLS_VERIFY") != "" {
		_, err = strconv.ParseBool(os.Getenv("HS_TLS_VERIFY"))
		if err != nil {
			log.Error("HS_TLS_VERIFY must be a bool")
			os.Exit(1)
		}
	}

	if os.Getenv("CSI_MAJOR_VERSION") != "0" || os.Getenv("CSI_MAJOR_VERSION") != "1" {
		if err != nil {
			log.Error("CSI_MAJOR_VERSION must be set to \"0\" or \"1\"")
			os.Exit(1)
		}
	}

	common.DataPortalMountPrefix = os.Getenv("HS_DATA_PORTAL_MOUNT_PREFIX")
}

type Server interface {
	Start(net.Listener) error
	Stop()
}

func main() {

	validateEnvironmentVars()

	var server Server

	CSI_version := os.Getenv("CSI_MAJOR_VERSION")

	endpoint := os.Getenv("CSI_ENDPOINT")
	csiDriver := driver.NewCSIDriver(
		os.Getenv("HS_ENDPOINT"),
		os.Getenv("HS_USERNAME"),
		os.Getenv("HS_PASSWORD"),
		os.Getenv("HS_TLS_VERIFY"),
	)

	if CSI_version == "0" {
		server = driver.NewCSIDriver_v0Support(csiDriver)
		common.CsiVersion = "0"
	} else {
		server = csiDriver
	}

	// Listen
	os.Remove(endpoint)
	l, err := net.Listen("unix", endpoint)
	if err != nil {
		log.Errorf("Error: Unable to listen on %s socket: %v\n",
			endpoint,
			err)
		os.Exit(1)
	}
	defer os.Remove(endpoint)

	// Start server
	if err := server.Start(l); err != nil {
		log.Errorf("Error: Unable to start CSI server: %v\n",
			err)
		os.Exit(1)
	}
	log.Info("hammerspace driver started")

	// Wait for signal
	sigc := make(chan os.Signal, 1)
	sigs := []os.Signal{
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
	}
	signal.Notify(sigc, sigs...)

	<-sigc
	server.Stop()
	log.Info("hammerspace driver stopped")
}
