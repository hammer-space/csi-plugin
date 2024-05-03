package driver

import (
	"reflect"
	"testing"

	"github.com/hammer-space/csi-plugin/pkg/driver"
)

func TestGetSnapshotNameFromSnapshotId(t *testing.T) {

	snapshotId := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	expected := "2019-05-24T15-26-57-0"
	actual, err := driver.GetSnapshotNameFromSnapshotId(snapshotId)
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
	_, err = driver.GetSnapshotNameFromSnapshotId(snapshotId)
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}

}

func TestGetShareNameFromSnapshotId(t *testing.T) {

	snapshotId := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	expected := "sanity-controller-source-vol-859F8B9B-35BBFB36"
	actual, err := driver.GetShareNameFromSnapshotId(snapshotId)
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
	_, err = driver.GetShareNameFromSnapshotId(snapshotId)
	if err == nil {
		t.Logf("Expected error")
		t.FailNow()
	}
}

func TestGetSnapshotIDFromSnapshotName(t *testing.T) {
	expected := "2019-05-24T15-26-57-0|/sanity-controller-source-vol-859F8B9B-35BBFB36"
	actual := driver.GetSnapshotIDFromSnapshotName("2019-05-24T15-26-57-0",
		"/sanity-controller-source-vol-859F8B9B-35BBFB36")
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

func TestGetVolumeNameFromPath(t *testing.T) {
	expected := "test-volume"
	actual := driver.GetVolumeNameFromPath("/test-backing-share/test-volume")
	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}
