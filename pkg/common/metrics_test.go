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

import (
	"context"
	"errors"
	"testing"
)

// TestAnvilRoute covers the metric route-template normalization (PR F): every
// per-resource id segment collapses to {id} so the Prometheus label doesn't
// explode into one series per share/file/task/snapshot, while action segments
// (e.g. file-snapshots/list, shares/{id}/objective-set) are preserved.
func TestAnvilRoute(t *testing.T) {
	cases := map[string]string{
		"/mgmt/v1.2/rest/shares/file--pvc-1a2b3c":              "/mgmt/v1.2/rest/shares/{id}",
		"/mgmt/v1.2/rest/shares/k8s-file-backed/objective-set": "/mgmt/v1.2/rest/shares/{id}/objective-set",
		"/mgmt/v1.2/rest/shares":                               "/mgmt/v1.2/rest/shares",
		"/mgmt/v1.2/rest/tasks/abcd-1234":                      "/mgmt/v1.2/rest/tasks/{id}",
		"/mgmt/v1.2/rest/files":                                "/mgmt/v1.2/rest/files",
		"/mgmt/v1.2/rest/files/somefile":                       "/mgmt/v1.2/rest/files/{id}",
		"/mgmt/v1.2/rest/objectives/keep-online":               "/mgmt/v1.2/rest/objectives/{id}",
		"/mgmt/v1.2/rest/file-snapshots/list":                  "/mgmt/v1.2/rest/file-snapshots/list",
		"/mgmt/v1.2/rest/file-snapshots/snap-9":                "/mgmt/v1.2/rest/file-snapshots/{id}",
		"/mgmt/v1.2/rest/cntl/state":                           "/mgmt/v1.2/rest/cntl/state",
		"/mgmt/v1.2/rest/data-portals/":                        "/mgmt/v1.2/rest/data-portals/",
		// share-snapshots: keep the action verb, collapse share + snapshot ids
		"/mgmt/v1.2/rest/share-snapshots/snapshot-create/k8s-file-backed":                  "/mgmt/v1.2/rest/share-snapshots/snapshot-create/{id}",
		"/mgmt/v1.2/rest/share-snapshots/snapshot-list/k8s-file-backed":                    "/mgmt/v1.2/rest/share-snapshots/snapshot-list/{id}",
		"/mgmt/v1.2/rest/share-snapshots/snapshot-delete/k8s-file-backed/2026-07-24T00-00": "/mgmt/v1.2/rest/share-snapshots/snapshot-delete/{id}/{id}",
	}
	for in, want := range cases {
		if got := AnvilRoute(in); got != want {
			t.Errorf("AnvilRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMeasureOp verifies the MeasureOp contract (PR F): it returns a non-nil
// finish closure that is safe to call with a nil *error, a nil-valued *error,
// and a non-nil error, without panicking (instruments are no-ops until a real
// MeterProvider is installed).
func TestMeasureOp(t *testing.T) {
	ctx := context.Background()

	// nil *error
	done := MeasureOp(ctx, "TestOp")
	if done == nil {
		t.Fatal("MeasureOp returned a nil finish closure")
	}
	done(nil)

	// pointer to a nil error (the common success case: defer MeasureOp(...)(&err))
	var errNil error
	MeasureOp(ctx, "TestOp")(&errNil)

	// pointer to a non-nil error (records an error)
	errSet := errors.New("boom")
	MeasureOp(ctx, "TestOp", nil...)(&errSet)

	// calling twice must not panic (idempotent finish is not required, but a
	// second independent op must be safe)
	MeasureOp(ctx, "AnotherOp")(nil)
}
