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
    "golang.org/x/net/context"

    "github.com/container-storage-interface/spec/lib/go/csi"
    "github.com/golang/protobuf/ptypes/wrappers"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"

    common "github.com/hammer-space/csi-plugin/pkg/common"
)

func (d *CSIDriver) GetPluginInfo(
    ctx context.Context,
    req *csi.GetPluginInfoRequest) (
    *csi.GetPluginInfoResponse, error) {

    manifest := map[string]string{}
    manifest["githash"] = common.Githash

    return &csi.GetPluginInfoResponse{
        Name:          common.CsiPluginName,
        VendorVersion: common.Version,
        Manifest:      manifest,
    }, nil
}

func (d *CSIDriver) Probe(
    ctx context.Context,
    req *csi.ProbeRequest) (
    *csi.ProbeResponse, error) {

    // Make sure the client and backend can communicate
    err := d.hsclient.EnsureLogin()
    if err != nil {
        return &csi.ProbeResponse{
            Ready: &wrappers.BoolValue{Value: false},
        }, status.Errorf(codes.Unavailable, err.Error())
    }

    return &csi.ProbeResponse{
        Ready: &wrappers.BoolValue{Value: true},
    }, nil
}

func (d *CSIDriver) GetPluginCapabilities(
    ctx context.Context,
    req *csi.GetPluginCapabilitiesRequest) (
    *csi.GetPluginCapabilitiesResponse, error) {

    return &csi.GetPluginCapabilitiesResponse{
        Capabilities: []*csi.PluginCapability{
            {
                Type: &csi.PluginCapability_Service_{
                    Service: &csi.PluginCapability_Service{
                        Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
                    },
                },
            },
            {
                Type: &csi.PluginCapability_Service_{
                    Service: &csi.PluginCapability_Service{
                        Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS,
                    },
                },
            },
        },
    }, nil
}
