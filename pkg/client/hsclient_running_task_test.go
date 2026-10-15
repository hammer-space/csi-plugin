package client

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hammer-space/csi-plugin/pkg/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const queuedTask = "fe081418-a5ea-4310-b9c7-bffc3be03a5d"

func alreadyRunning(action string) string {
	return fmt.Sprintf(`[{"errorCode":4000,"args":["Task '%s' is already running with ID '%s' and Status 'VALIDATED'"],"message":"BAD_REQUEST"}]`, action, queuedTask)
}

// A retried DeleteVolume used to fail on Anvil's "already running" 400, so the
// provisioner retried and sent another DELETE every few seconds until Anvil
// got to the first one. It must wait on the queued task instead.
func TestDeleteShareWaitsOnQueuedDelete(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()
	deletes, polls := 0, 0
	Mux.HandleFunc(BasePath+"/shares/pvc-1", func(w http.ResponseWriter, r *http.Request) {
		deletes++
		w.WriteHeader(400)
		fmt.Fprint(w, alreadyRunning("share-delete"))
	})
	Mux.HandleFunc(BasePath+"/tasks/"+queuedTask, func(w http.ResponseWriter, r *http.Request) {
		polls++
		fmt.Fprint(w, `{"uuid":"`+queuedTask+`","name":"share-delete","status":"COMPLETED"}`)
	})

	if err := hsclient.DeleteShare(context.Background(), "pvc-1", 0); err != nil {
		t.Fatalf("DeleteShare: %v", err)
	}
	if deletes != 1 || polls != 1 {
		t.Fatalf("DELETEs = %d, task polls = %d; want 1 and 1", deletes, polls)
	}
}

// Running out of time while the delete is still queued is not a failed delete.
func TestDeleteShareStillQueuedIsAborted(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()
	Mux.HandleFunc(BasePath+"/shares/pvc-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, alreadyRunning("share-delete"))
	})
	Mux.HandleFunc(BasePath+"/tasks/"+queuedTask, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"uuid":"`+queuedTask+`","name":"share-delete","status":"VALIDATED"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := hsclient.DeleteShare(ctx, "pvc-1", 0)
	if status.Code(err) != codes.Aborted {
		t.Fatalf("got %v, want Aborted", err)
	}
}

// Deleting a share whose create is still running is retryable, not Internal.
func TestDeleteShareBusyIsUnavailable(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()
	Mux.HandleFunc(BasePath+"/shares/pvc-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `[{"args":["BUSY, task 'share-delete' conflicts with running task 'share-create(c6897d5e-30ae-4adb-b9d2-f5a196dac9cf)' which has a status of EXECUTING"],"message":"BAD_REQUEST"}]`)
	})

	err := hsclient.DeleteShare(context.Background(), "pvc-1", 0)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("got %v, want Unavailable", err)
	}
}

// A retried CreateVolume must wait on the queued create named in the 400,
// not list every task on the cluster.
func TestCreateShareWaitsOnQueuedCreate(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()
	Mux.HandleFunc(BasePath+"/shares", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, alreadyRunning("share-create"))
	})
	Mux.HandleFunc(BasePath+"/tasks/"+queuedTask, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"uuid":"`+queuedTask+`","name":"share-create","status":"COMPLETED"}`)
	})
	Mux.HandleFunc(BasePath+"/tasks", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("listed every task instead of waiting on the queued one")
		fmt.Fprint(w, `[]`)
	})

	if err := hsclient.CreateShare(context.Background(), "pvc-1", "/pvc-1", 0, nil, nil, 0, ""); err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
}

func TestRunningTaskID(t *testing.T) {
	body := alreadyRunning("share-delete")
	if got := runningTaskID(body, "share-delete"); got != queuedTask {
		t.Errorf("share-delete: got %q", got)
	}
	if got := runningTaskID(body, "share-create"); got != "" {
		t.Errorf("share-create: got %q, want none", got)
	}
}

// Every NFS mount looks up the floating IPs; they come from one cached
// cluster-state read, not one per mount.
func TestClusterStateIsCached(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()
	common.SetCacheData("CLUSTER_STATE", nil, 60)
	t.Cleanup(func() { common.SetCacheData("CLUSTER_STATE", nil, 60) })
	calls := 0
	Mux.HandleFunc(BasePath+"/cntl/state", func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"name":"c","portalFloatingIps":[]}`)
	})
	for i := 0; i < 3; i++ {
		_, _ = hsclient.GetPortalFloatingIp(context.Background())
	}
	if calls != 1 {
		t.Fatalf("/cntl/state queried %d times, want 1", calls)
	}
}

// A portal in maintenance or briefly down is still this Anvil's: its address
// must count as an NFS server, or a healthy backing mount through it is
// treated as foreign and torn down. So must the Anvil's management IP, which a
// 4.2 mount through a portal lists as its server. Mount selection still uses
// only up portals.
func TestNFSServerAddressesIncludesPortalsInAnyState(t *testing.T) {
	setupHTTP()
	defer tearDownHTTP()
	for _, key := range []string{"DATA_PORTALS", "CLUSTER_STATE"} {
		common.SetCacheData(key, nil, 60)
	}
	t.Cleanup(func() {
		for _, key := range []string{"DATA_PORTALS", "CLUSTER_STATE"} {
			common.SetCacheData(key, nil, 60)
		}
	})
	Mux.HandleFunc(BasePath+"/data-portals/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"operState":"UP","adminState":"UP","dataPortalType":"NFS_V3","node":{"name":"a","mgmtIpAddress":{"address":"10.0.0.1"}}},
			{"operState":"UP","adminState":"DOWN","dataPortalType":"NFS_V3","node":{"name":"b","mgmtIpAddress":{"address":"10.0.0.2"}}}
		]`)
	})
	Mux.HandleFunc(BasePath+"/cntl/state", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"c","portalFloatingIps":[{"address":"10.0.0.9"}],"mgmtIps":[{"address":"10.0.0.20"}]}`)
	})

	addresses, err := hsclient.NFSServerAddresses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"10.0.0.1": true, "10.0.0.2": true, "10.0.0.9": true, "10.0.0.20": true}
	if len(addresses) != len(want) {
		t.Fatalf("addresses = %v, want %v", addresses, want)
	}
	for _, a := range addresses {
		if !want[a] {
			t.Fatalf("addresses = %v, want %v", addresses, want)
		}
	}

	portals, err := hsclient.GetDataPortals(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(portals) != 1 || portals[0].Node.MgmtIpAddress.Address != "10.0.0.1" {
		t.Fatalf("mountable portals = %+v, want only the up portal 10.0.0.1", portals)
	}
}
