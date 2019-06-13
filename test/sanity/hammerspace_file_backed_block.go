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
    "k8s.io/kubernetes/pkg/kubelet/kubeletconfig/util/log"
    "strings"
)

import (
	"context"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = sanity.DescribeSanity("Hammerspace - Block Volumes", func(sc *sanity.SanityContext) {
	var (
		cl *sanity.Cleanup
		c  csi.NodeClient
		s  csi.ControllerClient

		controllerPublishSupported bool
	)

	BeforeEach(func() {
		c = csi.NewNodeClient(sc.Conn)
		s = csi.NewControllerClient(sc.Conn)

		controllerPublishSupported = isControllerCapabilitySupported(
			s,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME)
		cl = &sanity.Cleanup{
			Context:                    sc,
			NodeClient:                 c,
			ControllerClient:           s,
			ControllerPublishSupported: controllerPublishSupported,
		}
	})

	AfterEach(func() {
		cl.DeleteVolumes()
	})

	Describe("CreateVolume", func() {

		It("should work", func() {
			name := uniqueString("sanity-node-full")

			// Create Volume First
			By("creating a single node writer volume")
			vol, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Block{
								Block: &csi.VolumeCapability_BlockVolume{},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
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

			By("Publishing Volume")
			nodepubvol, err := c.NodePublishVolume(
				context.Background(),
				&csi.NodePublishVolumeRequest{
					VolumeId:          vol.GetVolume().GetVolumeId(),
					TargetPath:        sc.Config.TargetPath+"/dev",
					StagingTargetPath: sc.Config.StagingPath+"/dev",
					VolumeCapability: &csi.VolumeCapability{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
					VolumeContext: vol.GetVolume().GetVolumeContext(),
					Secrets:       sc.Secrets.NodePublishVolumeSecret,
				},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(nodepubvol).NotTo(BeNil())

			//Check that HS metadata is set
			log.Infof("Checking Metadata")
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
                // Check the file exists
                output, err := common.ExecCommand("cat", fmt.Sprintf("%s?.eval list_tags", common.ShareStagingDir + vol.GetVolume().GetVolumeId()))
                if err != nil {
                    Expect(err).NotTo(HaveOccurred())
                }
                log.Infof(string(output))
				output, err = common.ExecCommand("cat", fmt.Sprintf("%s?.eval get_tag(\"%s\")", common.ShareStagingDir + vol.GetVolume().GetVolumeId(), key))
				if err != nil {
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(strings.TrimSpace(string(output))).To(Equal(fmt.Sprintf("\"%s\"", value)))
			}
			//Check that HS objectives ar set
			//if objectivesString, exists := sc.Config.TestVolumeParameters["objectives"]; exists {
			//    objectives := strings.Split(objectivesString, ",")
			//    for obj := range objectives {
			//
			//        Expect(string(output)).To(Equal(fmt.Sprintf("\"%s\"", value)))
			//    }
			//}

			//// NodeUnpublishVolume
			By("cleaning up calling nodeunpublish")
			nodeunpubvol, err := c.NodeUnpublishVolume(
				context.Background(),
				&csi.NodeUnpublishVolumeRequest{
					VolumeId:   vol.GetVolume().GetVolumeId(),
					TargetPath: sc.Config.TargetPath + "/dev",
				})
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeunpubvol).NotTo(BeNil())


		})
	})
})