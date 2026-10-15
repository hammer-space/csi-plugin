package manifests

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDevelopmentImagesStayInSync(t *testing.T) {
	operands, err := os.ReadFile("../operator/operands.json")
	if err != nil {
		t.Fatal(err)
	}
	images, err := ImageDefaults(operands)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := os.ReadFile("../../config/development-images.json")
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal(inventory, &actual); err != nil {
		t.Fatal(err)
	}
	if len(images) != len(actual) {
		t.Fatal("development image inventory differs from canonical manifest")
	}
	for key, image := range images {
		if actual[key] != image {
			t.Fatalf("%s: inventory %s differs from manifest %s", key, actual[key], image)
		}
	}
	manager, err := os.ReadFile("../../config/manager.yaml")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := ManagerImages(bytes.NewReader(manager), images)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manager, generated) {
		t.Fatal("manager image defaults are stale; run make generate")
	}

	images["driver"] = "example.invalid/driver:test-update"
	generated, err = ManagerImages(bytes.NewReader(manager), images)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(generated, []byte(images["driver"])) {
		t.Fatal("new driver image did not propagate to manager")
	}
}

func TestRejectConflictingOrUnmappedOperandImages(t *testing.T) {
	data, err := os.ReadFile("../operator/operands.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"unknown-container", "hs-csi-plugin-node"} {
		changed := strings.ReplaceAll(string(data), `"name": "csi-provisioner"`, `"name": "`+replacement+`"`)
		if _, err := ImageDefaults([]byte(changed)); err == nil {
			t.Fatalf("accepted %s image mapping", replacement)
		}
	}
}
