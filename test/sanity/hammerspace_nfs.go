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
	"github.com/hammer-space/csi-plugin/pkg/driver"
	"github.com/kubernetes-csi/csi-test/pkg/sanity"
	"k8s.io/kubernetes/pkg/kubelet/kubeletconfig/util/log"
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
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
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
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
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
							Mount: &csi.VolumeCapability_MountVolume{
								FsType: "nfs",
							},
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
			additionalMetadataTags := map[string]string{}
			if tags, exists := sc.Config.TestVolumeParameters["additionalMetadataTags"]; exists {
				additionalMetadataTags = parseMetadataTagsParam(tags)
			}
			for key, value := range additionalMetadataTags {
				// Check the file exists
				output, err := common.ExecCommand("cat", fmt.Sprintf("%s?.eval list_tags", sc.Config.TargetPath + "/"))
				if err != nil {
					Expect(err).NotTo(HaveOccurred())
				}
				log.Infof(string(output))
				output, err = common.ExecCommand("cat", fmt.Sprintf("%s?.eval get_tag(\"%s\")", sc.Config.TargetPath + "/", key))
				if err != nil {
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(strings.TrimSpace(string(output))).To(Equal(fmt.Sprintf("\"%s\"", value)))
			}

			//Check that HS objectives are set
			if objectivesString, exists := sc.Config.TestVolumeParameters["objectives"]; exists {
				objectives := strings.Split(objectivesString, ",")
				share, _ := GetHammerspaceClient().GetShare(driver.GetVolumeNameFromPath(vol.GetVolume().GetVolumeId()))
				log.Infof("Got share %v", share)
				objectiveNames := make([]string, len(share.Objectives.Applied))
				for i, o := range share.Objectives.Applied {
					objectiveNames[i] = o.Name
				}

				for _, obj := range objectives {
					if ! driver.IsValueInList(obj, objectiveNames) {
						Fail(fmt.Sprintf("%s objective is not set on share, applied objectives: %s", obj, share.Objectives))
					}
				}
			}
		})
	})
})




var _ = sanity.DescribeSanity("Hammerspace - NFS Volumes Negative Tests", func(sc *sanity.SanityContext) {
	var (
		cl *sanity.Cleanup
		c  csi.NodeClient
		s  csi.ControllerClient

		controllerPublishSupported bool
		nodeStageSupported         bool
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

		It("should fail with invalid fstype", func() {
			name := uniqueString("sanity-node-full")

			// Create Volume  with invalid FS type
			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "notafs"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "notafs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())

		})


		// Create Volume  with invalid metadata tags field
		It("should fail with invalid metadata", func() {
			name := uniqueString("sanity-node-full")

			// Create Volume  with invalid FS type
			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
			params["additionalMetadataTags"] = "invalid=format,,for,metadata"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())

		})


		// Create Volume  with invalid objectives field
		It("should fail with invalid objectives", func() {
			name := uniqueString("sanity-node-full")

			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
			params["objectives"] = "invalid=format,,for,objectives"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())

		})

		// Create Volume  with non-existent objective
		It("should fail with non-existent objective", func() {
			name := uniqueString("sanity-node-full")

			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
			params["objectives"] = "idonotexist"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())
		})


		// Create Volume  with invalid export options
		It("should fail with invalid objectives", func() {
			name := uniqueString("sanity-node-full")

			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
			params["exportOptions"] = "invalid=format,,for,exportOptions"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())

		})

		// Create Volume  with invalid delete delay
		It("should fail with invalid deleteDelay", func() {
			name := uniqueString("sanity-node-full")

			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
			params["deleteDelay"] = "not a number"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())

		})
		// Create Volume  with invalid volume name format
		It("should fail with invalid volumeNameFormat", func() {
			name := uniqueString("sanity-node-full")

			By("creating a multi node writer volume")
			params := copyStringMap(sc.Config.TestVolumeParameters)
			params["fsType"] = "nfs"
			params["volumeNameFormat"] = "invalid=format"
			_, err := s.CreateVolume(
				context.Background(),
				&csi.CreateVolumeRequest{
					Name: name,
					CapacityRange: &csi.CapacityRange{
						RequiredBytes: TestVolumeSize(sc),
					},
					VolumeCapabilities: []*csi.VolumeCapability{
						{
							AccessType: &csi.VolumeCapability_Mount{
								Mount: &csi.VolumeCapability_MountVolume{
									FsType: "nfs",
								},
							},
							AccessMode: &csi.VolumeCapability_AccessMode{
								Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
							},
						},
					},
					Secrets:    sc.Secrets.CreateVolumeSecret,
					Parameters: params,
				},
			)
			Expect(err).To(HaveOccurred())

		})
	})
})