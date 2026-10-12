package manifests

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/yaml"
)

type policyRule struct {
	APIGroups       []string `json:"apiGroups"`
	Resources       []string `json:"resources"`
	Verbs           []string `json:"verbs"`
	ResourceNames   []string `json:"resourceNames"`
	NonResourceURLs []string `json:"nonResourceURLs"`
}

type ruleHolder struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Rules []policyRule `json:"rules"`
}

// CheckDelegatedRBAC fails when the Operator's ClusterRole in rbacManifest
// does not hold every permission granted by the operand ClusterRoles in the
// generated operands. The Operator has no 'escalate' verb, so Kubernetes
// rejects creating an operand ClusterRole that grants anything the Operator
// lacks. operands.json regenerates from plugin.yaml, but rbac.yaml is
// maintained by hand, so this keeps a new driver rule from wedging installs.
func CheckDelegatedRBAC(operands []byte, rbacManifest io.Reader) error {
	var objects []ruleHolder
	if err := json.Unmarshal(operands, &objects); err != nil {
		return fmt.Errorf("decode operands: %w", err)
	}
	held, err := operatorClusterRules(rbacManifest)
	if err != nil {
		return err
	}
	granted := map[string]bool{}
	for _, rule := range held {
		if len(rule.ResourceNames) > 0 {
			continue // CREATE requests carry no name, so named grants cannot cover delegation.
		}
		for _, verb := range rule.Verbs {
			if verb == "escalate" {
				return fmt.Errorf("operator ClusterRole must not hold the escalate verb")
			}
			for _, group := range rule.APIGroups {
				for _, resource := range rule.Resources {
					granted[group+"|"+resource+"|"+verb] = true
				}
			}
		}
	}
	var missing []string
	for _, object := range objects {
		if object.Kind != "ClusterRole" {
			continue
		}
		for _, rule := range object.Rules {
			if len(rule.NonResourceURLs) > 0 {
				missing = append(missing, fmt.Sprintf("%s: nonResourceURLs %v", object.Metadata.Name, rule.NonResourceURLs))
			}
			for _, group := range rule.APIGroups {
				for _, resource := range rule.Resources {
					for _, verb := range rule.Verbs {
						if !granted[group+"|"+resource+"|"+verb] {
							missing = append(missing, fmt.Sprintf("%s: %s %q/%s", object.Metadata.Name, verb, group, resource))
						}
					}
				}
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("config/rbac.yaml operator ClusterRole lacks permissions it must delegate to operand ClusterRoles; add unscoped grants for:\n  %s",
			strings.Join(missing, "\n  "))
	}
	return nil
}

func operatorClusterRules(manifest io.Reader) ([]policyRule, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(manifest, 4096)
	for {
		var object ruleHolder
		if err := decoder.Decode(&object); err == io.EOF {
			return nil, fmt.Errorf("operator RBAC manifest has no ClusterRole")
		} else if err != nil {
			return nil, fmt.Errorf("decode operator RBAC manifest: %w", err)
		}
		if object.Kind == "ClusterRole" {
			return object.Rules, nil
		}
	}
}
