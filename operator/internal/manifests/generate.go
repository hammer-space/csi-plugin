// Package manifests derives the Operator's embedded operands from the manual
// deployment manifest. Only Operator-specific policy belongs in this package.
package manifests

import (
	"encoding/json"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Generate applies the OpenShift installation defaults without maintaining a
// second copy of the CSI workloads and RBAC. Runtime CR settings are applied by
// operator.Render after these templates have been embedded in the executable.
func Generate(source io.Reader) ([]byte, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(source, 4096)
	var objects []map[string]interface{}
	for {
		var v map[string]interface{}
		if err := decoder.Decode(&v); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode source manifest: %w", err)
		}
		if len(v) == 0 {
			continue
		}
		o := &unstructured.Unstructured{Object: v}
		if o.GetName() == "" || o.GetAPIVersion() == "" {
			return nil, fmt.Errorf("manifest object requires apiVersion and metadata.name")
		}
		o.SetNamespace("") // Render sets the operand namespace at runtime.
		switch o.GetKind() {
		case "CSIDriver", "Service", "ServiceAccount", "ConfigMap":
		case "ClusterRole":
			o.SetName("hammerspace-" + o.GetName())
			rules, _, err := unstructured.NestedSlice(v, "rules")
			if err != nil {
				return nil, err
			}
			var filtered []interface{}
			for _, entry := range rules {
				rule, ok := entry.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("%s: invalid RBAC rule", o.GetName())
				}
				groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
				resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
				var retained []string
				for _, resource := range resources {
					// Kubelet supplies Secret env values. The driver does not
					// need Secret reads or permission to manage cluster CRDs.
					if len(groups) == 1 && ((groups[0] == "" && resource == "secrets") ||
						(groups[0] == "apiextensions.k8s.io" && resource == "customresourcedefinitions")) {
						continue
					}
					retained = append(retained, resource)
				}
				if len(retained) > 0 {
					unstructured.SetNestedStringSlice(rule, retained, "resources")
					filtered = append(filtered, rule)
				}
			}
			unstructured.SetNestedSlice(v, filtered, "rules")
		case "ClusterRoleBinding":
			o.SetName("hammerspace-" + o.GetName())
			name, _, err := unstructured.NestedString(v, "roleRef", "name")
			if err != nil || name == "" {
				return nil, fmt.Errorf("%s: missing roleRef.name", o.GetName())
			}
			unstructured.SetNestedField(v, "hammerspace-"+name, "roleRef", "name")
		case "StatefulSet", "DaemonSet":
			if err := configureWorkload(v); err != nil {
				return nil, fmt.Errorf("%s: %w", o.GetName(), err)
			}
		default:
			// New resource kinds need corresponding reconciliation and
			// cleanup support before the Operator may own them.
			return nil, fmt.Errorf("unsupported operand kind %q", o.GetKind())
		}
		objects = append(objects, v)
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("source manifest contains no operands")
	}
	data, err := json.MarshalIndent(objects, "", "  ")
	return append(data, '\n'), err
}

func configureWorkload(v map[string]interface{}) error {
	pod, found, err := unstructured.NestedMap(v, "spec", "template", "spec")
	if err != nil || !found {
		return fmt.Errorf("missing pod spec")
	}
	if account, ok := pod["serviceAccount"]; ok {
		if _, exists := pod["serviceAccountName"]; !exists {
			pod["serviceAccountName"] = account
		}
		delete(pod, "serviceAccount")
	}
	unstructured.SetNestedField(pod, "linux", "nodeSelector", "kubernetes.io/os")
	containers, found, err := unstructured.NestedSlice(pod, "containers")
	if err != nil || !found || len(containers) == 0 {
		return fmt.Errorf("missing containers")
	}
	for _, entry := range containers {
		container, ok := entry.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid container")
		}
		container["imagePullPolicy"] = "IfNotPresent"
		for resource, value := range map[string]string{"cpu": "10m", "memory": "64Mi"} {
			if _, found, _ := unstructured.NestedFieldNoCopy(container, "resources", "requests", resource); !found {
				unstructured.SetNestedField(container, value, "resources", "requests", resource)
			}
		}
		if container["name"] == "driver-registrar" {
			// The registrar manages its sockets. Its minimal image does
			// not promise a shell for the manual manifest's cleanup hook.
			if lifecycle, ok := container["lifecycle"].(map[string]interface{}); ok {
				delete(lifecycle, "preStop")
				if len(lifecycle) == 0 {
					delete(container, "lifecycle")
				}
			}
		}
		if container["name"] == "hs-csi-plugin-controller" {
			mounts, _, err := unstructured.NestedSlice(container, "volumeMounts")
			if err != nil {
				return err
			}
			if !hasName(mounts, "rootshare-dir") {
				mounts = append(mounts, map[string]interface{}{"name": "rootshare-dir", "mountPath": "/var/lib/hammerspace/", "mountPropagation": "Bidirectional"})
			}
			unstructured.SetNestedSlice(container, mounts, "volumeMounts")
		}
	}
	unstructured.SetNestedSlice(pod, containers, "containers")
	volumes, _, err := unstructured.NestedSlice(pod, "volumes")
	if err != nil {
		return err
	}
	if !hasName(volumes, "rootshare-dir") {
		volumes = append(volumes, map[string]interface{}{"name": "rootshare-dir", "hostPath": map[string]interface{}{"path": "/var/lib/hammerspace/", "type": "DirectoryOrCreate"}})
	}
	for _, entry := range volumes {
		volume, ok := entry.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid volume")
		}
		if volume["name"] == "rootshare-dir" {
			if _, found, _ := unstructured.NestedFieldNoCopy(volume, "hostPath", "type"); !found {
				unstructured.SetNestedField(volume, "DirectoryOrCreate", "hostPath", "type")
			}
		}
	}
	unstructured.SetNestedSlice(pod, volumes, "volumes")
	return unstructured.SetNestedMap(v, pod, "spec", "template", "spec")
}

func hasName(entries []interface{}, name string) bool {
	for _, entry := range entries {
		if v, ok := entry.(map[string]interface{}); ok && v["name"] == name {
			return true
		}
	}
	return false
}
