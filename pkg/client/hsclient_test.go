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
	"strings"
	"testing"

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
		w.WriteHeader(fakeResponseCode)        // ✅ write status first
		_, _ = io.WriteString(w, fakeResponse) // ✅ then write body
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

	fakeResponseCode = 200
	_, err = hsclient.ListShares(context.Background())
	if err != nil {
		t.Logf("Expected error: %v", err)
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
		testutils.AssertEqualJSON(t, string(bodyString), expectedCreateShareBody)
	})

	fakeTaskResponse := FakeTaskCompleted
	fakeTaskResponseCode := 200
	Mux.HandleFunc(BasePath+"/tasks/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(fakeTaskResponseCode)
		_, _ = io.WriteString(w, fakeTaskResponse)
	})

	objectiveRequests := 0
	Mux.HandleFunc(BasePath+"/objectives", func(w http.ResponseWriter, r *http.Request) {
		objectiveRequests++
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `[
			{
				"uoid":{
					"uuid":"test-obj-uuid",
					"objectType":"OBJECTIVE"
				},
				"name":"test-obj",
				"internalId":10,
				"priority":"MEDIUM"
			},
			{
				"uoid":{
					"uuid":"test-obj2-uuid",
					"objectType":"OBJECTIVE"
				},
				"name":"test-obj2",
				"internalId":11,
				"priority":"HIGH"
			}
		]`)
	})

	// test basic
	expectedCreateShareBody = fmt.Sprintf(`{
		"name":"test",
		"path":"/test",
		"comment":"",
		"extendedInfo":{
			"csi_created_by_plugin_version":"%s",
			"csi_created_by_plugin_name":"%s",
			"csi_delete_delay": "%d",
			"csi_created_by_plugin_git_hash":"%s",
			"csi_created_by_csi_version":"%s"
		}
	}`, common.Version, common.CsiPluginName, 1, common.Githash, common.CsiVersion)

	err := hsclient.CreateShare(context.Background(), "test",
		"/test", -1,
		[]string{}, []common.ShareExportOptions{}, 1, "")
	if err != nil {
		t.Error(err)
	}

	// test multiple objectives
	t.Log("Test Multiple Objectives")
	expectedCreateShareBody = fmt.Sprintf(`{
		"name":"test",
		"path":"/test",
		"comment":"",
		"extendedInfo":{
			"csi_created_by_plugin_version":"%s",
			"csi_created_by_plugin_name":"%s",
			"csi_delete_delay": "%d",
			"csi_created_by_plugin_git_hash":"%s",
			"csi_created_by_csi_version":"%s"
		},
		"shareObjectives":[
			{
				"objective":{
					"uoid":{
						"uuid":"test-obj-uuid",
						"objectType":"OBJECTIVE"
					},
					"name":"test-obj",
					"internalId":10,
					"priority":"MEDIUM"
				}
			},
			{
				"objective":{
					"uoid":{
						"uuid":"test-obj2-uuid",
						"objectType":"OBJECTIVE"
					},
					"name":"test-obj2",
					"internalId":11,
					"priority":"HIGH"
				}
			}
		]
	}`, common.Version, common.CsiPluginName, 1, common.Githash, common.CsiVersion)

	err = hsclient.CreateShare(context.Background(), "test",
		"/test",
		-1, []string{"test-obj", "test-obj2"},
		[]common.ShareExportOptions{},
		1, "")
	if err != nil {
		t.Error(err)
	}
	if objectiveRequests != 1 {
		t.Fatalf("expected 1 objective list request, got %d", objectiveRequests)
	}

	t.Log("Test Missing Objective Fails Share Create")
	err = hsclient.CreateShare(context.Background(), "test",
		"/test",
		-1, []string{"missing-obj"},
		[]common.ShareExportOptions{},
		1, "")
	if err == nil {
		t.Fatal("expected missing objective to fail share create")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf(common.InvalidObjectiveNameDoesNotExist, "missing-obj")) {
		t.Fatalf("expected missing objective error, got %v", err)
	}
	if objectiveRequests != 1 {
		t.Fatalf("expected cached objective list to be reused, got %d objective list requests", objectiveRequests)
	}

	// test share size
	t.Log("Test Share Size")
	expectedCreateShareBody = fmt.Sprintf(`{
		"name":"test",
		"path":"/test",
		"comment":"",
		"extendedInfo":{
			"csi_created_by_plugin_version":"%s",
			"csi_created_by_plugin_name":"%s",
			"csi_delete_delay": "%d",
			"csi_created_by_plugin_git_hash":"%s",
			"csi_created_by_csi_version":"%s"
		},
		"shareSizeLimit":100
	}`, common.Version, common.CsiPluginName, 1, common.Githash, common.CsiVersion)

	err = hsclient.CreateShare(context.Background(), "test",
		"/test",
		100,
		[]string{},
		[]common.ShareExportOptions{},
		1, "")
	if err != nil {
		t.Error(err)
	}

	// test multiple export options
	t.Log("Test Multiple export options")
	expectedCreateShareBody = fmt.Sprintf(`{
		"name":"test",
		"path":"/test",
		"comment":"",
		"extendedInfo":{
			"csi_created_by_plugin_version":"%s",
			"csi_created_by_plugin_name":"%s",
			"csi_delete_delay": "%d",
			"csi_created_by_plugin_git_hash":"%s",
			"csi_created_by_csi_version":"%s"
		},
		"shareSizeLimit":100,
		"exportOptions":[
			{
				"subnet":"172.168.0.0/24",
				"accessPermissions":"RW",
				"rootSquash":false
			},
			{
				"subnet":"*",
				"accessPermissions":"RO",
				"rootSquash":true
			}
		]
	}`, common.Version, common.CsiPluginName, 1, common.Githash, common.CsiVersion)

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
		1, "")
	if err != nil {
		t.Error(err)
	}

	// test share creation fails on backend
	t.Log("Test Share Creation Fails")
	expectedCreateShareBody = fmt.Sprintf(`{
	"name":"test",
	"path":"/test",
	"comment":"",
	"extendedInfo":{
	    "csi_created_by_plugin_version":"%s",
	    "csi_created_by_plugin_name":"%s",
	    "csi_delete_delay":"%d",
	    "csi_created_by_plugin_git_hash":"%s",
	    "csi_created_by_csi_version":"%s"
	}
	}`, common.Version, common.CsiPluginName, 1, common.Githash, common.CsiVersion)

	fakeTaskResponse = FakeTaskFailed
	err = hsclient.CreateShare(context.Background(), "test", "/test", -1, []string{}, []common.ShareExportOptions{}, 1, "")
	if err == nil {
		t.Fatal("Expected error")
	}
}

func TestCreateShareFromSnapshotClonesInsideSourceShare(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()

	Mux.HandleFunc(BasePath+"/shares", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("restore inside source share must not create a new Hammerspace share")
	})

	Mux.HandleFunc(BasePath+"/share-snapshots/clone-create/source-share", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST clone-create, got %s", r.Method)
		}
		query := r.URL.Query()
		if got := query.Get("snapshot-name"); got != "snap-1" {
			t.Fatalf("expected snapshot-name=snap-1, got %q", got)
		}
		if got := query.Get("destination-path"); got != "/restore" {
			t.Fatalf("expected destination-path=/restore, got %q", got)
		}
		if got := query.Get("overwrite-destination"); got != "true" {
			t.Fatalf("expected overwrite-destination=true, got %q", got)
		}
		w.Header().Set("Location", "http://fake_location/tasks/clone-share-snapshot")
		w.WriteHeader(202)
	})

	Mux.HandleFunc(BasePath+"/tasks/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, FakeTaskCompleted)
	})

	restoredPath, err := hsclient.CreateShareFromSnapshot(
		context.Background(),
		"restore",
		"/restore",
		-1,
		[]string{},
		[]common.ShareExportOptions{{
			Subnet:            "*",
			AccessPermissions: "RW",
			RootSquash:        false,
		}},
		0,
		"restored share",
		"source-share",
		"/source-share",
		"snap-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if restoredPath != "/source-share/restore" {
		t.Fatalf("expected restored path /source-share/restore, got %q", restoredPath)
	}
}

func TestCloneShareSnapshotFailsOffloadedRsyncFailure(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()

	cloneRequests := 0

	Mux.HandleFunc(BasePath+"/share-snapshots/clone-create/source-share", func(w http.ResponseWriter, r *http.Request) {
		cloneRequests++
		if r.Method != "POST" {
			t.Fatalf("expected POST clone-create, got %s", r.Method)
		}
		query := r.URL.Query()
		if got := query.Get("snapshot-name"); got != "snap-1" {
			t.Fatalf("expected snapshot-name=snap-1, got %q", got)
		}
		if got := query.Get("destination-path"); got != "/restore" {
			t.Fatalf("expected destination-path=/restore, got %q", got)
		}
		w.Header().Set("Location", "http://fake_location/tasks/clone-task")
		w.WriteHeader(202)
	})

	Mux.HandleFunc(BasePath+"/tasks/clone-task", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatalf("expected only GET for failed clone task, got %s", r.Method)
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{
			"uuid":"clone-task",
			"name":"share-clone",
			"status":"FAILED",
			"statusMessage":"failed to rsync (offloaded) /hs/source/.snapshot/snap-1/ to /hs/source/restore"
		}`)
	})

	err := hsclient.CloneShareSnapshot(context.Background(), "source-share", "snap-1", "/restore", true)
	if err == nil {
		t.Fatal("expected clone failure")
	}
	if err.Error() != "failed to clone share snapshot" {
		t.Fatalf("expected clone failure error, got %v", err)
	}
	if cloneRequests != 1 {
		t.Fatalf("expected one clone request, got %d", cloneRequests)
	}
}

func TestCloneShareSnapshotRequiresTaskLocation(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()

	Mux.HandleFunc(BasePath+"/share-snapshots/clone-create/source-share", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	err := hsclient.CloneShareSnapshot(context.Background(), "source-share", "snap-1", "/restore", true)
	if err == nil || !strings.Contains(err.Error(), "no share snapshot clone task") {
		t.Fatalf("expected missing task location error, got %v", err)
	}
}
