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
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/pkg/sanity"
)

import (
	"context"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = sanity.DescribeSanity("Hammerspace - Create Volume Negative Tests", func(sc *sanity.SanityContext) {
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
			By("creating a single node writer volume with bad fs")
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
								Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
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
