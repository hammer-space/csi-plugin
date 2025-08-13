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

package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	//log "github.com/sirupsen/logrus"

	common "github.com/hammer-space/csi-plugin/pkg/common"
	testutils "github.com/hammer-space/csi-plugin/test/utils"
)

var (
	Mux      *http.ServeMux
	Server   *httptest.Server
	hsclient *HammerspaceClient
)

func setupHTTP() {
	Mux = http.NewServeMux()
	Server = httptest.NewServer(Mux)

	httpclient := http.DefaultClient
	hsclient = &HammerspaceClient{
		username:   "test_user",
		password:   "test_password",
		endpoint:   Server.URL,
		httpclient: httpclient,
	}
}

func tearDownHTTP() {
	Server.Close()
}

func TestListShares(t *testing.T) {
	//log.SetLevel(log.DebugLevel)
	setupHTTP()
	defer tearDownHTTP()

	fakeResponse := "[]"
	fakeResponseCode := 200

	Mux.HandleFunc(BasePath+"/shares", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, fakeResponse)
		w.WriteHeader(fakeResponseCode)
	})
	shares, err := hsclient.ListShares(context.Background())
	if err != nil {
		t.Error(err)
	} else if len(shares) != 0 {
		t.Logf("List shares not empty")
		t.FailNow()
	}

	fakeResponse = fmt.Sprintf("[%s,%s]", FakeShareRoot, FakeShare1)

	shares, err = hsclient.ListShares(context.Background())
	if err != nil {
		t.Error(err)
	} else if len(shares) != 2 {
		t.Logf("Incorrect number of shares")
		t.FailNow()
	}

	expectedShares := []common.ShareResponse{
		{
			Name:         "root",
			ExportPath:   "/",
			ExtendedInfo: map[string]string{},
			ShareState:   "PUBLISHED",
			ExportOptions: []common.ShareExportOptions{
				{
					Subnet:            "*",
					AccessPermissions: "RW",
					RootSquash:        false,
				},
			},
			Space: common.ShareSpaceResponse{
				Total:     64393052160,
				Used:      0,
				Available: 63909851136,
			},
		},
		{
			Name:       "test-client-code",
			ExportPath: "/test-client-code",
			ExtendedInfo: map[string]string{
				"csi_created_by_plugin_version":  "test_version",
				"csi_created_by_plugin_name":     "test_plugin",
				"csi_delayed_delete":             "0",
				"csi_created_by_plugin_git_hash": "",
				"csi_created_by_csi_version":     "1",
			},
			Size:       1073741824,
			ShareState: "PUBLISHED",
			ExportOptions: []common.ShareExportOptions{
				{
					Subnet:            "*",
					AccessPermissions: "RW",
					RootSquash:        false,
				},
			},
			Space: common.ShareSpaceResponse{
				Total:     1073741824,
				Used:      0,
				Available: 1073741824,
			},
		},
	}

	if !reflect.DeepEqual(shares, expectedShares) {
		t.Logf("Shares not equal")
		t.Logf("Expected: %v", expectedShares)
		t.Logf("Actual: %v", shares)
		t.FailNow()
	}

	fakeResponseCode = 500
	_, err = hsclient.ListShares(context.Background())
	if err != nil {
		t.Logf("Expected error")
		t.Fail()
	}
}

func TestCreateShare(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()

	fakeResponseCode := 202
	expectedCreateShareBody := ""

	// Fake create share
	Mux.HandleFunc(BasePath+"/shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://fake_location/tasks/99184048-9390-4e68-92b8-d3ce6413372d")
		w.WriteHeader(fakeResponseCode)
		bodyString, _ := io.ReadAll(r.Body)
		equal, err := testutils.AreEqualJSON(string(bodyString), expectedCreateShareBody)
		if err != nil {
			t.Error(err)
		}
		if !equal {
			t.Fail()
		}
	})

	fakeTaskResponse := FakeTaskCompleted
	fakeTaskResponseCode := 200
	Mux.HandleFunc(BasePath+"/tasks/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, fakeTaskResponse)
		w.WriteHeader(fakeTaskResponseCode)
	})

	// test basic
	expectedCreateShareBody = fmt.Sprintf(`
        {"name":"test",
         "path":"/test",
         "extendedInfo":{
             "csi_created_by_plugin_version": "%s",
             "csi_created_by_plugin_name": "%s",
             "csi_delete_delay": "0",
             "csi_created_by_plugin_git_hash": "%s",
             "csi_created_by_csi_version": "%s"
         },
         "shareSizeLimit":0,
         "exportOptions":[]}
    `, common.Version, common.CsiPluginName, common.Githash, common.CsiVersion)
	err := hsclient.CreateShare(context.Background(), "test",
		"/test", -1,
		[]string{}, []common.ShareExportOptions{}, 0, "")
	if err != nil {
		t.Error(err)
	}

	// test multiple objectives

	t.Log("Test Multiple Objectives")
	Mux.HandleFunc(BasePath+"/shares/test/objective-set", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		if r.Method != "POST" {
			t.Logf("Fail: this should be a POST method")
			t.Fail()
		}
		if !((r.URL.Query()["objective-identifier"][0] == "test-obj") || (r.URL.Query()["objective-identifier"][0] == "test-obj2")) {
			t.Logf("Fail: Incorrect Objective %s", r.URL.Query()["objective-identifier"][0])
			t.Fail()
		}
	})

	err = hsclient.CreateShare(context.Background(), "test",
		"/test",
		-1, []string{"test-obj", "test-obj2"},
		[]common.ShareExportOptions{},
		0, "")
	if err != nil {
		t.Error(err)
	}

	// test share size
	t.Log("Test Share Size")
	expectedCreateShareBody = fmt.Sprintf(`
        {"name":"test",
         "path":"/test",
         "extendedInfo":{
             "csi_created_by_plugin_version": "%s",
             "csi_created_by_plugin_name": "%s",
             "csi_created_by_plugin_git_hash": "%s",
             "csi_created_by_csi_version": "%s"
         },
         "shareSizeLimit":100,
         "exportOptions":[]}
    `, common.Version, common.CsiPluginName, common.Githash, common.CsiVersion)
	err = hsclient.CreateShare(context.Background(), "test",
		"/test",
		100,
		[]string{},
		[]common.ShareExportOptions{},
		-1, "")
	if err != nil {
		t.Error(err)
	}

	// test multiple export options
	t.Log("Test Multiple export options")
	expectedCreateShareBody = fmt.Sprintf(`
        {"name":"test",
         "path":"/test",
         "extendedInfo":{
             "csi_created_by_plugin_version": "%s",
             "csi_created_by_plugin_name": "%s",
             "csi_delete_delay": "0",
             "csi_created_by_plugin_git_hash": "%s",
             "csi_created_by_csi_version": "%s"
         },
         "shareSizeLimit":100,
         "exportOptions":[
            {
                "subnet": "172.168.0.0/24",
                "accessPermissions": "RW",
                "rootSquash": false
            },
            {
                "subnet": "*",
                "accessPermissions": "RO",
                "rootSquash": true
            }
         ]}
    `, common.Version, common.CsiPluginName, common.Githash, common.CsiVersion)
	exportOptions := []common.ShareExportOptions{
		{
			Subnet:            "172.168.0.0/24",
			AccessPermissions: "RW",
			RootSquash:        false,
		},
		{
			Subnet:            "*",
			AccessPermissions: "RO",
			RootSquash:        true,
		},
	}
	err = hsclient.CreateShare(context.Background(), "test",
		"/test",
		100,
		[]string{},
		exportOptions,
		0, "")
	if err != nil {
		t.Error(err)
	}

	// test share creation fails on backend
	t.Log("Test Share Creation Fails")
	fakeTaskResponse = FakeTaskFailed
	expectedCreateShareBody = fmt.Sprintf(`
        {"name":"test",
         "path":"/test",
         "extendedInfo":{
             "csi_created_by_plugin_version": "%s",
             "csi_created_by_plugin_name": "%s",
             "csi_delete_delay": "0",
             "csi_created_by_plugin_git_hash": "%s",
             "csi_created_by_csi_version": "%s"
         },
         "shareSizeLimit":0,
         "exportOptions":[]}
    `, common.Version, common.CsiPluginName, common.Githash, common.CsiVersion)
	err = hsclient.CreateShare(context.Background(), "test", "/test", -1, []string{}, []common.ShareExportOptions{}, 0, "")
	if err == nil {
		t.Logf("Expected error")
		t.Fail()
	}
}
