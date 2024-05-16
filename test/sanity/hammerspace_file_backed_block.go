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
	"io"
	"io/ioutil"
	"k8s.io/kubernetes/pkg/kubelet/kubeletconfig/util/log"
	"os"
	"strconv"
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
					TargetPath:        sc.Config.TargetPath + "/dev",
					StagingTargetPath: sc.Config.StagingPath + "/dev",
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
				additionalMetadataTags = parseMetadataTagsParam(tags)
			}
			for key, value := range additionalMetadataTags {
				// Check the file exists
				output, err := common.ExecCommand("cat", fmt.Sprintf("%s?.eval list_tags", common.ShareStagingDir+vol.GetVolume().GetVolumeId()))
				if err != nil {
					Expect(err).NotTo(HaveOccurred())
				}
				log.Infof(string(output))
				output, err = common.ExecCommand("cat", fmt.Sprintf("%s?.eval get_tag(\"%s\")", common.ShareStagingDir+vol.GetVolume().GetVolumeId(), key))
				if err != nil {
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(strings.TrimSpace(string(output))).To(Equal(fmt.Sprintf("\"%s\"", value)))
			}

			By("Write data to volume")
			//sc.Config.TargetPath
			testData := []byte("test_data")
			err = ioutil.WriteFile(sc.Config.TargetPath+"/dev", testData, 0644)
			Expect(err).NotTo(HaveOccurred())

			By("expand the volume")
			_, err = c.NodeExpandVolume(
				context.Background(),
				&csi.NodeExpandVolumeRequest{
					VolumeId:   vol.GetVolume().GetVolumeId(),
					VolumePath: sc.Config.TargetPath + "/dev",
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc) * 2,
					},
				},
			)

			Expect(err).NotTo(HaveOccurred())

			output, err := common.ExecCommand("blockdev", "--getsize64", sc.Config.TargetPath+"/dev")
			if err != nil {
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(strconv.Atoi(strings.TrimSpace(string(output)))).To(Equal(TestVolumeSize(sc) * 2))

			By("unpublish the volume")
			_, err = c.NodeUnpublishVolume(
				context.Background(),
				&csi.NodeUnpublishVolumeRequest{
					VolumeId:   vol.GetVolume().GetVolumeId(),
					TargetPath: sc.Config.TargetPath + "/dev",
				},
			)

			Expect(err).NotTo(HaveOccurred())

			By("publish the volume to alternative location as read-only")
			nodepubvol, err = c.NodePublishVolume(
				context.Background(),
				&csi.NodePublishVolumeRequest{
					VolumeId:          vol.GetVolume().GetVolumeId(),
					TargetPath:        sc.Config.TargetPath + "/dev",
					StagingTargetPath: sc.Config.StagingPath + "/dev",
					Readonly:          true,
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

			By("Read data from volume")
			r, err := os.Open(sc.Config.TargetPath + "/dev")
			if err != nil {
				Expect(err).NotTo(HaveOccurred())
			}

			output = make([]byte, len(testData))
			_, err = io.ReadFull(r, output[:])
			r.Close()
			if err != nil {
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(output).To(Equal(testData))

			By("Ensure write data to volume fails")
			err = ioutil.WriteFile(sc.Config.TargetPath+"/dev", testData, 0644)
			Expect(err).To(HaveOccurred())

			By("unpublish the volume from alt location")
			_, err = c.NodeUnpublishVolume(
				context.Background(),
				&csi.NodeUnpublishVolumeRequest{
					VolumeId:   vol.GetVolume().GetVolumeId(),
					TargetPath: sc.Config.TargetPath + "/dev",
				},
			)

			Expect(err).NotTo(HaveOccurred())

		})
	})
})
