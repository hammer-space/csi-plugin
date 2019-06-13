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

// These are hammerspace specific sanity tests

package sanitytest

import (
	"fmt"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/hammer-space/csi-plugin/pkg/common"
	"github.com/kubernetes-csi/csi-test/pkg/sanity"
	"strings"
)

import (
	"context"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = sanity.DescribeSanity("Hammerspace - NFS Volumes", func(sc *sanity.SanityContext) {
	var (
		cl *sanity.Cleanup
		c  csi.NodeClient
		s  csi.ControllerClient

		controllerPublishSupported bool
		nodeStageSupported         bool
		nodeVolumeStatsSupported   bool
	)

	BeforeEach(func() {
		c = csi.NewNodeClient(sc.Conn)
		s = csi.NewControllerClient(sc.Conn)

		controllerPublishSupported = isControllerCapabilitySupported(
			s,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME)
		nodeStageSupported = isNodeCapabilitySupported(c, csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME)
		if nodeStageSupported {
			err := createMountTargetLocation(sc.Config.StagingPath)
			Expect(err).NotTo(HaveOccurred())
		}
		nodeVolumeStatsSupported = isNodeCapabilitySupported(c, csi.NodeServiceCapability_RPC_GET_VOLUME_STATS)
		cl = &sanity.Cleanup{
			Context:                    sc,
			NodeClient:                 c,
			ControllerClient:           s,
			ControllerPublishSupported: controllerPublishSupported,
			NodeStageSupported:         nodeStageSupported,
		}
	})

	AfterEach(func() {
		cl.DeleteVolumes()
	})

	Describe("CreateVolume", func() {

		It("should work", func() {
			name := uniqueString("sanity-node-full")

			// Create Volume First
			By("creating a multi node writer volume")
			vol, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: sc.Config.TestVolumeParameters,
				},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(vol).NotTo(BeNil())
			Expect(vol.GetVolume()).NotTo(BeNil())
			Expect(vol.GetVolume().GetVolumeId()).NotTo(BeEmpty())
			cl.RegisterVolume(name, sanity.VolumeInfo{VolumeID: vol.GetVolume().GetVolumeId()})

			nodepubvol, err := c.NodePublishVolume(
				context.Background(),
				&csi.NodePublishVolumeRequest{
					VolumeId:          vol.GetVolume().GetVolumeId(),
					TargetPath:        sc.Config.TargetPath,
					StagingTargetPath: sc.Config.StagingPath,
					VolumeCapability: &csi.VolumeCapability{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
					VolumeContext: vol.GetVolume().GetVolumeContext(),
					Secrets:       sc.Secrets.NodePublishVolumeSecret,
				},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(nodepubvol).NotTo(BeNil())

			//Check that HS metadata is set
			//sc.Config.TargetPath
			additionalMetadataTags := map[string]string{}
			if tags, exists := sc.Config.TestVolumeParameters["additionalMetadataTags"]; exists {
				tagsList := strings.Split(tags, ",")
				for _, m := range tagsList {
					extendedInfo := strings.Split(m, "=")
					//assert options is len 2
					key := strings.TrimSpace(extendedInfo[0])
					value := strings.TrimSpace(extendedInfo[1])

					additionalMetadataTags[key] = value
				}
			}
			for key, value := range additionalMetadataTags {
				output, err := common.ExecCommand("cat", fmt.Sprintf("%s?.eval\\ get_tag(\"%s\")", sc.Config.TargetPath, key))
				if err != nil {
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(string(output)).To(Equal(fmt.Sprintf("\"%s\"", value)))
			}
		})
	})
})