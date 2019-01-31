/*
Copyright 2017 Kubernetes Authors.

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
	"context"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/pkg/sanity"

	. "github.com/onsi/gomega"
)

const (
	// DefTestVolumeSize defines the base size of dynamically
	// provisioned volumes. 10GB by default, can be overridden by
	// setting Config.TestVolumeSize.
	DefTestVolumeSize int64 = 10 * 1024 * 1024 * 1024

	MaxNameLength int = 128
)

func TestVolumeSize(sc *sanity.SanityContext) int64 {
	if sc.Config.TestVolumeSize > 0 {
		return sc.Config.TestVolumeSize
	}
	return DefTestVolumeSize
}

func verifyVolumeInfo(v *csi.Volume) {
	Expect(v).NotTo(BeNil())
	Expect(v.GetVolumeId()).NotTo(BeEmpty())
}

func verifySnapshotInfo(snapshot *csi.Snapshot) {
	Expect(snapshot).NotTo(BeNil())
	Expect(snapshot.GetSnapshotId()).NotTo(BeEmpty())
	Expect(snapshot.GetSourceVolumeId()).NotTo(BeEmpty())
	Expect(snapshot.GetCreationTime()).NotTo(BeZero())
}

func isControllerCapabilitySupported(
	c csi.ControllerClient,
	capType csi.ControllerServiceCapability_RPC_Type,
) bool {

	caps, err := c.ControllerGetCapabilities(
		context.Background(),
		&csi.ControllerGetCapabilitiesRequest{})
	Expect(err).NotTo(HaveOccurred())
	Expect(caps).NotTo(BeNil())
	Expect(caps.GetCapabilities()).NotTo(BeNil())

	for _, cap := range caps.GetCapabilities() {
		Expect(cap.GetRpc()).NotTo(BeNil())
		if cap.GetRpc().GetType() == capType {
			return true
		}
	}
	return false
}
