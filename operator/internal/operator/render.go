package operator

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hammer-space/csi-plugin/operator/internal/manifests"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

// DefaultNamespace is the suggested install namespace. The operator manages its
// operands in whatever namespace OLM installs it into.
const DefaultNamespace = "hammerspace-csi"
const DriverName = "com.hammerspace.csi"
const Finalizer = "storage.hammerspace.com/operands"

var DriverResource = schema.GroupVersionResource{Group: "storage.hammerspace.com", Version: "v1alpha1", Resource: "hammerspacecsidrivers"}

// operands.json is generated from deploy/kubernetes/kubernetes-1.36/plugin.yaml
// by make generate. Do not edit the embedded copy by hand.
//
//go:embed operands.json
var operands []byte

type Spec struct {
	CredentialsSecretName string              `json:"credentialsSecretName"`
	TLSVerify             *bool               `json:"tlsVerify,omitempty"`
	LogLevel              string              `json:"logLevel,omitempty"`
	NodeSelector          map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations           []corev1.Toleration `json:"tolerations,omitempty"`
	StorageClasses        []StorageClass      `json:"storageClasses,omitempty"`
}

type StorageClass struct {
	Name             string `json:"name"`
	Mode             string `json:"mode"`
	BackingShareName string `json:"backingShareName,omitempty"`
}

// Images is release configuration, shared with the bundle's relatedImages.
// Every image must be explicit: never silently deploy an untested driver tag.
type Images map[string]string

var ImageKeys = []string{"driver", "provisioner", "attacher", "snapshotter", "resizer", "registrar", "livenessprobe"}

func ParseSpec(cr *unstructured.Unstructured) (Spec, error) {
	var spec Spec
	b, err := json.Marshal(cr.Object["spec"])
	if err != nil {
		return spec, err
	}
	if err = json.Unmarshal(b, &spec); err != nil {
		return spec, err
	}
	if cr.GetName() != "cluster" {
		return spec, fmt.Errorf("only the singleton named cluster is supported")
	}
	if len(validation.IsDNS1123Subdomain(spec.CredentialsSecretName)) != 0 {
		return spec, fmt.Errorf("credentialsSecretName must be a valid Secret name")
	}
	if spec.LogLevel == "" {
		spec.LogLevel = "info"
	}
	switch spec.LogLevel {
	case "error", "warn", "info", "debug":
	default:
		return spec, fmt.Errorf("invalid logLevel")
	}
	if os, ok := spec.NodeSelector["kubernetes.io/os"]; ok && os != "linux" {
		return spec, fmt.Errorf("only Linux nodes are supported")
	}
	names := map[string]bool{}
	for _, sc := range spec.StorageClasses {
		if len(validation.IsDNS1123Subdomain(sc.Name)) != 0 || names[sc.Name] {
			return spec, fmt.Errorf("StorageClass names must be valid and unique")
		}
		names[sc.Name] = true
		switch sc.Mode {
		case "NFS":
			if sc.BackingShareName != "" {
				return spec, fmt.Errorf("NFS classes use independent shares; omit backingShareName")
			}
		case "Block", "ext4", "xfs":
			if sc.BackingShareName == "" || strings.ContainsAny(sc.BackingShareName, "/|\n") {
				return spec, fmt.Errorf("file-backed classes require a backing share name without path separators")
			}
		default:
			return spec, fmt.Errorf("unsupported StorageClass mode")
		}
	}
	return spec, nil
}

func object(v map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: v}
}

func Render(cr *unstructured.Unstructured, spec Spec, images Images, secretVersion, namespace string) ([]*unstructured.Unstructured, error) {
	for _, key := range ImageKeys {
		if images[key] == "" {
			return nil, fmt.Errorf("missing release image %s", key)
		}
	}
	var raw []map[string]interface{}
	if err := json.Unmarshal(operands, &raw); err != nil {
		return nil, err
	}
	var result []*unstructured.Unstructured
	for _, v := range raw {
		o := object(v)
		switch o.GetKind() {
		case "Service", "ServiceAccount", "ConfigMap", "StatefulSet", "DaemonSet":
			o.SetNamespace(namespace)
		}
		if o.GetKind() == "ClusterRoleBinding" {
			subjects, _, _ := unstructured.NestedSlice(v, "subjects")
			for _, sub := range subjects {
				sub.(map[string]interface{})["namespace"] = namespace
			}
			unstructured.SetNestedSlice(v, subjects, "subjects")
		}
		if o.GetKind() == "StatefulSet" || o.GetKind() == "DaemonSet" {
			pod, _, _ := unstructured.NestedMap(v, "spec", "template", "spec")
			containers := pod["containers"].([]interface{})
			for _, c := range containers {
				container := c.(map[string]interface{})
				name := container["name"].(string)
				key, ok := manifests.ContainerImageKey(name)
				if !ok || images[key] == "" {
					return nil, fmt.Errorf("no release image for container %q", name)
				}
				container["image"] = images[key]
				envVars, _, err := unstructured.NestedSlice(container, "env")
				if err != nil {
					return nil, err
				}
				for _, e := range envVars {
					env := e.(map[string]interface{})
					if ref, ok := env["valueFrom"].(map[string]interface{}); ok {
						if secret, ok := ref["secretKeyRef"].(map[string]interface{}); ok {
							secret["name"] = spec.CredentialsSecretName
						}
					}
					if env["name"] == "HS_TLS_VERIFY" {
						env["value"] = fmt.Sprint(spec.TLSVerify == nil || *spec.TLSVerify)
					}
					if env["name"] == "LOG_LEVEL" {
						env["value"] = spec.LogLevel
					}
				}
				unstructured.SetNestedSlice(container, envVars, "env")
			}
			selector := map[string]interface{}{"kubernetes.io/os": "linux"}
			for k, val := range spec.NodeSelector {
				selector[k] = val
			}
			pod["nodeSelector"] = selector
			if len(spec.Tolerations) > 0 {
				b, _ := json.Marshal(spec.Tolerations)
				var tolerations []interface{}
				json.Unmarshal(b, &tolerations)
				pod["tolerations"] = tolerations
			}
			unstructured.SetNestedMap(v, pod, "spec", "template", "spec")
			annotations, _, err := unstructured.NestedStringMap(v, "spec", "template", "metadata", "annotations")
			if err != nil {
				return nil, err
			}
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["storage.hammerspace.com/credentials-version"] = secretVersion
			unstructured.SetNestedStringMap(v, annotations, "spec", "template", "metadata", "annotations")
		}
		result = append(result, o)
	}
	for _, sc := range spec.StorageClasses {
		params := map[string]interface{}{"volumeNameFormat": "csi-%s"}
		switch sc.Mode {
		case "NFS":
			params["fsType"] = "nfs"
		case "Block":
			params["blockBackingShareName"] = sc.BackingShareName
		default:
			params["fsType"] = sc.Mode
			params["mountBackingShareName"] = sc.BackingShareName
		}
		result = append(result, object(map[string]interface{}{
			"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": map[string]interface{}{"name": sc.Name},
			"provisioner": DriverName, "parameters": params, "reclaimPolicy": "Retain", "volumeBindingMode": "WaitForFirstConsumer", "allowVolumeExpansion": true,
		}))
	}
	// Both driver roles mount NFS; the node role also creates/attaches loop devices.
	result = append(result, object(map[string]interface{}{
		"apiVersion": "security.openshift.io/v1", "kind": "SecurityContextConstraints", "metadata": map[string]interface{}{"name": "hammerspace-csi"},
		"allowPrivilegedContainer": true, "allowPrivilegeEscalation": true, "allowHostDirVolumePlugin": true,
		"allowHostNetwork": true, "allowHostPorts": true, "allowHostPID": false, "allowHostIPC": false,
		"allowedCapabilities": []interface{}{"SYS_ADMIN"}, "requiredDropCapabilities": []interface{}{},
		"runAsUser": map[string]interface{}{"type": "RunAsAny"}, "seLinuxContext": map[string]interface{}{"type": "RunAsAny"},
		"fsGroup": map[string]interface{}{"type": "RunAsAny"}, "supplementalGroups": map[string]interface{}{"type": "RunAsAny"},
		"volumes": []interface{}{"hostPath", "emptyDir", "secret", "configMap", "projected", "downwardAPI"},
		"users":   []interface{}{"system:serviceaccount:" + namespace + ":csi-node", "system:serviceaccount:" + namespace + ":csi-provisioner"},
		"groups":  []interface{}{}, "readOnlyRootFilesystem": false,
	}))
	controller := true
	for _, o := range result {
		o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: cr.GetAPIVersion(), Kind: cr.GetKind(), Name: cr.GetName(), UID: cr.GetUID(), Controller: &controller}})
		labels := o.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["app.kubernetes.io/managed-by"] = "hammerspace-csi-operator"
		o.SetLabels(labels)
	}
	return result, nil
}

func Resource(o *unstructured.Unstructured) schema.GroupVersionResource {
	plurals := map[string]string{"CSIDriver": "csidrivers", "Service": "services", "ServiceAccount": "serviceaccounts", "ConfigMap": "configmaps", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets", "ClusterRole": "clusterroles", "ClusterRoleBinding": "clusterrolebindings", "StorageClass": "storageclasses", "SecurityContextConstraints": "securitycontextconstraints"}
	gv, _ := schema.ParseGroupVersion(o.GetAPIVersion())
	return gv.WithResource(plurals[o.GetKind()])
}
