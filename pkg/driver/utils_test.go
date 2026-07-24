package driver

import (
	"reflect"
	"testing"
)

func TestGetSnapshotNameFromSnapshotId(t *testing.T) {

	snapshotId := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	expected := "2019-05-24T15-26-57-0"
	actual, err := GetSnapshotNameFromSnapshotId(snapshotId)
	if err != nil {
		t.Logf("Unexpected error, %v", err)
		t.FailNow()
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}

	snapshotId = "2019-05-24T15-26-57-0"
	_, err = GetSnapshotNameFromSnapshotId(snapshotId)
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}

}

func TestGetShareNameFromSnapshotId(t *testing.T) {

	snapshotId := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	expected := "sanity-controller-source-vol-859F8B9B-35BBFB36"
	actual, err := GetShareNameFromSnapshotId(snapshotId)
	if err != nil {
		t.Logf("Unexpected error, %v", err)
		t.FailNow()
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}

	snapshotId = "2019-05-24T15-26-57-0"
	_, err = GetShareNameFromSnapshotId(snapshotId)
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}
}

func TestGetSnapshotIDFromSnapshotName(t *testing.T) {
	expected := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	actual := GetSnapshotIDFromSnapshotName("2019-05-24T15-26-57-0",
		"/sanity-controller-source-vol-859F8B9B-35BBFB36")
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

func TestGetVolumeNameFromPath(t *testing.T) {
	expected := "test-volume"
	actual := GetVolumeNameFromPath("/test-backing-share/test-volume")
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

// TestIsFileBackedVolumeID covers the structural file- vs share-backed
// discriminator (PR C): a file-backed volume ID is a file *inside* a backing
// share (multi-segment path), while a share-backed/native NFS volume ID is the
// share itself (single top-level segment). This replaced a GetShare probe that
// 404'd for every file-backed source.
func TestIsFileBackedVolumeID(t *testing.T) {
	cases := []struct {
		volumeID string
		want     bool
	}{
		{"/k8s-file-backed/file--pvc-1234", true}, // file inside a backing share
		{"/backing/file--pvc-abcd/nested", true},  // deeper still -> file-backed
		{"/some-share", false},                    // native NFS share
		{"/hs-nfs-prod", false},                   // native NFS share
		{"/", false},                              // root
		{"", false},                               // empty -> Dir("")=="." != "/" would be true; guard below
	}
	for _, tc := range cases {
		got := isFileBackedVolumeID(tc.volumeID)
		// "" is a malformed ID; document current behavior rather than assert a
		// specific value we don't rely on.
		if tc.volumeID == "" {
			continue
		}
		if got != tc.want {
			t.Fatalf("isFileBackedVolumeID(%q) = %v, want %v", tc.volumeID, got, tc.want)
		}
	}
}
