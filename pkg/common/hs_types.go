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

package common

type ClusterResponse struct {
    Capacity map[string]string `json:"capacity"`
}

// We must create separate req and response objects since the API does not allow
// specifying unused fields
type ShareRequest struct {
    Name          string               `json:"name"`
    ExportPath    string               `json:"path"`
    ExtendedInfo  map[string]string    `json:"extendedInfo"`
    Size          int64                `json:"shareSizeLimit,omitifempty"`
    ExportOptions []ShareExportOptions `json:"exportOptions,omitifempty"`
}

type ShareUpdateRequest struct {
    Name         string            `json:"name"`
    ExtendedInfo map[string]string `json:"extendedInfo"`
}

type ShareResponse struct {
    Name          string               `json:"name"`
    ExportPath    string               `json:"path"`
    ExtendedInfo  map[string]string    `json:"extendedInfo"`
    ShareState    string               `json:"shareState"`
    Size          int64                `json:"shareSizeLimit,omitifempty,string"`
    ExportOptions []ShareExportOptions `json:"exportOptions,omitifempty"`
    Space         ShareSpaceResponse   `json:"space"'`
}

type ShareSpaceResponse struct {
    Used      string `json:"used"`
    Total     string `json:"total"`
    Available string `json:"available"`
    percent   int
}

type ShareExportOptions struct {
    Subnet            string `json:"subnet"`
    AccessPermissions string `json:"accessPermissions"` // Must be "RO" or "RW"
    RootSquash        bool   `json:"rootSquash"`
}

type Task struct {
    Uuid      string `json:"uuid"`
    Action    string `json:"name"`
    Status    string `json:"status"`
    ExitValue string `json:"exitValue"`
}

type File struct {
    Name string `json:"name"`
    Path string `json:"path"`
    Size int64  `json:"size,string"`
}

type FileSnapshot struct {
    SourceFilename string `json:"sourceFilename"`
    Time           string `json:"time"`
}

type DataPortal struct {
    OperState      string            `json:"operState"`      // We want 'UP'
    AdminState     string            `json:"adminState"`     // We want 'UP'
    DataPortalType string            `json:"dataPortalType"` // We want NFS_V3
    Exported       []string          `json:"exported"`
    Node           DataPortalNode    `json:"node"`
    Uoid           map[string]string `json:"uoid"`
}

type DataPortalNodeAddress struct {
    Address      string `json:"address""`
    PrefixLength int    `json:"prefixLength"`
}

type DataPortalNode struct {
    Name          string                `json:"name"`
    MgmtIpAddress DataPortalNodeAddress `json:"mgmtIpAddress"` // do we want this or some data ip?
}
