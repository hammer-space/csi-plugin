package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/csaupgrade"
)

const fieldManager = "hammerspace-csi-operator"

type Reconciler struct {
	Client dynamic.Interface
	Images Images
}

func (r *Reconciler) resource(o *unstructured.Unstructured) dynamic.ResourceInterface {
	return r.Client.Resource(Resource(o)).Namespace(o.GetNamespace())
}

func ownedBy(o, cr *unstructured.Unstructured) bool {
	for _, owner := range o.GetOwnerReferences() {
		if owner.UID == cr.GetUID() && owner.APIVersion == cr.GetAPIVersion() && owner.Kind == cr.GetKind() && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func (r *Reconciler) Reconcile(ctx context.Context) error {
	cr, err := r.Client.Resource(DriverResource).Get(ctx, "cluster", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	spec, err := ParseSpec(cr)
	// An invalid mutable setting must not strand the cleanup finalizer. ParseSpec
	// returns the decoded spec even when semantic validation fails; cleanup only
	// needs its immutable StorageClass inventory and checks every object's owner.
	if cr.GetDeletionTimestamp() != nil {
		return r.cleanup(ctx, cr, spec)
	}
	if err != nil {
		return r.condition(ctx, cr, false, false, "InvalidConfiguration", err.Error())
	}
	ns := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	if _, err := r.Client.Resource(ns).Get(ctx, Namespace, metav1.GetOptions{}); err != nil {
		return r.condition(ctx, cr, false, false, "NamespaceMissing", "Create the hammerspace-csi operand namespace before installation")
	}
	secret, err := r.Client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(Namespace).Get(ctx, spec.CredentialsSecretName, metav1.GetOptions{})
	if err != nil {
		return r.condition(ctx, cr, false, false, "CredentialsUnavailable", "Cannot read the referenced credentials Secret")
	}
	for _, key := range []string{"username", "password", "endpoint"} {
		value, _, _ := unstructured.NestedString(secret.Object, "data", key)
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) == 0 {
			return r.condition(ctx, cr, false, false, "InvalidCredentials", "The Secret must contain nonempty username, password and endpoint keys")
		}
	}
	desired, err := Render(cr, spec, r.Images, string(secret.GetUID())+"/"+secret.GetResourceVersion())
	if err != nil {
		return err
	}
	// Preflight the entire set before making any operand changes. Never adopt an
	// existing manually installed CSI driver or someone else's StorageClass.
	for _, o := range desired {
		current, err := r.resource(o).Get(ctx, o.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !ownedBy(current, cr) {
			return r.condition(ctx, cr, false, false, "OwnershipConflict", fmt.Sprintf("%s %s already exists without this Operator's ownership", o.GetKind(), o.GetName()))
		}
	}
	if !hasFinalizer(cr) {
		cr.SetFinalizers(append(cr.GetFinalizers(), Finalizer))
		cr, err = r.Client.Resource(DriverResource).Update(ctx, cr, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	// Workloads go last, after admission permissions, accounts and configuration.
	sort.SliceStable(desired, func(i, j int) bool { return workload(desired[i]) == false && workload(desired[j]) })
	for _, o := range desired {
		if err := r.apply(ctx, cr, o); err != nil {
			if statusErr := r.condition(ctx, cr, false, false, "ReconcileFailed", "An operand could not be reconciled; inspect Operator logs and cluster events"); statusErr != nil {
				return statusErr
			}
			return err
		}
	}
	ready := true
	for _, o := range desired {
		if !workload(o) {
			continue
		}
		current, err := r.resource(o).Get(ctx, o.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if workloadReady(current) {
			continue
		}
		ready = false
		// A rollout and a broken workload both look "not ready". Without this
		// the CR reported RollingOut indefinitely while the driver crash-looped.
		failure, err := r.workloadFailure(ctx, current)
		if err != nil {
			return err
		}
		if failure != "" {
			return r.condition(ctx, cr, false, false, "WorkloadFailing", failure)
		}
	}
	if ready {
		return r.condition(ctx, cr, true, false, "Ready", "Controller and node workloads are ready")
	}
	return r.condition(ctx, cr, false, true, "RollingOut", "Waiting for controller and node workloads")
}

// crashLoopRestarts is how many restarts a CrashLoopBackOff container needs
// before it counts as failing. Kubernetes reports CrashLoopBackOff after the
// first crash; one or two can happen in a normal rollout (e.g. a slow
// dependency), and three back-offs (10s, 20s, 40s) are over a minute of failing.
const crashLoopRestarts = 3

// Waiting reasons that do not resolve without a change to the image or spec.
var stuckWaitingReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

// workloadFailure returns a description of the first workload pod container
// that is failing rather than starting, or "" if none is.
func (r *Reconciler) workloadFailure(ctx context.Context, o *unstructured.Unstructured) (string, error) {
	selector, _, err := unstructured.NestedStringMap(o.Object, "spec", "selector", "matchLabels")
	if err != nil || len(selector) == 0 {
		return "", err
	}
	pods, err := r.Client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(o.GetNamespace()).
		List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(selector).String()})
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
		for _, item := range statuses {
			c, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(c, "name")
			reason, _, _ := unstructured.NestedString(c, "state", "waiting", "reason")
			restarts, _, _ := unstructured.NestedInt64(c, "restartCount")
			if stuckWaitingReasons[reason] || (reason == "CrashLoopBackOff" && restarts >= crashLoopRestarts) {
				return fmt.Sprintf("%s %s: pod %s container %s is %s (%d restarts); inspect the pod events and container logs",
					o.GetKind(), o.GetName(), pod.GetName(), name, reason, restarts), nil
			}
		}
	}
	return "", nil
}

func (r *Reconciler) apply(ctx context.Context, cr, desired *unstructured.Unstructured) error {
	current, err := r.resource(desired).Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = r.resource(desired).Create(ctx, desired, metav1.CreateOptions{FieldManager: fieldManager})
		return err // AlreadyExists is retried with ownership checking next time.
	}
	if err != nil {
		return err
	}
	if !ownedBy(current, cr) {
		return fmt.Errorf("ownership changed for %s %s", desired.GetKind(), desired.GetName())
	}
	if current, err = r.upgradeManagedFields(ctx, current); err != nil {
		return err
	}
	// The UID stops a delete/recreate between GET and PATCH from adopting an
	// unrelated replacement with the same name: metadata.uid is immutable, so
	// the apiserver rejects the patch if the object was replaced. No
	// resourceVersion: kube-controller-manager updates workload status
	// constantly, and every such update would turn this patch into a 409
	// Conflict and the CR into Degraded during a routine rollout.
	desired.SetResourceVersion("")
	desired.SetUID(current.GetUID())
	b, err := json.Marshal(desired.Object)
	if err != nil {
		return err
	}
	force := true // Only fields of resources already owned by this CR.
	_, err = r.resource(desired).Patch(ctx, desired.GetName(), types.ApplyPatchType, b, metav1.PatchOptions{FieldManager: fieldManager, Force: &force})
	return err
}

// upgradeManagedFields hands fields this Operator set with a plain create or
// update over to its server-side apply manager. Server-side apply only removes
// fields its own Apply entry owns, so without this a field dropped from the
// manifest stays behind. Changing a list item's key, such as a port number,
// then leaves the old item next to the new one, and the API server rejects
// the duplicate port name. Earlier releases created every operand that way.
func (r *Reconciler) upgradeManagedFields(ctx context.Context, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	const attempts = 3 // The patch tests resourceVersion, which workload status updates keep moving.
	for i := 0; ; i++ {
		patch, err := csaupgrade.UpgradeManagedFieldsPatch(current, sets.New(fieldManager), fieldManager)
		if err != nil || patch == nil {
			return current, err
		}
		upgraded, err := r.resource(current).Patch(ctx, current.GetName(), types.JSONPatchType, patch, metav1.PatchOptions{})
		if err == nil || i+1 == attempts {
			return upgraded, err
		}
		if current, err = r.resource(current).Get(ctx, current.GetName(), metav1.GetOptions{}); err != nil {
			return nil, err
		}
	}
}

func workload(o *unstructured.Unstructured) bool {
	return o.GetKind() == "StatefulSet" || o.GetKind() == "DaemonSet"
}
func workloadReady(o *unstructured.Unstructured) bool {
	get := func(fields ...string) int64 { v, _, _ := unstructured.NestedInt64(o.Object, fields...); return v }
	if get("status", "observedGeneration") < o.GetGeneration() {
		return false
	}
	if o.GetKind() == "StatefulSet" {
		want := get("spec", "replicas")
		current, _, _ := unstructured.NestedString(o.Object, "status", "currentRevision")
		update, _, _ := unstructured.NestedString(o.Object, "status", "updateRevision")
		return want > 0 && get("status", "readyReplicas") == want && get("status", "updatedReplicas") == want && current != "" && current == update
	}
	want := get("status", "desiredNumberScheduled")
	return want > 0 && get("status", "numberReady") == want && get("status", "updatedNumberScheduled") == want && get("status", "numberUnavailable") == 0
}

func hasFinalizer(cr *unstructured.Unstructured) bool {
	for _, f := range cr.GetFinalizers() {
		if f == Finalizer {
			return true
		}
	}
	return false
}

func (r *Reconciler) cleanup(ctx context.Context, cr *unstructured.Unstructured, spec Spec) error {
	if !hasFinalizer(cr) {
		return nil
	}
	objects, err := Render(cr, spec, r.Images, "")
	if err != nil {
		return err
	}
	// Stop workloads before withdrawing their accounts, RBAC and SCC. Do not
	// enumerate or delete any PVC, PV, Secret, Namespace or backend data.
	for _, workloadsOnly := range []bool{true, false} {
		pending := false
		for _, o := range objects {
			if workload(o) != workloadsOnly {
				continue
			}
			current, err := r.resource(o).Get(ctx, o.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !ownedBy(current, cr) {
				continue
			}
			pending = true
			if current.GetDeletionTimestamp() == nil {
				uid := current.GetUID()
				policy := metav1.DeletePropagationForeground
				if err := r.resource(o).Delete(ctx, o.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
		if pending {
			return r.condition(ctx, cr, false, true, "Removing", "Removing Operator-owned resources; persistent data is retained")
		}
	}
	var finalizers []string
	for _, f := range cr.GetFinalizers() {
		if f != Finalizer {
			finalizers = append(finalizers, f)
		}
	}
	cr.SetFinalizers(finalizers)
	_, err = r.Client.Resource(DriverResource).Update(ctx, cr, metav1.UpdateOptions{})
	return err
}

func (r *Reconciler) condition(ctx context.Context, cr *unstructured.Unstructured, available, progressing bool, reason, message string) error {
	old, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	var conditions []interface{}
	for _, item := range []struct {
		name  string
		value bool
	}{{"Available", available}, {"Progressing", progressing}, {"Degraded", !available && !progressing}} {
		value := "False"
		if item.value {
			value = "True"
		}
		transition := time.Now().UTC().Format(time.RFC3339)
		for _, c := range old {
			m := c.(map[string]interface{})
			if m["type"] == item.name && m["status"] == value {
				if s, ok := m["lastTransitionTime"].(string); ok {
					transition = s
				}
			}
		}
		conditions = append(conditions, map[string]interface{}{"type": item.name, "status": value, "reason": reason, "message": message, "observedGeneration": cr.GetGeneration(), "lastTransitionTime": transition})
	}
	status := map[string]interface{}{"observedGeneration": cr.GetGeneration(), "conditions": conditions}
	imageStatus := map[string]interface{}{}
	for k, v := range r.Images {
		imageStatus[k] = v
	}
	status["images"] = imageStatus
	if reflect.DeepEqual(cr.Object["status"], status) {
		return nil
	}
	cr.Object["status"] = status
	_, err := r.Client.Resource(DriverResource).UpdateStatus(ctx, cr, metav1.UpdateOptions{})
	return err
}
