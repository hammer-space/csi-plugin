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
	"github.com/hammer-space/hammerspace-csi-plugin/pkg/driver"
	"net"
	"os"
	"testing"

	sanity "github.com/kubernetes-csi/csi-test/pkg/sanity"
)

func TestSanity(t *testing.T) {
	// Set up variables
	mountPath := "/tmp/"
	stagePath := "/tmp/"
	// Set up driver and env
	d := driver.NewCSIDriver(
		os.Getenv("HS_ENDPOINT"),
		os.Getenv("HS_USERNAME"),
		os.Getenv("HS_PASSWORD"),
		os.Getenv("HS_TLS_VERIFY"),
		os.Getenv("CSI_USE_ANVIL_FOR_DATA"))
	defer os.Remove(os.Getenv("CSI_ENDPOINT"))
	go func() {
		l, _ := net.Listen("unix", os.Getenv("CSI_ENDPOINT"))
		d.Start(l)
	}()

	// Run test
	config := &sanity.Config{
		TargetPath:               mountPath,
		StagingPath:              stagePath,
		Address:                  os.Getenv("CSI_ENDPOINT"),
		TestVolumeParametersFile: os.Getenv("SANITY_PARAMS_FILE"),
	}
	sanity.Test(t, config)
}
