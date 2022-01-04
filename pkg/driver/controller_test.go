package driver

import (
	"reflect"
	"testing"

	common "github.com/hammer-space/csi-plugin/pkg/common"
)

func TestParseParams(t *testing.T) {

	// Test defaults
	expectedParams := common.HSVolumeParameters{
		VolumeNameFormat: common.DefaultVolumeNameFormat,
		DeleteDelay:      -1,
		Comment:          "Created by CSI driver",
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
		common.ShareExportOptions{
			Subnet:            "*",
			AccessPermissions: "RO",
			RootSquash:        false,
		},
		common.ShareExportOptions{
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
