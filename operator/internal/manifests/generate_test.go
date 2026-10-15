package manifests

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const canonicalManifest = "../../../deploy/kubernetes/kubernetes-1.36/plugin.yaml"

func TestEmbeddedOperandsMatchCanonicalManifest(t *testing.T) {
	source, err := os.Open(canonicalManifest)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	want, err := Generate(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../operator/operands.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("embedded operands are stale; run make -C operator generate from the repository root")
	}
}

func TestSourceChangesPropagateAndOperatorPolicyIsPreserved(t *testing.T) {
	source, err := os.ReadFile(canonicalManifest)
	if err != nil {
		t.Fatal(err)
	}
	// A sidecar argument change must reach both workloads through generation.
	changed := strings.ReplaceAll(string(source), "--v=5", "--v=9")
	generated, err := Generate(strings.NewReader(changed))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(generated, []byte("--v=5")) || !bytes.Contains(generated, []byte("--v=9")) {
		t.Fatal("source manifest changes did not propagate")
	}
	var objects []map[string]interface{}
	if err := json.Unmarshal(generated, &objects); err != nil {
		t.Fatal(err)
	}
	roles, workloads := 0, 0
	for _, obj := range objects {
		meta := obj["metadata"].(map[string]interface{})
		if _, exists := meta["namespace"]; exists {
			t.Fatal("template must leave the namespace to the renderer")
		}
		switch obj["kind"] {
		case "ClusterRole":
			roles++
			if !strings.HasPrefix(meta["name"].(string), "hammerspace-") {
				t.Fatal("Operator role name is not isolated")
			}
			for _, rule := range obj["rules"].([]interface{}) {
				for _, resource := range rule.(map[string]interface{})["resources"].([]interface{}) {
					if resource == "secrets" || resource == "customresourcedefinitions" {
						t.Fatalf("unnecessary driver RBAC: %v", resource)
					}
				}
			}
		case "StatefulSet", "DaemonSet":
			workloads++
			pod := obj["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
			if _, exists := pod["serviceAccount"]; exists || pod["serviceAccountName"] == nil {
				t.Fatal("workload must use serviceAccountName")
			}
			if !hasName(pod["volumes"].([]interface{}), "rootshare-dir") {
				t.Fatal("workload needs a persistent Hammerspace directory")
			}
			for _, entry := range pod["containers"].([]interface{}) {
				container := entry.(map[string]interface{})
				if container["imagePullPolicy"] != "IfNotPresent" || container["resources"] == nil {
					t.Fatal("missing Operator container defaults")
				}
				if container["name"] == "driver-registrar" && container["lifecycle"] != nil {
					t.Fatal("registrar cannot rely on a shell cleanup hook")
				}
				if container["name"] == "hs-csi-plugin-controller" && !hasName(container["volumeMounts"].([]interface{}), "rootshare-dir") {
					t.Fatal("controller must mount the persistent Hammerspace directory")
				}
			}
		}
	}
	if roles == 0 || workloads != 2 {
		t.Fatalf("unexpected operand inventory: %d roles, %d workloads", roles, workloads)
	}
}

func TestRejectInvalidOrUnsupportedManifests(t *testing.T) {
	for _, source := range []string{
		"# empty manifest\n---\n",
		"[broken yaml",
		"apiVersion: v1\nkind: Secret\nmetadata:\n  name: credentials\n",
		"apiVersion: v1\nkind: Service\n",
	} {
		if _, err := Generate(strings.NewReader(source)); err == nil {
			t.Fatalf("unexpectedly accepted manifest %q", source)
		}
	}
}
