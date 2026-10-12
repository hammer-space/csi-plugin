package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func testCR() *unstructured.Unstructured {
	return object(map[string]interface{}{
		"apiVersion": "storage.hammerspace.com/v1alpha1", "kind": "HammerspaceCSIDriver",
		"metadata": map[string]interface{}{"name": "cluster", "uid": "owner-1", "generation": int64(1)},
		"spec": map[string]interface{}{"credentialsSecretName": "credentials", "storageClasses": []interface{}{
			map[string]interface{}{"name": "hs-nfs", "mode": "NFS"},
			map[string]interface{}{"name": "hs-block", "mode": "Block", "backingShareName": "blocks"},
			map[string]interface{}{"name": "hs-ext4", "mode": "ext4", "backingShareName": "files"},
			map[string]interface{}{"name": "hs-xfs", "mode": "xfs", "backingShareName": "xfs"},
		}},
	})
}

func testImages() Images {
	result := Images{}
	for _, k := range ImageKeys {
		result[k] = "example.invalid/" + k + ":test"
	}
	return result
}
func mustRender(t *testing.T, cr *unstructured.Unstructured) []*unstructured.Unstructured {
	t.Helper()
	spec, err := ParseSpec(cr)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Render(cr, spec, testImages(), "secret-uid/1")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func find(t *testing.T, objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, o := range objects {
		if o.GetKind() == kind && o.GetName() == name {
			return o
		}
	}
	t.Fatalf("missing %s %s", kind, name)
	return nil
}

func TestRenderStorageModesAndBlockHostAccess(t *testing.T) {
	cr := testCR()
	objects := mustRender(t, cr)
	for _, o := range objects {
		if !ownedBy(o, cr) {
			t.Fatalf("no owner: %s", o.GetName())
		}
	}
	for _, tc := range []struct{ name, key, value string }{{"hs-nfs", "fsType", "nfs"}, {"hs-block", "blockBackingShareName", "blocks"}, {"hs-ext4", "fsType", "ext4"}, {"hs-xfs", "fsType", "xfs"}} {
		sc := find(t, objects, "StorageClass", tc.name)
		value, _, _ := unstructured.NestedString(sc.Object, "parameters", tc.key)
		if value != tc.value || sc.Object["reclaimPolicy"] != "Retain" || sc.Object["volumeBindingMode"] != "WaitForFirstConsumer" {
			t.Fatalf("wrong class: %v", sc.Object)
		}
		if tc.name == "hs-block" {
			if _, found, _ := unstructured.NestedString(sc.Object, "parameters", "fsType"); found {
				t.Fatal("raw block must not force a filesystem")
			}
		}
	}
	driver := find(t, objects, "CSIDriver", DriverName)
	capacity, _, _ := unstructured.NestedBool(driver.Object, "spec", "storageCapacity")
	controller := find(t, objects, "StatefulSet", "csi-provisioner")
	provisioners, _, _ := unstructured.NestedSlice(controller.Object, "spec", "template", "spec", "containers")
	publishes := false
	for _, c := range provisioners {
		container := c.(map[string]interface{})
		if container["name"] != "csi-provisioner" {
			continue
		}
		args, _, _ := unstructured.NestedStringSlice(container, "args")
		for _, arg := range args {
			if arg == "--enable-capacity" {
				publishes = true
			}
		}
	}
	// Advertising storageCapacity with nothing publishing CSIStorageCapacity
	// objects makes the scheduler reject every node for these classes;
	// publishing without advertising leaves the objects unread.
	if capacity != publishes {
		t.Fatalf("storageCapacity %v but provisioner --enable-capacity %v", capacity, publishes)
	}
	node := find(t, objects, "DaemonSet", "csi-node")
	volumes, _, _ := unstructured.NestedSlice(node.Object, "spec", "template", "spec", "volumes")
	dev := false
	for _, v := range volumes {
		m := v.(map[string]interface{})
		if m["name"] == "dev-dir" {
			p, _, _ := unstructured.NestedString(m, "hostPath", "path")
			dev = p == "/dev"
		}
	}
	if !dev {
		t.Fatal("block node requires host /dev")
	}
	containers, _, _ := unstructured.NestedSlice(node.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m := c.(map[string]interface{})
		if m["name"] == "csi-resizer" {
			t.Fatal("resizer belongs only on controller")
		}
		if m["image"] == "" {
			t.Fatal("missing image")
		}
		if m["name"] != "hs-csi-plugin-node" {
			continue
		}
		privileged, _, _ := unstructured.NestedBool(m, "securityContext", "privileged")
		if !privileged {
			t.Fatal("node requires privileged device access")
		}
		mounts := m["volumeMounts"].([]interface{})
		devMount, propagation := false, false
		for _, mount := range mounts {
			v := mount.(map[string]interface{})
			if v["mountPath"] == "/dev" {
				devMount = true
			}
			if v["mountPath"] == "/var/lib/kubelet/" && v["mountPropagation"] == "Bidirectional" {
				propagation = true
			}
		}
		if !devMount || !propagation {
			t.Fatal("block device/mount propagation missing")
		}
		for _, e := range m["env"].([]interface{}) {
			v := e.(map[string]interface{})
			if v["name"] == "HS_TLS_VERIFY" && v["value"] != "true" {
				t.Fatal("TLS verification must default on")
			}
		}
	}
	scc := find(t, objects, "SecurityContextConstraints", "hammerspace-csi")
	users := scc.Object["users"].([]interface{})
	if len(users) != 2 {
		t.Fatal("SCC must be scoped to operand service accounts")
	}
}

func TestRenderPreservesAnnotationsAndAllowsMissingContainerEnv(t *testing.T) {
	previous := operands
	t.Cleanup(func() { operands = previous })
	var templates []map[string]interface{}
	if err := json.Unmarshal(operands, &templates); err != nil {
		t.Fatal(err)
	}
	for _, template := range templates {
		if template["kind"] != "StatefulSet" {
			continue
		}
		unstructured.SetNestedStringMap(template, map[string]string{"prometheus.io/scrape": "true"}, "spec", "template", "metadata", "annotations")
		containers, _, _ := unstructured.NestedSlice(template, "spec", "template", "spec", "containers")
		delete(containers[0].(map[string]interface{}), "env")
		unstructured.SetNestedSlice(template, containers, "spec", "template", "spec", "containers")
	}
	var err error
	operands, err = json.Marshal(templates)
	if err != nil {
		t.Fatal(err)
	}
	controller := find(t, mustRender(t, testCR()), "StatefulSet", "csi-provisioner")
	annotations, _, _ := unstructured.NestedStringMap(controller.Object, "spec", "template", "metadata", "annotations")
	if annotations["prometheus.io/scrape"] != "true" || annotations["storage.hammerspace.com/credentials-version"] != "secret-uid/1" {
		t.Fatalf("template annotations were lost: %v", annotations)
	}
	containers, _, _ := unstructured.NestedSlice(controller.Object, "spec", "template", "spec", "containers")
	if env, _, _ := unstructured.NestedSlice(containers[0].(map[string]interface{}), "env"); len(env) != 0 {
		t.Fatalf("unexpected sidecar environment: %v", env)
	}
}

func TestRenderRejectsUnknownContainer(t *testing.T) {
	previous := operands
	t.Cleanup(func() { operands = previous })
	var templates []map[string]interface{}
	if err := json.Unmarshal(operands, &templates); err != nil {
		t.Fatal(err)
	}
	for _, template := range templates {
		if template["kind"] != "StatefulSet" {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(template, "spec", "template", "spec", "containers")
		containers[0].(map[string]interface{})["name"] = "unknown-sidecar"
		unstructured.SetNestedSlice(template, containers, "spec", "template", "spec", "containers")
	}
	var err error
	operands, err = json.Marshal(templates)
	if err != nil {
		t.Fatal(err)
	}
	cr := testCR()
	spec, err := ParseSpec(cr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(cr, spec, testImages(), "secret-uid/1"); err == nil {
		t.Fatal("unknown container must fail before deploying an empty image reference")
	}
}

func TestSpecRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*unstructured.Unstructured)
	}{
		{"duplicate driver", func(cr *unstructured.Unstructured) { cr.SetName("second") }},
		{"missing secret", func(cr *unstructured.Unstructured) {
			unstructured.RemoveNestedField(cr.Object, "spec", "credentialsSecretName")
		}},
		{"windows", func(cr *unstructured.Unstructured) {
			unstructured.SetNestedStringMap(cr.Object, map[string]string{"kubernetes.io/os": "windows"}, "spec", "nodeSelector")
		}},
		{"missing backing share", func(cr *unstructured.Unstructured) {
			unstructured.SetNestedSlice(cr.Object, []interface{}{map[string]interface{}{"name": "block", "mode": "Block"}}, "spec", "storageClasses")
		}},
		{"unsupported mode", func(cr *unstructured.Unstructured) {
			unstructured.SetNestedSlice(cr.Object, []interface{}{map[string]interface{}{"name": "sc", "mode": "iscsi"}}, "spec", "storageClasses")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := testCR()
			tc.edit(cr)
			if _, err := ParseSpec(cr); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCRDNamesMatchKubernetesValidation(t *testing.T) {
	data, err := os.ReadFile("../../config/crd.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data, err = yaml.ToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	var crd map[string]interface{}
	if err := json.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	versions, _, _ := unstructured.NestedSlice(crd, "spec", "versions")
	properties, _, _ := unstructured.NestedMap(versions[0].(map[string]interface{}), "schema", "openAPIV3Schema", "properties", "spec", "properties")
	for _, path := range [][]string{{"credentialsSecretName", "pattern"}, {"storageClasses", "items", "properties", "name", "pattern"}} {
		pattern, found, err := unstructured.NestedString(properties, path...)
		if err != nil || !found {
			t.Fatalf("missing schema pattern at %v: %v", path, err)
		}
		re := regexp.MustCompile(pattern)
		for _, name := range []string{"credentials", "credentials.example.com", "a", "a-b.c-d", "a..b", "a.-b", "a-.b", "-a", "A"} {
			want := len(validation.IsDNS1123Subdomain(name)) == 0
			if got := re.MatchString(name); got != want {
				t.Errorf("schema %v accepts %q = %v, Kubernetes validation = %v", path, name, got, want)
			}
		}
	}
}

// The fake tracker does not implement SSA field ownership/defaulting. Emulate
// the merge here to test controller behavior; API-server validation is separate.
func harness(t *testing.T) (*Reconciler, *fake.FakeDynamicClient) {
	t.Helper()
	cr := testCR()
	ns := object(map[string]interface{}{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": Namespace}})
	secret := object(map[string]interface{}{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]interface{}{"name": "credentials", "namespace": Namespace, "uid": "secret-uid", "resourceVersion": "1"}, "data": map[string]interface{}{"username": "dXNlcg==", "password": "cGFzcw==", "endpoint": "aHR0cHM6Ly9leGFtcGxlLmludmFsaWQ="}})
	listKinds := map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, cr, ns, secret)
	client.PrependReactor("patch", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		a := action.(ktesting.PatchAction)
		if a.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		var patch map[string]interface{}
		if err := json.Unmarshal(a.GetPatch(), &patch); err != nil {
			return true, nil, err
		}
		current, err := client.Tracker().Get(a.GetResource(), a.GetNamespace(), a.GetName())
		if err != nil {
			return true, nil, err
		}
		u := current.(*unstructured.Unstructured).DeepCopy()
		var merge func(map[string]interface{}, map[string]interface{})
		merge = func(dst, src map[string]interface{}) {
			for k, v := range src {
				m, ok := v.(map[string]interface{})
				d, dok := dst[k].(map[string]interface{})
				if ok && dok {
					merge(d, m)
				} else {
					dst[k] = v
				}
			}
		}
		merge(u.Object, patch)
		err = client.Tracker().Update(a.GetResource(), u, a.GetNamespace())
		return true, u, err
	})
	return &Reconciler{Client: client, Images: testImages()}, client
}

func TestReconcileCreateDriftRotationAndUpgrade(t *testing.T) {
	r, c := harness(t)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err := c.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinalizer(cr) {
		t.Fatal("missing cleanup finalizer")
	}
	for _, o := range mustRender(t, testCR()) {
		if _, err := r.resource(o).Get(ctx, o.GetName(), metav1.GetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Repeated reconcile must not create duplicate operands or fail on ownership.
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secret, _ := c.Resource(secretGVR).Namespace(Namespace).Get(ctx, "credentials", metav1.GetOptions{})
	secret.SetResourceVersion("2")
	c.Resource(secretGVR).Namespace(Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	nodeGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	node, _ := c.Resource(nodeGVR).Namespace(Namespace).Get(ctx, "csi-node", metav1.GetOptions{})
	containers, _, _ := unstructured.NestedSlice(node.Object, "spec", "template", "spec", "containers")
	containers[0].(map[string]interface{})["image"] = "drift"
	unstructured.SetNestedSlice(node.Object, containers, "spec", "template", "spec", "containers")
	c.Resource(nodeGVR).Namespace(Namespace).Update(ctx, node, metav1.UpdateOptions{})
	r.Images["driver"] = "example.invalid/driver:upgrade"
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	node, _ = c.Resource(nodeGVR).Namespace(Namespace).Get(ctx, "csi-node", metav1.GetOptions{})
	version, _, _ := unstructured.NestedString(node.Object, "spec", "template", "metadata", "annotations", "storage.hammerspace.com/credentials-version")
	if version != "secret-uid/2" {
		t.Fatalf("Secret rotation did not roll workloads: %q", version)
	}
	containers, _, _ = unstructured.NestedSlice(node.Object, "spec", "template", "spec", "containers")
	for _, v := range containers {
		m := v.(map[string]interface{})
		if m["image"] == "drift" {
			t.Fatal("drift not repaired")
		}
		if m["name"] == "hs-csi-plugin-node" && m["image"] != r.Images["driver"] {
			t.Fatal("upgrade not applied")
		}
	}
}

// Apply patches to existing operands carry the UID, so a replaced object is
// rejected, but no resourceVersion: workload status changes between GET and
// PATCH would otherwise fail the patch with 409 and mark the CR Degraded.
func TestApplyPatchesPinUIDNotResourceVersion(t *testing.T) {
	r, c := harness(t)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// The fake tracker assigns no UIDs or resourceVersions; set them as the apiserver would.
	for _, o := range mustRender(t, testCR()) {
		current, err := r.resource(o).Get(ctx, o.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		current.SetUID(types.UID("uid-" + o.GetKind() + "-" + o.GetName()))
		current.SetResourceVersion("42")
		if _, err := r.resource(o).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var patches []map[string]interface{}
	c.PrependReactor("patch", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		a := action.(ktesting.PatchAction)
		if a.GetPatchType() == types.ApplyPatchType {
			var patch map[string]interface{}
			if err := json.Unmarshal(a.GetPatch(), &patch); err != nil {
				t.Fatal(err)
			}
			patches = append(patches, patch)
		}
		return false, nil, nil
	})
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(patches) == 0 {
		t.Fatal("second reconcile applied no patches")
	}
	for _, patch := range patches {
		meta := patch["metadata"].(map[string]interface{})
		if rv, ok := meta["resourceVersion"]; ok {
			t.Errorf("%s %s: apply patch carries resourceVersion %v", patch["kind"], meta["name"], rv)
		}
		if uid, want := meta["uid"], fmt.Sprintf("uid-%s-%s", patch["kind"], meta["name"]); uid != want {
			t.Errorf("%s %s: apply patch uid = %v, want %s", patch["kind"], meta["name"], uid, want)
		}
	}
}

func TestOwnershipConflictDoesNotChangeOperands(t *testing.T) {
	r, c := harness(t)
	ctx := context.Background()
	unowned := object(map[string]interface{}{"apiVersion": "storage.k8s.io/v1", "kind": "CSIDriver", "metadata": map[string]interface{}{"name": DriverName}, "spec": map[string]interface{}{"storageCapacity": true}})
	if _, err := r.resource(unowned).Create(ctx, unowned, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.ClearActions()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Actions() {
		if a.GetVerb() == "create" || a.GetVerb() == "patch" || a.GetVerb() == "delete" {
			t.Fatalf("mutation on conflict: %v", a)
		}
	}
	got, _ := r.resource(unowned).Get(ctx, DriverName, metav1.GetOptions{})
	if !reflect.DeepEqual(got.Object, unowned.Object) {
		t.Fatal("adopted existing driver")
	}
	cr, _ := c.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	conditions, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	if conditions[0].(map[string]interface{})["reason"] != "OwnershipConflict" {
		t.Fatal("missing conflict status")
	}
}

func TestMissingCredentialsAndCleanupPreserveUserData(t *testing.T) {
	r, c := harness(t)
	ctx := context.Background()
	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secret, _ := c.Resource(secretGVR).Namespace(Namespace).Get(ctx, "credentials", metav1.GetOptions{})
	c.Resource(secretGVR).Namespace(Namespace).Delete(ctx, "credentials", metav1.DeleteOptions{})
	c.ClearActions()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("created operands without credentials")
		}
	}
	c.Resource(secretGVR).Namespace(Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cr, _ := c.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	now := metav1.Now()
	cr.SetDeletionTimestamp(&now)
	c.Resource(DriverResource).Update(ctx, cr, metav1.UpdateOptions{})
	c.ClearActions()
	for i := 0; i < 3; i++ {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range c.Actions() {
		if a.GetVerb() == "delete" {
			switch a.GetResource().Resource {
			case "secrets", "namespaces", "persistentvolumes", "persistentvolumeclaims":
				t.Fatalf("deleted user data: %v", a)
			}
		}
	}
	if _, err := c.Resource(secretGVR).Namespace(Namespace).Get(ctx, "credentials", metav1.GetOptions{}); err != nil {
		t.Fatal("Secret removed")
	}
	cr, _ = c.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	if hasFinalizer(cr) {
		t.Fatal("cleanup did not finish")
	}
	node := find(t, mustRender(t, testCR()), "DaemonSet", "csi-node")
	if _, err := r.resource(node).Get(ctx, node.GetName(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("node not removed")
	}
}

func TestCleanupAfterConfigurationBecomesInvalid(t *testing.T) {
	r, client := harness(t)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cr, err := client.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Older CRD versions admitted this name. Uninstall must work even when a
	// stored resource no longer passes the current runtime validation.
	unstructured.SetNestedField(cr.Object, "a..b", "spec", "credentialsSecretName")
	if _, err := ParseSpec(cr); err == nil {
		t.Fatal("regression fixture must fail semantic validation")
	}
	now := metav1.Now()
	cr.SetDeletionTimestamp(&now)
	if _, err := client.Resource(DriverResource).Update(ctx, cr, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cr, err = client.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hasFinalizer(cr) {
		t.Fatal("invalid configuration stranded the cleanup finalizer")
	}
	for _, operand := range mustRender(t, testCR()) {
		if _, err := r.resource(operand).Get(ctx, operand.GetName(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("owned %s %s remains after cleanup: %v", operand.GetKind(), operand.GetName(), err)
		}
	}
}

func TestWorkloadReadinessRejectsStaleAndEmptyRollouts(t *testing.T) {
	for _, kind := range []string{"DaemonSet", "StatefulSet"} {
		t.Run(kind, func(t *testing.T) {
			o := object(map[string]interface{}{"kind": kind, "metadata": map[string]interface{}{"generation": int64(2)}, "spec": map[string]interface{}{"replicas": int64(1)}, "status": map[string]interface{}{"observedGeneration": int64(1)}})
			if workloadReady(o) {
				t.Fatal("stale rollout reported ready")
			}
			status := map[string]interface{}{"observedGeneration": int64(2), "readyReplicas": int64(1), "updatedReplicas": int64(1), "currentRevision": "rev2", "updateRevision": "rev2", "desiredNumberScheduled": int64(1), "numberReady": int64(1), "updatedNumberScheduled": int64(1), "numberUnavailable": int64(0)}
			o.Object["status"] = status
			if !workloadReady(o) {
				t.Fatal(fmt.Sprintf("healthy %s not ready", kind))
			}
			status["updatedReplicas"] = int64(0)
			status["updatedNumberScheduled"] = int64(0)
			if workloadReady(o) {
				t.Fatal("old ready pods hid pending upgrade")
			}
		})
	}
}

func TestFailingWorkloadPodMarksDegraded(t *testing.T) {
	podGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	pod := func(reason string, restarts int64) *unstructured.Unstructured {
		return object(map[string]interface{}{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]interface{}{"name": "csi-node-abc", "namespace": Namespace, "labels": map[string]interface{}{"app": "csi-node"}},
			"status": map[string]interface{}{"containerStatuses": []interface{}{map[string]interface{}{
				"name": "hs-csi-plugin-node", "restartCount": restarts,
				"state": map[string]interface{}{"waiting": map[string]interface{}{"reason": reason}},
			}}},
		})
	}
	for _, tc := range []struct {
		name           string
		pod            *unstructured.Unstructured
		reason         string
		degraded, prog string
	}{
		{"no pods yet", nil, "RollingOut", "False", "True"},
		{"early crash during rollout", pod("CrashLoopBackOff", 1), "RollingOut", "False", "True"},
		{"still creating", pod("ContainerCreating", 0), "RollingOut", "False", "True"},
		{"crash loop", pod("CrashLoopBackOff", 5), "WorkloadFailing", "True", "False"},
		{"image pull", pod("ImagePullBackOff", 0), "WorkloadFailing", "True", "False"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := harness(t)
			ctx := context.Background()
			if tc.pod != nil {
				if _, err := c.Resource(podGVR).Namespace(Namespace).Create(ctx, tc.pod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			cr, err := c.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			conditions, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
			got := map[string]string{}
			for _, item := range conditions {
				m := item.(map[string]interface{})
				got[m["type"].(string)] = m["status"].(string)
				if m["reason"] != tc.reason {
					t.Errorf("%s reason = %v, want %s", m["type"], m["reason"], tc.reason)
				}
			}
			if got["Degraded"] != tc.degraded || got["Progressing"] != tc.prog || got["Available"] != "False" {
				t.Errorf("conditions = %v, want Degraded=%s Progressing=%s Available=False", got, tc.degraded, tc.prog)
			}
		})
	}
}

// Operands created by earlier releases carry an Update managedFields entry.
// It must become the Operator's Apply entry, or server-side apply cannot drop
// fields the manifest no longer sets (a renumbered port stays as a duplicate).
func TestApplyUpgradesCreateOwnedFieldsToApply(t *testing.T) {
	r, c := harness(t)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	sts, err := c.Resource(gvr).Namespace(Namespace).Get(ctx, "csi-provisioner", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sts.SetManagedFields([]metav1.ManagedFieldsEntry{
		{Manager: fieldManager, Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "apps/v1", FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)}},
		{Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "apps/v1", FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:replicas":{}}}`)}, Subresource: "status"},
	})
	if _, err := c.Resource(gvr).Namespace(Namespace).Update(ctx, sts, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	sts, err = c.Resource(gvr).Namespace(Namespace).Get(ctx, "csi-provisioner", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range sts.GetManagedFields() {
		got = append(got, fmt.Sprintf("%s/%s/%s", m.Manager, m.Operation, m.Subresource))
	}
	want := []string{fieldManager + "/Apply/", "kube-controller-manager/Update/status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managedFields = %v, want %v", got, want)
	}
}
