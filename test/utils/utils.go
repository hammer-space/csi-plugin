package utils

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// NormalizeJSON ensures stable comparison between JSON strings.
func NormalizeJSON(s string) (any, error) {
	var obj any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return obj, nil
}

// AssertEqualJSON compares two JSON strings ignoring key order.
func AssertEqualJSON(t *testing.T, expected, got string) {
	expObj, err := NormalizeJSON(expected)
	if err != nil {
		t.Fatalf("bad expected JSON: %v", err)
	}
	gotObj, err := NormalizeJSON(got)
	if err != nil {
		t.Fatalf("bad got JSON: %v", err)
	}

	if !reflect.DeepEqual(expObj, gotObj) {
		expJSON, _ := json.MarshalIndent(expObj, "", "  ")
		gotJSON, _ := json.MarshalIndent(gotObj, "", "  ")
		t.Errorf("Expected:\n%s\nGot:\n%s", expJSON, gotJSON)
	}
}
