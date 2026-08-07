package driver

import (
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	common "github.com/hammer-space/csi-plugin/pkg/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseParams(t *testing.T) {

	// Test defaults
	expectedParams := common.HSVolumeParameters{
		VolumeNameFormat: common.DefaultVolumeNameFormat,
		DeleteDelay:      -1,
		Comment:          "Created by CSI driver",
		ObjectiveTarget:  "share",
	}
	stringParams := map[string]string{}
	actualParams, _ := parseVolParams(stringParams)
	if !reflect.DeepEqual(actualParams, expectedParams) {
		t.Logf("Params not equal")
		t.Logf("Expected: %v", expectedParams)
		t.Logf("Actual: %v", actualParams)
		t.FailNow()
	}

	// Test valid name format
	expectedParams = common.HSVolumeParameters{
		VolumeNameFormat: "my-csi-volume-%s-hammerspace",
		DeleteDelay:      -1,
		Comment:          "Created by CSI driver",
		ObjectiveTarget:  "share",
	}
	stringParams = map[string]string{
		"volumeNameFormat": "my-csi-volume-%s-hammerspace",
	}
	actualParams, err := parseVolParams(stringParams)
	if !reflect.DeepEqual(actualParams, expectedParams) {
		t.Logf("Params not equal")
		t.Logf("Expected: %v", expectedParams)
		t.Logf("Actual: %v", actualParams)
		t.FailNow()
	}

	// Test invalid name format
	expectedParams = common.HSVolumeParameters{
		DeleteDelay: -1,
	}
	stringParams = map[string]string{
		"volumeNameFormat": "blah%s/",
	}
	actualParams, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}
	stringParams = map[string]string{
		"volumeNameFormat": "blah",
	}
	actualParams, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}

	// Test delete delay
	expectedParams = common.HSVolumeParameters{
		DeleteDelay:      30,
		VolumeNameFormat: common.DefaultVolumeNameFormat,
		Comment:          "Created by CSI driver",
		ObjectiveTarget:  "share",
	}
	stringParams = map[string]string{
		"deleteDelay": "30",
	}
	actualParams, err = parseVolParams(stringParams)
	if !reflect.DeepEqual(actualParams, expectedParams) {
		t.Logf("Params not equal")
		t.Logf("Expected: %v", expectedParams)
		t.Logf("Actual: %v", actualParams)
		t.FailNow()
	}

	stringParams = map[string]string{
		"deleteDelay": "notanumber",
	}
	_, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}

	// Test objectives
	expectedObjectives := []string{
		"obj1", "obj2", "obj3",
	}
	stringParams = map[string]string{
		"objectives": "obj1, obj2	,obj3,,",
	}
	actualParams, err = parseVolParams(stringParams)
	if !reflect.DeepEqual(actualParams.Objectives, expectedObjectives) {
		t.Logf("Objectives not equal")
		t.Logf("Expected: %v", expectedObjectives)
		t.Logf("Actual: %v", actualParams)
		t.FailNow()
	}

	// Test export options
	expectedOptions := []common.ShareExportOptions{
		{
			Subnet:            "*",
			AccessPermissions: "RO",
			RootSquash:        false,
		},
		{
			Subnet:            "10.2.0.0/24",
			AccessPermissions: "RW",
			RootSquash:        true,
		},
	}
	stringParams = map[string]string{
		"exportOptions": "*,RO, false; 10.2.0.0/24,RW,true",
	}
	actualParams, err = parseVolParams(stringParams)
	if !reflect.DeepEqual(actualParams.ExportOptions, expectedOptions) {
		t.Logf("Export options not equal")
		t.Logf("Expected: %v", expectedObjectives)
		t.Logf("Actual: %v", actualParams)
		t.FailNow()
	}

	// Test invalid export options

	stringParams = map[string]string{
		"exportOptions": ";;",
	}
	_, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}

	stringParams = map[string]string{
		"exportOptions": "*,RO, blah",
	}
	_, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}

	stringParams = map[string]string{
		"exportOptions": "*,RO",
	}
	_, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}

	// Test extended info
	expectedParams = common.HSVolumeParameters{
		AdditionalMetadataTags: map[string]string{
			"test_key":   "test_value",
			"test_quote": "\"test\"",
		},
	}
	stringParams = map[string]string{
		"additionalMetadataTags": "test_key=test_value,test_quote=\"test\"",
	}
	actualParams, err = parseVolParams(stringParams)
	if !reflect.DeepEqual(actualParams.AdditionalMetadataTags, expectedParams.AdditionalMetadataTags) {
		t.Logf("Params not equal")
		t.Logf("Expected: %v", expectedParams.AdditionalMetadataTags)
		t.Logf("Actual: %v", actualParams.AdditionalMetadataTags)
		t.FailNow()
	}

	// Test invalid
	stringParams = map[string]string{
		"additionalMetadataTags": "test_keyest_value,test_quote=\"test\"",
	}
	actualParams, err = parseVolParams(stringParams)
	if err == nil {
		t.Logf("expected error")
		t.FailNow()
	}

}

func TestGetMountFlagsFromCapabilities(t *testing.T) {
	capabilities := []*csi.VolumeCapability{
		{
			AccessType: &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			},
		},
		{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{
					FsType:     "nfs",
					MountFlags: []string{"vers=4.2", "hard"},
				},
			},
		},
	}

	actual := getMountFlagsFromCapabilities(capabilities)
	expected := []string{"vers=4.2", "hard"}

	if !reflect.DeepEqual(actual, expected) {
		t.Logf("Mount flags not equal")
		t.Logf("Expected: %v", expected)
		t.Logf("Actual: %v", actual)
		t.FailNow()
	}
}

// TestParseObjectiveTarget covers the objectiveTarget StorageClass parameter
// added for the fast file-backed CreateVolume path (PR I): default "share",
// explicit share/file/both, and rejection of anything else.
func TestParseObjectiveTarget(t *testing.T) {
	cases := []struct {
		name    string
		in      string // "" means the param is omitted entirely
		want    string
		wantErr bool
	}{
		{"default when omitted", "", "share", false},
		{"explicit share", "share", "share", false},
		{"file", "file", "file", false},
		{"both", "both", "both", false},
		{"invalid value", "bogus", "", true},
		{"case-sensitive (Share is invalid)", "Share", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]string{}
			if tc.in != "" {
				params["objectiveTarget"] = tc.in
			}
			got, err := parseVolParams(params)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("objectiveTarget=%q: expected error, got none", tc.in)
				}
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("objectiveTarget=%q: expected InvalidArgument, got %v", tc.in, status.Code(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("objectiveTarget=%q: unexpected error: %v", tc.in, err)
			}
			if got.ObjectiveTarget != tc.want {
				t.Fatalf("objectiveTarget=%q: got %q, want %q", tc.in, got.ObjectiveTarget, tc.want)
			}
		})
	}
}

// TestCheckFileBackedMinSize covers the per-fsType minimum size gate (PR B):
// xfs < 300 MiB and ext4 < 20 MiB are rejected with InvalidArgument; at/above
// the floor and other fsTypes pass.
func TestCheckFileBackedMinSize(t *testing.T) {
	const mib = 1024 * 1024
	cases := []struct {
		name    string
		fsType  string
		size    int64
		wantErr bool
	}{
		{"xfs below floor", "xfs", 299 * mib, true},
		{"xfs at floor", "xfs", common.MinXfsSizeBytes, false},
		{"xfs above floor", "xfs", 1024 * mib, false},
		{"ext4 below floor", "ext4", 19 * mib, true},
		{"ext4 at floor", "ext4", common.MinExt4SizeBytes, false},
		{"ext4 above floor", "ext4", 100 * mib, false},
		{"ext4 tiny", "ext4", 1, true},
		{"ext3 rejected large", "ext3", 100 * mib, true},   // ext3 unsupported at any size
		{"ext3 rejected small", "ext3", 1, true},           // ext3 unsupported at any size
		{"ext3 rejected at floor", "ext3", 20 * mib, true}, // ext3 unsupported even at/above the ext4 floor
		{"other fsType not gated", "btrfs", 1, false},
		{"empty fsType not gated", "", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFileBackedMinSize(tc.fsType, tc.size)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s %d: expected error, got nil", tc.fsType, tc.size)
				}
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("%s %d: expected InvalidArgument, got %v", tc.fsType, tc.size, status.Code(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("%s %d: unexpected error: %v", tc.fsType, tc.size, err)
			}
		})
	}
}
