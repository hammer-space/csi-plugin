/*
Copyright 2018 The Kubernetes Authors.

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

package sanitytest

import (
	"github.com/hammer-space/csi-plugin/pkg/client"
	"github.com/hammer-space/csi-plugin/pkg/driver"
	"net"
	"os"
	"testing"

	log "github.com/sirupsen/logrus"

	sanity "github.com/kubernetes-csi/csi-test/pkg/sanity"
)

var (
	HSClient *client.HammerspaceClient
)

func Mkdir(targetPath string) (string, error) {
	os.Mkdir(targetPath, 0755)
	return targetPath, nil
}

func TestSanity(t *testing.T) {

	defer os.Remove(os.Getenv("CSI_ENDPOINT"))
	os.Remove(os.Getenv("CSI_ENDPOINT"))

	// Set up logging
	log.SetLevel(log.DebugLevel)
	log.SetReportCaller(true)

	// Set up variables
	mountPath := "/tmp/sanity-mounts"
	stagePath := "/tmp/sanity-stage"
	// Set up driver and env
	d := driver.NewCSIDriver(
		os.Getenv("HS_ENDPOINT"),
		os.Getenv("HS_USERNAME"),
		os.Getenv("HS_PASSWORD"),
		os.Getenv("HS_TLS_VERIFY"))

	go func() {
		l, _ := net.Listen("unix", os.Getenv("CSI_ENDPOINT"))
		d.Start(l)
	}()

	// Run test
	config := &sanity.Config{
		CreateTargetDir:          Mkdir, //Work around for sanity trying to recreate existing directories and failing
		CreateStagingDir:         Mkdir,
		CreatePathCmdTimeout:     30,
		TargetPath:               mountPath,
		StagingPath:              stagePath,
		Address:                  os.Getenv("CSI_ENDPOINT"),
		TestVolumeParametersFile: os.Getenv("SANITY_PARAMS_FILE"),
		TestVolumeSize:           1 * 1024 * 1024 * 1024,
	}
	sanity.Test(t, config)
}
